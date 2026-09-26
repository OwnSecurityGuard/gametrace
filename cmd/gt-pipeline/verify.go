package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"net/netip"
	"time"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	sdkcontract "github.com/OwnSecurityGuard/gametrace/sdk/contract"
	sdkevent "github.com/OwnSecurityGuard/gametrace/sdk/event"

	"gametrace/pkg/auth"
	"gametrace/pkg/decode"
	"gametrace/pkg/event"
	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/plugin"
	"gametrace/pkg/plugin/quality"
	"gametrace/pkg/plugindev"
	"gametrace/pkg/store"
)

// sampleBytesHardCapPackets / sampleBytesHardCapBytes 是 plugin.sample_bytes 的
// 硬上限（设计 §6），不可通过参数突破。取证只给有限的事实窗口。
const (
	sampleBytesHardCapPackets = 20
	sampleBytesHardCapBytes   = 64
)

// Verify 用指定插件对离线会话的 raw_packets 解码并做契约+质量校验。
//
// 它是 Runtime Plane 的 verify 执行（设计 §1.3 / §7）：拥有真实流量与 registry，
// 调度解码、产出语料，再交给 quality.Verify 合并 SDK 违规与 gametrace 统计得 verdict。
// 完成后把 validated 证明写入控制库的 plugin_validations 表（按 owner+name），
// 供 gt-mcp 的 status_plugin 读取。
func (s *pipelineService) Verify(ctx context.Context, req capturecontrol.VerifyRequest) (capturecontrol.VerifyResult, error) {
	if req.SessionID == "" {
		return capturecontrol.VerifyResult{}, fmt.Errorf("session_id is required")
	}
	if req.Plugin == "" {
		return capturecontrol.VerifyResult{}, fmt.Errorf("plugin is required")
	}
	logger := s.logger.With("session_id", req.SessionID, "plugin", req.Plugin, "op", "verify")

	meta, err := s.controlStore.GetSession(ctx, req.SessionID)
	if err != nil {
		return capturecontrol.VerifyResult{}, fmt.Errorf("get session: %w", err)
	}
	if meta == nil || meta.DBPath == "" {
		return capturecontrol.VerifyResult{}, fmt.Errorf("session %s not found or has no db_path", req.SessionID)
	}

	client, ok := s.registry.FindByNameFor(auth.OwnerFrom(ctx), req.Plugin)
	if !ok {
		client, ok = s.registry.FindFor(auth.OwnerFrom(ctx), req.Plugin)
	}
	if !ok {
		return capturecontrol.VerifyResult{}, fmt.Errorf("plugin %s not found or not a decoder", req.Plugin)
	}

	st, err := s.openSessionStoreReadOnly(req.SessionID, meta.DBPath)
	if err != nil {
		return capturecontrol.VerifyResult{}, fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	dispatcher, err := decode.NewDispatcher(client, req.SessionID, logger, decode.WithServerPort(meta.Port))
	if err != nil {
		return capturecontrol.VerifyResult{}, fmt.Errorf("new dispatcher: %w", err)
	}
	defer dispatcher.Close()

	// 语义契约阶段（Semantic Contract v1）：声明期 Check + 运行期逐事件 CheckEvent。
	// manifest 拿不到时降级为纯传输层校验（不阻断 verify），只记 warn。
	var manifest *sdk.Manifest
	if raw, merr := s.registry.GetPluginManifestFor(auth.OwnerFrom(ctx), req.Plugin); merr == nil && len(raw) > 0 {
		if m, perr := plugin.ParseManifest(raw); perr == nil {
			manifest = m
		} else {
			logger.Warn("verify: parse manifest snapshot failed, semantic checks skipped", "error", perr)
		}
	} else {
		logger.Warn("verify: manifest unavailable, semantic checks skipped", "error", merr)
	}
	sem := newSemCollector()

	var corpus []quality.DecodeIO
	var totalRaw, matchedRaw int64
	loopErr := forEachRawDecoded(ctx, st, dispatcher, decodeRawOptions{
		Protocol: req.Protocol,
		Src:      req.Src,
		Dst:      req.Dst,
		Limit:    req.Limit,
	}, func(r rawDecodeResult) {
		totalRaw++
		// candidate = 命中会话 target port 的包，即插件真正该解的流量。非 candidate
		// 的包只进 input.raw，绝不参与违规与质量统计。
		candidate := rawPortMatches(r.Src, r.Dst, int32(meta.Port))
		if candidate {
			matchedRaw++
		}
		corpus = append(corpus, decodeIOsFromResult(r, candidate)...)
		if manifest != nil && candidate {
			for _, ev := range r.Events {
				sem.checkEvent(manifest, ev)
			}
		}
	})
	if loopErr != nil {
		return capturecontrol.VerifyResult{}, loopErr
	}

	// 会话适用性（P1-1 阶段 1：target port + matching count）：过滤窗口里一个
	// 命中会话 target port 的包都没有，说明这个会话根本没带插件要解的流量 ——
	// 属于「换会话重试」而非「插件质量差」，verdict 报 not_applicable。
	app := sessionApplicability(int32(meta.Port), totalRaw, matchedRaw)

	// 声明期两层校验（runtime/schema/state）只在会话适用时并入：不适用的会话没有
	// 任何插件产出的证据，报语义违规只会误导用户去改插件。
	if manifest != nil && app.Applicable {
		sem.addReport(sdkcontract.NewPluginChecker().Check(manifest))
	}

	result := quality.Verify(corpus)
	result.Violations = append(result.Violations, sem.violations()...)
	quality.RecomputeVerdict(result)

	if !app.Applicable {
		// 不适用：没有插件该解的流量。quality 置 nil（对非本协议流量算出的统计
		// 会被读成「插件质量差」），两轴 not_run，verdict not_applicable。
		result.Verdict = plugindev.VerdictNotApplicable
		result.Quality = nil
		result.Checks = &plugindev.VerifyChecks{Decode: plugindev.NotRun, Semantic: plugindev.NotRun}
	}

	// 跨平面回写：validated 证明必须跨进程可见（Runtime Plane verify 写、
	// gt-mcp status_plugin 读）。平台不持有用户插件目录，所以这条证据存在控制库
	// 的 plugin_validations 表，按 (owner, name) 唯一，不再落盘。
	// 仅 verdict == "pass" 才立证；warn/fail/not_applicable 显式清除旧证明
	// （防旧 pass 残留）。
	runID := fmt.Sprintf("verify_%d", time.Now().UnixNano())
	owner := auth.OwnerFrom(ctx)
	if result.Verdict == plugindev.VerdictPass {
		if perr := s.controlStore.UpsertPluginValidation(ctx, store.PluginValidation{
			Owner:       owner,
			Name:        req.Plugin,
			VerifyRunID: runID,
			SessionID:   req.SessionID,
			Verdict:     result.Verdict,
			At:          time.Now(),
		}); perr != nil {
			logger.Warn("verify: persist validation proof failed", "error", perr)
		}
	} else if cerr := s.controlStore.ClearPluginValidation(ctx, owner, req.Plugin); cerr != nil {
		logger.Warn("verify: clear validation proof failed", "error", cerr)
	}
	plugindev.RecordVerify(req.Plugin, result)

	out := capturecontrol.VerifyResult{
		Verdict:       result.Verdict,
		VerifyRunID:   runID,
		SessionID:     req.SessionID,
		AtUnix:        time.Now().Unix(),
		Applicability: app,
	}
	if result.Checks != nil {
		out.Checks = capturecontrol.VerifyChecks{Decode: result.Checks.Decode, Semantic: result.Checks.Semantic}
	}
	for _, v := range result.Violations {
		out.Violations = append(out.Violations, capturecontrol.ViolationView{
			RuleID:    v.RuleID,
			Topic:     v.Topic,
			Severity:  v.Severity,
			Statement: v.Statement,
			DocRef:    v.DocRef,
			Count:     v.Count,
			Sample:    v.Sample,
			Layer:     v.Layer,
		})
	}
	if q := result.Quality; q != nil {
		out.Quality = &capturecontrol.QualityView{
			InputRaw:           q.InputRaw,
			InputCandidate:     q.InputCandidate,
			DecodeSuccess:      q.DecodeSuccess,
			DecodeUnknown:      q.DecodeUnknown,
			DecodeUnknownRatio: q.DecodeUnknownRatio,
			CorrelatedInputs:   q.CorrelatedInputs,
			LongPacketErrors:   q.LongPacketErrors,
			EntropyEstimate:    q.EntropyEstimate,
			DecodeErrors:       q.DecodeErrors,
		}
	}
	logger.Info("verify completed", "verdict", out.Verdict, "violations", len(out.Violations), "corpus", len(corpus))
	return out, nil
}

// rawPortMatches 判断一个原始包是否命中会话的 target port（meta.Port）：
// Src/Dst 任一端口等于它即命中（双向流量都算）。port<=0（无端口信息）时视为
// 全部命中，保持旧行为不误伤。
func rawPortMatches(src, dst string, port int32) bool {
	if port <= 0 {
		return true
	}
	for _, s := range []string{src, dst} {
		if ap, err := netip.ParseAddrPort(s); err == nil && int32(ap.Port()) == port {
			return true
		}
	}
	return false
}

// sessionApplicability 汇总 verify/test_plugin 的会话适用性判定（P1-1 阶段 1）。
// 只做 target port + matching count：窗口里一个命中包都没有就判定不适用，
// verdict 报 not_applicable（换会话），而不是质量 fail（修插件）。
// raw packet distribution / protocol 分布等更细的信号留待阶段 2。
func sessionApplicability(targetPort int32, totalPackets, matchedPackets int64) *capturecontrol.VerifyApplicability {
	app := &capturecontrol.VerifyApplicability{
		Result:         "match",
		Applicable:     true,
		Reason:         "ok",
		TargetPort:     targetPort,
		TotalPackets:   totalPackets,
		MatchedPackets: matchedPackets,
	}
	switch {
	case totalPackets == 0:
		app.Applicable = false
		app.Reason = "no_raw_packets"
	case targetPort > 0 && matchedPackets == 0:
		app.Applicable = false
		app.Reason = "no_matching_packets"
	}
	if !app.Applicable {
		app.Result = "not_match"
	}
	return app
}

// SampleBytes 读取会话原始包的前若干字节（事实：hexdump / 长度直方图 / 首字节分布 /
// 熵），不做任何解释，并在 plugin_debug_access 留审计（设计 §6）。
//
// 硬上限（20 包 / 64 字节）不可通过参数突破；审计记真实返回量，截断后数据不假。
func (s *pipelineService) SampleBytes(ctx context.Context, req capturecontrol.SampleBytesRequest) (capturecontrol.SampleBytesResult, error) {
	if req.SessionID == "" {
		return capturecontrol.SampleBytesResult{}, fmt.Errorf("session_id is required")
	}
	logger := s.logger.With("session_id", req.SessionID, "op", "sample_bytes")

	meta, err := s.controlStore.GetSession(ctx, req.SessionID)
	if err != nil {
		return capturecontrol.SampleBytesResult{}, fmt.Errorf("get session: %w", err)
	}
	if meta == nil || meta.DBPath == "" {
		return capturecontrol.SampleBytesResult{}, fmt.Errorf("session %s not found or has no db_path", req.SessionID)
	}

	limit := req.Limit
	if limit <= 0 || limit > sampleBytesHardCapPackets {
		limit = sampleBytesHardCapPackets
	}
	maxBytes := int(req.MaxBytes)
	if maxBytes <= 0 || maxBytes > sampleBytesHardCapBytes {
		maxBytes = sampleBytesHardCapBytes
	}

	// 只读打开（仅读 raw_packets）。
	st, err := s.openSessionStoreReadOnly(req.SessionID, meta.DBPath)
	if err != nil {
		return capturecontrol.SampleBytesResult{}, fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// 多取一条以判断是否被硬上限截断。
	rows, err := st.QueryRawPackets(ctx, store.RawPacketQuery{Limit: int(limit) + 1, Offset: 0})
	if err != nil {
		return capturecontrol.SampleBytesResult{}, fmt.Errorf("query raw packets: %w", err)
	}
	truncated := int64(len(rows)) > limit
	if truncated {
		rows = rows[:limit]
	}

	out := capturecontrol.SampleBytesResult{
		SessionID:        req.SessionID,
		RequestedPackets: limit,
		ReturnedPackets:  int64(len(rows)),
		LengthHistogram:  map[int32]int64{},
		FirstByteDist:    map[int32]int64{},
	}
	var entropySum float64
	for _, r := range rows {
		payload := r.Payload
		if len(payload) > maxBytes {
			payload = payload[:maxBytes]
		}
		out.ReturnedBytes += int64(len(payload))
		ent := shannonBits(payload)
		entropySum += ent
		var firstByte int32
		if len(r.Payload) > 0 {
			firstByte = int32(r.Payload[0])
		}
		out.Packets = append(out.Packets, capturecontrol.SampledPacket{
			RawPacketID: r.ID,
			Src:         r.Src,
			Dst:         r.Dst,
			Length:      int64(len(r.Payload)),
			Hex:         hex.EncodeToString(payload),
			Entropy:     ent,
			FirstByte:   firstByte,
		})
		bucket := int32((len(r.Payload) / 16) * 16)
		out.LengthHistogram[bucket]++
		if len(r.Payload) > 0 {
			out.FirstByteDist[firstByte]++
		}
	}
	if n := len(rows); n > 0 {
		out.MeanEntropy = entropySum / float64(n)
	}

	// 审计：写入方唯一为 Runtime Plane（pipeline），记真实返回量。
	auditID, aerr := s.controlStore.RecordDebugAccess(ctx, store.DebugAccess{
		Actor:            "pipeline",
		Tool:             "sample_bytes",
		Plugin:           req.Plugin,
		SessionID:        req.SessionID,
		RequestedPackets: limit,
		ReturnedPackets:  out.ReturnedPackets,
		ReturnedBytes:    out.ReturnedBytes,
		Truncated:        truncated,
	})
	if aerr != nil {
		logger.Warn("record debug access", "error", aerr)
	}
	out.AuditID = auditID
	out.Truncated = truncated

	logger.Info("sample_bytes completed",
		"returned_packets", out.ReturnedPackets, "returned_bytes", out.ReturnedBytes,
		"truncated", truncated, "audit_id", auditID)
	return out, nil
}

// decodeIOsFromResult 把一个原始包的解码结果展开为 quality.DecodeIO 序列：
// 每个产出的事件对应一个非终结响应（带 event_type/schema_id/payload），
// 再追加一个终结响应（done=true，承载原始字节供熵估计）。解码失败则仅一个
// done=true 且带 DecodeError 的响应。
// candidate 透传到每个 DecodeIO：只有命中会话 target port 的包才计入质量统计。
func decodeIOsFromResult(r rawDecodeResult, candidate bool) []quality.DecodeIO {
	if r.Err != nil {
		return []quality.DecodeIO{{
			InputID:     r.RawID,
			Candidate:   candidate,
			Done:        true,
			DecodeError: r.Err.Error(),
			Payload:     r.Payload,
		}}
	}
	var ios []quality.DecodeIO
	for _, ev := range r.Events {
		ios = append(ios, quality.DecodeIO{
			InputID:    r.RawID,
			Candidate:  candidate,
			Done:       false,
			EventType:  string(ev.Identity.Type),
			PayloadLen: 1, // 事件存在即代表响应带非空 payload
			Correlated: ev.Trace.CorrelationID != "",
		})
	}
	// 终结响应：承载原始字节供熵估计（不重复计入未知率）。
	ios = append(ios, quality.DecodeIO{InputID: r.RawID, Candidate: candidate, Done: true, Payload: r.Payload})
	return ios
}

// shannonBits 返回 b 的香农熵（bits/byte，0..8）。
func shannonBits(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var freq [256]float64
	for _, c := range b {
		freq[c]++
	}
	var h float64
	n := float64(len(b))
	for _, f := range freq {
		if f == 0 {
			continue
		}
		p := f / n
		h -= p * math.Log2(p)
	}
	return h
}

// semCollector 聚合语义契约（Semantic Contract v1）校验违规，
// 按 RuleID 去重计数，形态对齐 quality.Verify 的传输层违规输出。
type semCollector struct {
	viol  map[string]*plugindev.Violation
	order []string
}

func newSemCollector() *semCollector {
	return &semCollector{viol: map[string]*plugindev.Violation{}}
}

func (c *semCollector) add(v sdkcontract.Violation) {
	e, ok := c.viol[v.RuleID]
	if !ok {
		e = &plugindev.Violation{RuleID: v.RuleID, Layer: plugindev.LayerSemantic}
		c.viol[v.RuleID] = e
		c.order = append(c.order, v.RuleID)
	}
	e.Count++
	if e.Sample == "" {
		e.Sample = v.Message
	}
	if spec, ok := v.Spec(); ok {
		e.Topic = spec.Topic
		e.Severity = string(spec.Severity)
		e.Statement = spec.Statement
		e.DocRef = spec.DocRef
	}
	if e.Severity == "" {
		e.Severity = string(v.Severity)
	}
}

// addReport 并入一份 SDK Report（可空）。
func (c *semCollector) addReport(r *sdkcontract.Report) {
	if r == nil {
		return
	}
	for _, v := range r.Violations {
		c.add(v)
	}
}

// checkEvent 把单条解码事件转为 SDK Draft 并跑 event/schema/state 层校验。
// gametrace event.Value 与 SDK event.Value 的 MsgPack wire 格式一致，经序列化互转即可。
func (c *semCollector) checkEvent(m *sdk.Manifest, ev *event.Event) {
	if ev == nil {
		return
	}
	b, err := ev.Payload.Value.MarshalMsgpack()
	if err != nil {
		return
	}
	v, err := sdkevent.UnmarshalValueMsgpack(b)
	if err != nil {
		return
	}
	d := &sdkevent.Draft{
		Type:  sdkevent.EventType(ev.Identity.Type),
		Value: v,
	}
	c.addReport(sdkcontract.NewPluginChecker().CheckEvent(m, d))
}

func (c *semCollector) violations() []*plugindev.Violation {
	if len(c.order) == 0 {
		return nil
	}
	out := make([]*plugindev.Violation, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.viol[id])
	}
	return out
}
