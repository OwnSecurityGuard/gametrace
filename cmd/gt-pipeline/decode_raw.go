package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/decode"
	"gametrace/pkg/event"
	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/state"
	"gametrace/pkg/store"
)

// rawBatchSize 离线解码时分批读取 raw_packets 的批大小。
// 避免一次性把全部 raw packets 加载进内存。
const rawBatchSize = 1000

// testPluginDefaultSample 是 test_plugin 返回的解码事件采样上限默认值。
const testPluginDefaultSample int64 = 50

// testPluginMaxDataJSON 是单个采样事件 data_json 的最大字节数（预览截断，避免大载荷爆炸）。
const testPluginMaxDataJSON = 4096

// decodeRawOptions 控制离线解码循环的过滤与上限。
type decodeRawOptions struct {
	Protocol string
	Src      string
	Dst      string
	Limit    int64 // 0 = 全部
}

// rawDecodeResult 是单个原始包的解码结果（成功携带事件，失败携带错误）。
type rawDecodeResult struct {
	RawID  string
	Src    string
	Dst    string
	Events []*event.Event
	Err    error
	// Payload 是该原始包的未解码字节，仅供进程内统计（熵估计）使用，
	// 绝不外传前端（隐私安全，见 TestPlugin 注释）。
	Payload []byte
}

// forEachRawDecoded 分批读取会话的 raw_packets 并在进程内解码，
// 对每个原始包调用 onResult。原始字节仅在此函数内存在，绝不外传前端。
// opts.Limit>0 时最多处理 Limit 个原始包。
func forEachRawDecoded(ctx context.Context, st store.Store, dispatcher *decode.Dispatcher, opts decodeRawOptions, onResult func(rawDecodeResult)) error {
	offset := 0
	var totalRaw int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batchLimit := rawBatchSize
		if opts.Limit > 0 {
			remaining := opts.Limit - totalRaw
			if remaining <= 0 {
				break
			}
			if remaining < int64(batchLimit) {
				batchLimit = int(remaining)
			}
		}
		rows, qerr := st.QueryRawPackets(ctx, store.RawPacketQuery{
			Protocol: opts.Protocol,
			Src:      opts.Src,
			Dst:      opts.Dst,
			Limit:    batchLimit,
			Offset:   offset,
		})
		if qerr != nil {
			return fmt.Errorf("query raw batch at offset %d: %w", offset, qerr)
		}
		if len(rows) == 0 {
			break
		}
		totalRaw += int64(len(rows))
		for _, r := range rows {
			pkt, perr := rawRowToPacket(r)
			if perr != nil {
				onResult(rawDecodeResult{RawID: r.ID, Src: r.Src, Dst: r.Dst, Payload: r.Payload, Err: perr})
				continue
			}
			ev, derr := dispatcher.DecodeV2(ctx, pkt)
			if derr != nil {
				onResult(rawDecodeResult{RawID: r.ID, Src: r.Src, Dst: r.Dst, Payload: r.Payload, Err: derr})
				continue
			}
			onResult(rawDecodeResult{RawID: r.ID, Src: r.Src, Dst: r.Dst, Payload: r.Payload, Events: ev})
		}
		offset += len(rows)
		if len(rows) < batchLimit {
			break
		}
	}
	return nil
}

// DecodeRawPackets 用指定插件对离线会话的 raw_packets 批量解码，
// 结果写入该 session 的 events 表与 state_changes 投影表。
//
// 约束：
//   - 仅允许解码已停止的 session（不在 tasks map 中的）。
//   - 解码在 pipeline 进程内执行（RegistryServer 在此进程）。
//   - 结果写回原 session 的 capture.sqlite（EventWriter 语义）。
func (s *pipelineService) DecodeRawPackets(ctx context.Context, req capturecontrol.DecodeRawPacketsRequest) (capturecontrol.DecodeRawPacketsResult, error) {
	if req.SessionID == "" {
		return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("session_id is required")
	}
	if req.Plugin == "" {
		return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("plugin is required")
	}

	logger := s.logger.With("session_id", req.SessionID, "plugin", req.Plugin, "op", "decode_raw")

	// 1. 拒绝解码正在运行的 session（避免与 captureTask 的写操作冲突）
	if _, ok := s.getTask(req.SessionID); ok {
		return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("session %s is still running, stop it before decoding", req.SessionID)
	}

	// 2. 获取 db_path
	meta, err := s.controlStore.GetSession(ctx, req.SessionID)
	if err != nil {
		return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("get session: %w", err)
	}
	if meta == nil || meta.DBPath == "" {
		return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("session %s not found or has no db_path", req.SessionID)
	}
	dbPath := meta.DBPath

	// 3. 检查插件可用：优先按名精确路由（FindByName），退化按协议 hint（Find）。
	client, ok := s.registry.FindByNameFor(auth.OwnerFrom(ctx), req.Plugin)
	if !ok {
		client, ok = s.registry.FindFor(auth.OwnerFrom(ctx), req.Plugin)
	}
	if !ok {
		return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("plugin %s not found or not a decoder", req.Plugin)
	}
	// 解析实际命中的插件注册名（FindFor 按协议 hint 退化时可能与 req.Plugin 不同），
	// 供语义规则按注册键（owner/name）重载。
	pluginName := req.Plugin
	if n, ok := s.registry.NameByClient(client); ok {
		pluginName = n
	}
	// 语义规则引擎：与实时抓包路径一致执行 name/annotate/pair/extract，
	// 否则离线解码只产出裸事件与状态变更，缺少语义富化。
	sem := newSemanticEngine(logger, s.registry)
	sem.refreshRules(auth.OwnerFrom(ctx), pluginName)

	// 4. 按 dbDriver 打开会话存储（sqlite 走 capture.sqlite；postgres 走共享 PG 库）。
	st, err := s.openSessionStore(req.SessionID, dbPath)
	if err != nil {
		return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// 5. 清空旧解码结果（可选）
	if req.ClearExisting {
		if err := st.ClearDecodedData(ctx); err != nil {
			return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("clear decoded data: %w", err)
		}
		logger.Info("cleared existing decoded data and state_changes")
	}

	// 6. 创建 dispatcher（使用真实 sessionID，并携带服务端端口提示以辅助方向推断）
	// 失败分组采集：插件主动报的"这条我解不了"与链路层失败都进这里。
	// 无论本轮成功还是中途返回都要落库（整体替换语义，写空即代表"本轮没有失败"），
	// 否则上一轮的分组会被误当成这一轮的结果。
	errCol := decode.NewErrorCollector()
	defer func() {
		persistDecodeErrors(context.Background(), st, req.SessionID, errCol, logger)
	}()
	dispatcher, err := decode.NewDispatcher(client, req.SessionID, logger,
		decode.WithServerPort(meta.Port), decode.WithErrorCollector(errCol))
	if err != nil {
		return capturecontrol.DecodeRawPacketsResult{}, fmt.Errorf("new dispatcher: %w", err)
	}
	defer dispatcher.Close()

	// 7. 分批读取 raw packets 并解码，结果写回 events 表与 state_changes 投影表。
	// 失败次数统一取 errCol.Total()（含插件主动报错），不再单独维护计数 —— 两个
	// 计数并存会出现「失败 5 次」配「500 条原因」这种自相矛盾的展示。
	var totalRaw, decoded int64
	var pending []*event.Event
	var enrichedSCs []store.EnrichedStateChange
	var sinceFlush int
	// 重解码刻意用「本次调用独占、ScopeSession」的基线，不复用 pipelineService 的共享实例：
	// 本次会把同一批 raw 包从头重跑一遍，如果起点带着上一轮（或实时抓包那一轮）的终态，
	// 首见赋值会被 noop 抑制、场景中间值会被判成「从终态退回去」，凭空造出反向假变化。
	// 2026-09-18 实测：seed 旧投影方案 526 条 → 230 条且出现 level 4→1；不干预则是
	// 确定性重算，与实时那一轮逐条相同。护栏见 decode_raw_seed_test.go。
	baseline := state.NewBaselineManager(nil)
	flush := func() error {
		if len(pending) == 0 && len(enrichedSCs) == 0 {
			return nil
		}
		// 先落事件：投影行引用的 event_id 必须已经存在，否则就是孤儿行。
		if len(pending) > 0 {
			if err := st.AppendEvents(ctx, pending); err != nil {
				return fmt.Errorf("append events: %w", err)
			}
			pending = nil
		}
		// 投影单独重试：上一轮写失败时事件已经落库、pending 为空，
		// 若把投影写挂在 pending 上，这批投影就再也等不到重试。
		if len(enrichedSCs) > 0 {
			if err := st.WriteEnrichedStateChanges(ctx, req.SessionID, enrichedSCs); err != nil {
				// 保留缓冲下轮重试；旧写法在这里也会清空，等于一次磁盘抖动就永久丢一批投影。
				return fmt.Errorf("write enriched state changes: %w", err)
			}
			enrichedSCs = enrichedSCs[:0]
		}
		sinceFlush = 0
		return nil
	}

	start := time.Now()
	loopErr := forEachRawDecoded(ctx, st, dispatcher, decodeRawOptions{
		Protocol: req.Protocol,
		Src:      req.Src,
		Dst:      req.Dst,
		Limit:    req.Limit,
	}, func(r rawDecodeResult) {
		totalRaw++
		if r.Err != nil {
			// 链路层失败（超时/流断/包无法还原）也要带上下文进分组，否则又是只有一个数字。
			errCol.Add(decode.ErrKindTransport, r.RawID, r.Src, r.Dst, r.Err.Error())
			logger.Debug("decode failed", "id", r.RawID, "src", r.Src, "dst", r.Dst, "error", r.Err)
			return
		}
		if len(r.Events) == 0 {
			return
		}
		for _, ev := range r.Events {
			if ev == nil {
				continue
			}
			// 语义富化：与实时路径一致执行 name/annotate/pair。
			sem.enrichSemantics(ev)
			pending = append(pending, ev)
			scChanges, err := baseline.Apply(ev, req.SessionID)
			if err != nil {
				logger.Warn("state projection", "event_id", ev.Identity.ID, "error", err)
				continue
			}
			enrichedSCs = append(enrichedSCs, scChanges...)
		}
		decoded += int64(len(r.Events))
		sinceFlush++
		if sinceFlush >= rawBatchSize {
			if ferr := flush(); ferr != nil {
				logger.Warn("flush events", "error", ferr)
			}
		}
	})
	if loopErr != nil {
		_ = flush()
		return capturecontrol.DecodeRawPacketsResult{TotalRaw: totalRaw, Decoded: decoded, DecodeErrors: errCol.Total()}, loopErr
	}
	if err := flush(); err != nil {
		return capturecontrol.DecodeRawPacketsResult{TotalRaw: totalRaw, Decoded: decoded, DecodeErrors: errCol.Total()}, err
	}

	logger.Info("decode_raw completed",
		"total_raw", totalRaw, "decoded", decoded, "decode_errors", errCol.Total(),
		"duration_sec", time.Since(start).Seconds())
	return capturecontrol.DecodeRawPacketsResult{
		TotalRaw:     totalRaw,
		Decoded:      decoded,
		DecodeErrors: errCol.Total(),
	}, nil
}

// TestPlugin 用指定插件对离线会话的 raw_packets 解码并采样返回，用于验证插件解码质量。
// 原始包字节仅进程内使用，绝不回传前端；结果不落库（隔离测试，不污染会话真实解码数据）。
func (s *pipelineService) TestPlugin(ctx context.Context, req capturecontrol.TestPluginRequest) (capturecontrol.TestPluginResult, error) {
	if req.SessionID == "" {
		return capturecontrol.TestPluginResult{}, fmt.Errorf("session_id is required")
	}
	if req.Plugin == "" {
		return capturecontrol.TestPluginResult{}, fmt.Errorf("plugin is required")
	}
	sampleLimit := req.SampleLimit
	if sampleLimit <= 0 {
		sampleLimit = testPluginDefaultSample
	}

	logger := s.logger.With("session_id", req.SessionID, "plugin", req.Plugin, "op", "test_plugin")

	// 允许对运行中（running）会话做只读测试：test_plugin 只 SELECT raw_packets 并用独立 dispatcher 采样，
	// 不回写会话库；会话库已开启 WAL，读不阻塞 captureTask 的写、写也不阻塞读，故不存在写冲突。
	// 若会话此刻正在被停止/重建，GetSession/open 会自然报错，由上层提示。
	running := false
	if _, ok := s.getTask(req.SessionID); ok {
		running = true
	}
	logger = logger.With("running", running)

	// 获取 db_path
	meta, err := s.controlStore.GetSession(ctx, req.SessionID)
	if err != nil {
		return capturecontrol.TestPluginResult{}, fmt.Errorf("get session: %w", err)
	}
	if meta == nil || meta.DBPath == "" {
		return capturecontrol.TestPluginResult{}, fmt.Errorf("session %s not found or has no db_path", req.SessionID)
	}
	dbPath := meta.DBPath

	// 检查插件可用：优先按名精确路由（FindByName），退化按协议 hint（Find）。
	client, ok := s.registry.FindByNameFor(auth.OwnerFrom(ctx), req.Plugin)
	if !ok {
		client, ok = s.registry.FindFor(auth.OwnerFrom(ctx), req.Plugin)
	}
	if !ok {
		return capturecontrol.TestPluginResult{}, fmt.Errorf("plugin %s not found or not a decoder", req.Plugin)
	}

	// 按 dbDriver 打开会话存储（只读：与运行中 writer 并发安全，且不回写会话库）
	st, err := s.openSessionStoreReadOnly(req.SessionID, dbPath)
	if err != nil {
		return capturecontrol.TestPluginResult{}, fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// 创建 dispatcher（不写库，仅解码采样）
	errCol := decode.NewErrorCollector()
	dispatcher, err := decode.NewDispatcher(client, req.SessionID, logger,
		decode.WithServerPort(meta.Port), decode.WithErrorCollector(errCol))
	if err != nil {
		return capturecontrol.TestPluginResult{}, fmt.Errorf("new dispatcher: %w", err)
	}
	defer dispatcher.Close()

	res := capturecontrol.TestPluginResult{TypeHistogram: map[string]int64{}}
	var totalRaw, decoded int64
	start := time.Now()
	loopErr := forEachRawDecoded(ctx, st, dispatcher, decodeRawOptions{
		Protocol: req.Protocol,
		Src:      req.Src,
		Dst:      req.Dst,
		Limit:    req.Limit,
	}, func(r rawDecodeResult) {
		totalRaw++
		if r.Err != nil {
			// 只登记不采样：样本统一在末尾按指纹取（见下），否则 sampleLimit 会被
			// 最先出现的那一种错误占满 —— 恰好是用户最需要区分同类错误的时候。
			errCol.Add(decode.ErrKindTransport, r.RawID, r.Src, r.Dst, r.Err.Error())
			return
		}
		for _, ev := range r.Events {
			decoded++
			res.TypeHistogram[string(ev.Identity.Type)]++
			if len(res.SampleEvents) < int(sampleLimit) {
				res.SampleEvents = append(res.SampleEvents, eventToLite(ev))
			}
		}
	})
	res.TotalRaw, res.Decoded = totalRaw, decoded
	res.DecodeErrors = errCol.Total()
	// 错误样本按指纹去重（每组一条，次数多的在前），包含插件主动报错 —— 这类错误
	// 过去连日志都是一行带过，现在能在「解码错误样例」里看到原文。
	res.ErrorSamples = errorSamplesFromGroups(errCol, int(sampleLimit))
	if loopErr != nil {
		return res, loopErr
	}

	logger.Info("test_plugin completed",
		"total_raw", totalRaw, "decoded", decoded, "decode_errors", res.DecodeErrors,
		"error_kinds", errCol.Kinds(),
		"sample_events", len(res.SampleEvents), "duration_sec", time.Since(start).Seconds())
	return res, nil
}

// eventToLite 把解码事件拍平为 TestEventLite（data.* 转为 JSON 预览，截断防爆炸）。
func eventToLite(ev *event.Event) capturecontrol.TestEventLite {
	data := ev.Payload.Value.ToAny()
	js, _ := json.Marshal(data)
	if len(js) > testPluginMaxDataJSON {
		js = js[:testPluginMaxDataJSON]
	}
	return capturecontrol.TestEventLite{
		ID:            string(ev.Identity.ID),
		TimestampUnix: ev.Identity.Timestamp.Unix(),
		Type:          string(ev.Identity.Type),
		DataJSON:      string(js),
	}
}

// rawRowToPacket 把 RawPacketRow 转换为 event.Packet 供 Dispatcher.Decode 使用。
// Src/Dst 在 raw_packets 表中以 "ip:port" 字符串存储，需解析回 netip.AddrPort。
func rawRowToPacket(r store.RawPacketRow) (event.Packet, error) {
	src, err := netip.ParseAddrPort(r.Src)
	if err != nil {
		return event.Packet{}, fmt.Errorf("parse src %q: %w", r.Src, err)
	}
	dst, err := netip.ParseAddrPort(r.Dst)
	if err != nil {
		return event.Packet{}, fmt.Errorf("parse dst %q: %w", r.Dst, err)
	}
	protocol := r.Protocol
	if protocol == "" {
		protocol = "tcp"
	}
	pkt := event.Packet{
		ID:        r.ID,
		Timestamp: r.Timestamp,
		Raw:       r.Payload,
		LinkType:  event.LinkType(r.LinkType),
		Src:       src,
		Dst:       dst,
		Protocol:  protocol,
		Metadata:  map[string]any{},
	}
	// 优先复用落库时的 conn_id：实时抓包按连接生命周期派生（同一五元组重连会
	// 拿新 ID），这里没有 TCP 控制位无法重建代次，重新派生会得到不同的标识，
	// 使离线补解码的事件与 raw_packets 里的原始帧连不上。
	// 历史库没有 conn_id 时退回按规范五元组派生。
	if r.ConnID != "" {
		pkt.Metadata["conn_id"] = r.ConnID
	} else {
		deriveConnID(&pkt)
	}
	return pkt, nil
}
