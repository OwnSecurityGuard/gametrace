package main

// get_protocol_catalog：协议级索引 / 聚合视图。
//
// 与 list_decoded_data 的职责边界：
//   - list_decoded_data       事件级详情查询（完整 payload / trace / analysis）
//   - get_protocol_catalog    协议级聚合（session / 时间范围 / 方向 → 协议概览）
//
// 实现原则（见 docs/get_protocol_catalog 设计规格 v1）：
//   - 复用现有 decoded event 查询能力（store.EventPager.StreamEvents 正序流式，
//     时间窗口在 SQL 层下推），不重新发明一套事件筛选器；
//   - 不把整个 session payload 加载进内存：单次正序流式遍历，聚合状态紧凑
//     （每协议一条 + 每个 event 一条 causation 回溯索引，payload 解码后即释放）；
//   - 不自行推断 request/response：paired_responses 复用事件自带 causation_id
//     （响应事件指向触发它的请求），无 pair 事实则返回空数组；
//   - 不加入任何 AI 判断字段（importance / category / 压测建议等）。

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/event"
	"gametrace/pkg/store"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	protocolCatalogDefaultLimit = 100
	protocolCatalogMaxLimit     = 500
)

// catalogDirectionValues 是 direction 参数允许的取值（第一版唯一协议过滤条件）。
var catalogDirectionValues = map[string]bool{
	"client_to_server": true,
	"server_to_client": true,
}

// catalogReqRef 是 causation 配对回溯用的请求侧事件信息（紧凑，非 payload）。
type catalogReqRef struct {
	dir     string
	msgName string
	tsNano  int64
}

// catalogResponseRef 记录一个带 causation 的命名响应事件，聚合结束后取回其请求。
type catalogResponseRef struct {
	causID string
	respTs int64
	resp   string // direction|msg_name，仅响应有名称时记录
}

// catalogSample 是协议聚合的单事件采样（时间正序追加），同时用于
// interval 统计与 first/middle/last 采样，避免为同一协议维护两份数组。
type catalogSample struct {
	tsNano int64
	id     string
}

// catalogPairAccum 是 paired_responses 单条（响应 msg_name）的聚合态。
type catalogPairAccum struct {
	count   int
	reqSeen map[string]struct{} // 去重后的 request event id（paired_count 来源）
	latency []int64             // 毫秒；仅非负有效差
}

// protoAgg 是 direction|msg_name 协议的聚合态。
type protoAgg struct {
	msgName   string
	direction string
	count     int
	firstNano int64
	lastNano  int64
	semantic  map[string]struct{}
	pushTrue  bool
	pushFalse bool
	fields    map[string]int // path -> observed_count（事件数，非元素数）
	samples   []catalogSample
	pairs     map[string]*catalogPairAccum
}

// catalogAggregator 在单次正序流式遍历中累计协议级聚合。
type catalogAggregator struct {
	sessionID     string
	direction     string
	eventCount    int
	c2s           int
	s2c           int
	unnamedEvents int
	minNano       int64
	maxNano       int64
	aggs          map[string]*protoAgg
	// reqByID 保留全部事件的最小折射（dir/msg_name/ts），供 pairing 按
	// causation_id 回溯请求。正序流中请求先于响应出现，聚合结束时已完备。
	reqByID map[string]catalogReqRef
	// responseRefs 记录命名事件携带的 causation 引用；内存与带 cause 的事件数正比。
	responseRefs []catalogResponseRef
}

func newCatalogAggregator(sessionID, direction string) *catalogAggregator {
	return &catalogAggregator{
		sessionID: sessionID,
		direction: direction,
		aggs:      map[string]*protoAgg{},
		reqByID:   map[string]catalogReqRef{},
	}
}

// add 累计单个事件。direction 过滤在入口完成，被过滤事件不进入任何统计。
func (a *catalogAggregator) add(ev *event.Event) {
	if ev == nil {
		return
	}
	dir := eventMetaDirection(ev)
	msgName := eventMetaStringValue(ev, "msg_name")
	if a.direction != "" && dir != a.direction {
		return
	}
	tsNano := ev.Identity.Timestamp.UnixNano()
	evID := string(ev.Identity.ID)
	a.eventCount++
	if a.eventCount == 1 || tsNano < a.minNano {
		a.minNano = tsNano
	}
	if tsNano > a.maxNano {
		a.maxNano = tsNano
	}
	switch dir {
	case "client_to_server":
		a.c2s++
	case "server_to_client":
		a.s2c++
	}
	a.reqByID[evID] = catalogReqRef{dir: dir, msgName: msgName, tsNano: tsNano}
	if msgName == "" {
		// 无 msg_name 的 event 不生成协议项，只在 summary.unnamed_events 统计。
		a.unnamedEvents++
		return
	}
	if caus := string(ev.Trace.CausationID); caus != "" {
		a.responseRefs = append(a.responseRefs, catalogResponseRef{
			causID: caus,
			respTs: tsNano,
			resp:   dir + "|" + msgName,
		})
	}
	agg := a.aggs[dir+"|"+msgName]
	if agg == nil {
		agg = &protoAgg{
			msgName:   msgName,
			direction: dir,
			semantic:  map[string]struct{}{},
			fields:    map[string]int{},
			pairs:     map[string]*catalogPairAccum{},
		}
		a.aggs[dir+"|"+msgName] = agg
	}
	agg.count++
	if agg.count == 1 || tsNano < agg.firstNano {
		agg.firstNano = tsNano
	}
	if tsNano > agg.lastNano {
		agg.lastNano = tsNano
	}
	for _, s := range eventMetaStringList(ev, "semantic") {
		agg.semantic[s] = struct{}{}
	}
	if pv, ok := ev.MetaValue("is_push"); ok {
		if b, ok := pv.AsBool(); ok {
			if b {
				agg.pushTrue = true
			} else {
				agg.pushFalse = true
			}
		}
	}
	// 字段路径来源是业务 payload（data），不读取 meta/analysis/capture。
	// 每个 event 一次小 map 分配；换来的是一遍流式完成全量字段结构收集。
	for path := range collectEventFieldPaths(a.bizValue(ev)) {
		agg.fields[path]++
	}
	agg.samples = append(agg.samples, catalogSample{tsNano: tsNano, id: evID})
}

func (a *catalogAggregator) bizValue(ev *event.Event) event.Value {
	biz, _, _ := event.SplitReservedKeys(ev.Payload.Value)
	return biz
}

// build 在流式遍历结束后生成完整 catalog（协议统计基于全量范围，再做协议级分页）。
func (a *catalogAggregator) build(limit, offset int) map[string]any {
	a.resolvePairs()

	keys := make([]string, 0, len(a.aggs))
	for k := range a.aggs {
		keys = append(keys, k)
	}
	// 第一版固定排序：first_seen ASC → direction ASC → msg_name ASC。
	sort.Slice(keys, func(i, j int) bool {
		ai, aj := a.aggs[keys[i]], a.aggs[keys[j]]
		if ai.firstNano != aj.firstNano {
			return ai.firstNano < aj.firstNano
		}
		if ai.direction != aj.direction {
			return ai.direction < aj.direction
		}
		return ai.msgName < aj.msgName
	})

	total := len(keys)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	protocols := make([]map[string]any, 0, end-start)
	for _, k := range keys[start:end] {
		protocols = append(protocols, catalogProtocolMap(a.aggs[k]))
	}

	var observedStart, observedEnd any
	if a.eventCount > 0 {
		observedStart = time.Unix(0, a.minNano).Format(time.RFC3339Nano)
		observedEnd = time.Unix(0, a.maxNano).Format(time.RFC3339Nano)
	}
	return map[string]any{
		"session_id": a.sessionID,
		"observed_time_range": map[string]any{
			"start": observedStart,
			"end":   observedEnd,
		},
		"summary": map[string]any{
			"event_count":             a.eventCount,
			"client_to_server_events": a.c2s,
			"server_to_client_events": a.s2c,
			"unnamed_events":          a.unnamedEvents,
		},
		"total_protocols": total,
		"count":           len(protocols),
		"offset":          offset,
		"limit":           limit,
		"has_more":        offset+len(protocols) < total,
		"protocols":       protocols,
	}
}

// resolvePairs 用现有 causation 关系生成 paired_responses：
// response.causation_id 指向的请求若属于某个命名协议，则为该协议记一条配对。
// 不做任何相邻/同 correlation/时间接近的自行推断。
func (a *catalogAggregator) resolvePairs() {
	for _, ref := range a.responseRefs {
		req, ok := a.reqByID[ref.causID]
		if !ok || req.msgName == "" {
			continue
		}
		agg := a.aggs[req.dir+"|"+req.msgName]
		if agg == nil {
			continue
		}
		p := agg.pairs[ref.resp]
		if p == nil {
			p = &catalogPairAccum{reqSeen: map[string]struct{}{}}
			agg.pairs[ref.resp] = p
		}
		p.count++
		p.reqSeen[ref.causID] = struct{}{}
		if lat := ref.respTs - req.tsNano; lat >= 0 {
			p.latency = append(p.latency, lat/1e6)
		}
	}
}

func catalogProtocolMap(agg *protoAgg) map[string]any {
	// fields 按 path 稳定排序。
	paths := make([]string, 0, len(agg.fields))
	for p := range agg.fields {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	fieldList := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		fieldList = append(fieldList, map[string]any{"path": p, "observed_count": agg.fields[p]})
	}

	// semantic 唯一值集合，按名称稳定排序。
	semList := make([]string, 0, len(agg.semantic))
	for s := range agg.semantic {
		semList = append(semList, s)
	}
	sort.Strings(semList)

	// is_push 三态：全 true → true；全 false → false；混合 → null。
	var isPush any
	switch {
	case agg.pushTrue && agg.pushFalse:
		isPush = nil
	case agg.pushTrue:
		isPush = true
	default:
		isPush = false
	}

	pairKeys := make([]string, 0, len(agg.pairs))
	for mn := range agg.pairs {
		pairKeys = append(pairKeys, mn)
	}
	sort.Strings(pairKeys)
	pairedResponses := make([]map[string]any, 0, len(pairKeys))
	for _, mn := range pairKeys {
		p := agg.pairs[mn]
		pairedResponses = append(pairedResponses, map[string]any{
			"msg_name":     mn,
			"count":        p.count,
			"paired_count": len(p.reqSeen),
			"latency_ms":   catalogStatsMap(p.latency),
		})
	}

	return map[string]any{
		"key":              agg.direction + "|" + agg.msgName,
		"msg_name":         agg.msgName,
		"direction":        agg.direction,
		"semantic":         semList,
		"is_push":          isPush,
		"count":            agg.count,
		"first_seen":       time.Unix(0, agg.firstNano).Format(time.RFC3339Nano),
		"last_seen":        time.Unix(0, agg.lastNano).Format(time.RFC3339Nano),
		"interval_ms":      catalogIntervalStats(agg.samples),
		"fields":           fieldList,
		"sample_event_ids": catalogSampleIDs(agg.samples),
		"paired_responses": pairedResponses,
	}
}

// catalogIntervalStats 计算协议相邻事件时间间隔统计（毫秒）。
// samples 按时间正序追加，相邻差即 delta；<2 个 event 时 count=0 全 null。
func catalogIntervalStats(samples []catalogSample) map[string]any {
	if len(samples) < 2 {
		return map[string]any{"count": 0, "min": nil, "p50": nil, "p95": nil, "max": nil}
	}
	deltas := make([]int64, 0, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		if d := samples[i].tsNano - samples[i-1].tsNano; d >= 0 {
			deltas = append(deltas, d/1e6)
		}
	}
	return catalogStatsMap(deltas)
}

// catalogStatsMap 计算 count/min/p50/p95/max（毫秒，整数）。空输入全 null。
func catalogStatsMap(values []int64) map[string]any {
	if len(values) == 0 {
		return map[string]any{"count": 0, "min": nil, "p50": nil, "p95": nil, "max": nil}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	min := sorted[0]
	max := sorted[len(sorted)-1]
	return map[string]any{
		"count": len(sorted),
		"min":   int64Ptr(min),
		"p50":   int64Ptr(catalogPercentile(sorted, 0.50)),
		"p95":   int64Ptr(catalogPercentile(sorted, 0.95)),
		"max":   int64Ptr(max),
	}
}

// catalogPercentile 从升序数组取最近秩百分位（nearest-rank）。
func catalogPercentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// catalogSampleIDs 返回 first / middle / last 最多 3 个事件 ID。
func catalogSampleIDs(samples []catalogSample) []string {
	n := len(samples)
	switch {
	case n == 0:
		return []string{}
	case n == 1:
		return []string{samples[0].id}
	case n == 2:
		return []string{samples[0].id, samples[1].id}
	default:
		return []string{samples[0].id, samples[n/2].id, samples[n-1].id}
	}
}

func int64Ptr(v int64) *int64 { return &v }

// eventMetaDirection 解析事件方向：优先 meta.direction，缺失时用
// host 推断的 Context.Direction 兜底（与 decodedEventMap 行为一致）。
func eventMetaDirection(ev *event.Event) string {
	if v, ok := ev.MetaValue("direction"); ok {
		if s, ok := v.AsString(); ok && s != "" {
			return s
		}
	}
	return ev.Context.Direction
}

// eventMetaStringValue 返回 meta 中指定键的字符串值。
func eventMetaStringValue(ev *event.Event, key string) string {
	if v, ok := ev.MetaValue(key); ok {
		if s, ok := v.AsString(); ok {
			return s
		}
	}
	return ""
}

// eventMetaStringList 返回 meta 中指定键的字符串数组（semantic 等标签）。
func eventMetaStringList(ev *event.Event, key string) []string {
	v, ok := ev.MetaValue(key)
	if !ok {
		return nil
	}
	arr, ok := v.AsArray()
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.AsString(); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// collectEventFieldPaths 递归遍历对象/数组，输出字段结构路径集合：
//   - 数组统一折叠为 []（items[].id），不暴露实际下标；
//   - 同一 event 内同一路径只记一次（observed_count 是事件数，非元素数）；
//   - 空数组保留容器路径（items[]），非空数组只保留叶子路径（items[].id）。
func collectEventFieldPaths(v event.Value) map[string]struct{} {
	paths := map[string]struct{}{}
	var walk func(v event.Value, prefix string)
	walk = func(v event.Value, prefix string) {
		switch v.Kind {
		case event.Object:
			for k, child := range v.Object {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				if child.Kind == event.Object || child.Kind == event.Array {
					walk(child, p)
				} else {
					paths[p] = struct{}{}
				}
			}
		case event.Array:
			childPath := prefix + "[]"
			if len(v.Array) == 0 {
				paths[childPath] = struct{}{}
				return
			}
			for _, item := range v.Array {
				walk(item, childPath)
			}
		default:
			if prefix != "" {
				paths[prefix] = struct{}{}
			}
		}
	}
	walk(v, "")
	return paths
}

// handleGetProtocolCatalog 实现 get_protocol_catalog MCP 工具。
func (m *mcpCapture) handleGetProtocolCatalog(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := req.GetInt("limit", protocolCatalogDefaultLimit)
	offset := req.GetInt("offset", 0)
	sessionID := req.GetString("session_id", "")
	startRaw := req.GetString("start_time", "")
	endRaw := req.GetString("end_time", "")
	direction := req.GetString("direction", "")

	if limit < 1 || limit > protocolCatalogMaxLimit {
		return errorResult(fmt.Errorf("limit out of range: must be 1..%d", protocolCatalogMaxLimit)), nil
	}
	if offset < 0 {
		return errorResult(fmt.Errorf("offset < 0")), nil
	}
	if direction != "" && !catalogDirectionValues[direction] {
		return errorResult(fmt.Errorf("direction invalid: allowed client_to_server|server_to_client")), nil
	}
	var startTime, endTime time.Time
	if startRaw != "" {
		t, err := time.Parse(time.RFC3339, startRaw)
		if err != nil {
			return errorResult(fmt.Errorf("invalid start_time: %w", err)), nil
		}
		startTime = t
	}
	if endRaw != "" {
		t, err := time.Parse(time.RFC3339, endRaw)
		if err != nil {
			return errorResult(fmt.Errorf("invalid end_time: %w", err)), nil
		}
		endTime = t
	}
	if !startTime.IsZero() && !endTime.IsZero() && startTime.After(endTime) {
		return errorResult(fmt.Errorf("start_time > end_time")), nil
	}

	// session 默认行为与 list_decoded_data 保持一致：为空时解析为当前抓包 session。
	if sessionID == "" {
		owner := auth.OwnerFrom(ctx)
		if current, cErr := m.sessionMgr.readCurrent(owner); cErr == nil && current != nil && current.SessionID != "" {
			sessionID = current.SessionID
		}
	}
	dbPath, err := m.getDBPath(ctx, sessionID)
	if err != nil {
		return errorResult(err), nil
	}
	if dbPath == "" {
		return errorResult(fmt.Errorf("session not found: %s", sessionID)), nil
	}

	slog.Info("get_protocol_catalog requested",
		"session_id", sessionID, "start_time", startRaw, "end_time", endRaw,
		"direction", direction, "limit", limit, "offset", offset)

	reader, err := m.openReader(ctx, sessionID)
	if err != nil {
		return errorResult(err), nil
	}
	defer reader.Close()

	pager, ok := reader.(store.EventPager)
	if !ok {
		return errorResult(fmt.Errorf("event reader does not support paging")), nil
	}

	agg := newCatalogAggregator(sessionID, direction)
	pageQ := store.EventPageQuery{SessionID: sessionID, From: startTime, To: endTime}
	if err := pager.StreamEvents(ctx, pageQ, 500, func(batch []*event.Event) (bool, error) {
		for _, ev := range batch {
			agg.add(ev)
		}
		return true, nil
	}); err != nil {
		return errorResult(fmt.Errorf("query events: %w", err)), nil
	}

	slog.Info("get_protocol_catalog completed",
		"session_id", sessionID, "event_count", agg.eventCount, "total_protocols", len(agg.aggs))
	return successResult(agg.build(limit, offset)), nil
}