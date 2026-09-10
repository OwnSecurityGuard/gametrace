// Package state 的 query.go 实现「状态变更分析」查询引擎：
// 一次查询把窗口内的字段变更富化成带来源消息（请求/响应/推送）的变更流，
// 并按 操作 / 实体 / 事件 / 时间段 四种维度同时给出分组结果。
//
// 三种视图（按操作 / 按实体 / 按时间）共用同一份 Changes，
// 因此前端切换视图无需重新查询，跨视图跳转只是换一个渲染维度。
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// DataSource 是状态变更查询所需的最小读取能力。
// store.SQLiteStore / store.PGStore 均满足（captureReader 的超集）。
type DataSource interface {
	QueryStateChanges(ctx context.Context, q store.StateChangeQuery) ([]store.StateChangeRow, error)
	QueryEventsInRange(ctx context.Context, sessionID string, from, to time.Time, limit int) ([]*event.Event, error)
	QueryEventsByCorrelation(ctx context.Context, correlationID string, limit, offset int) ([]*event.Event, error)
	GetEventByID(ctx context.Context, id string) (*event.Event, error)
}

// ===== 查询参数 =====

// AnchorKind 是锚点类型：决定时间窗口的原点 T0 与是否附加实体约束。
type AnchorKind string

const (
	// AnchorNone 无锚点，覆盖整会话（或显式 From/To）。
	AnchorNone AnchorKind = ""
	// AnchorOperation 以一次协议操作（请求及其响应/推送）为锚点。
	AnchorOperation AnchorKind = "operation"
	// AnchorEntity 以实体 subject_type:subject_id 为锚点，附加实体约束。
	AnchorEntity AnchorKind = "entity"
	// AnchorEvent 以具体事件为锚点。
	AnchorEvent AnchorKind = "event"
)

// GroupBy 是聚合维度。
type GroupBy string

const (
	GroupByOperation GroupBy = "operation"
	GroupByEntity    GroupBy = "entity"
	GroupByEvent     GroupBy = "event"
	GroupByTime      GroupBy = "time"
)

// SortBy 是分组排序方式。
type SortBy string

const (
	// SortByTime 按组内最近一次变化时间排序（默认 desc：最近发生在前）。
	SortByTime SortBy = "time"
	// SortByFirstChange 按组内首次变化时间排序（默认 asc：时间流）。
	SortByFirstChange SortBy = "first_change"
	// SortByChangeCount 按变化数量排序（默认 desc）。
	SortByChangeCount SortBy = "change_count"
)

// Filter 是变更级过滤条件。多值字段为 OR 语义，不同字段之间为 AND。
type Filter struct {
	SubjectTypes []string `json:"subject_types,omitempty"`
	SubjectIDs   []string `json:"subject_ids,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Ops          []string `json:"ops,omitempty"`
}

// Empty 返回是否没有任何过滤条件。
func (f Filter) Empty() bool {
	return len(f.SubjectTypes) == 0 && len(f.SubjectIDs) == 0 && len(f.Paths) == 0 && len(f.Ops) == 0
}

// Query 描述一次状态变更查询。
type Query struct {
	SessionID string `json:"session_id"`
	// Anchor 决定时间原点 T0 与实体约束。
	Anchor Anchor `json:"anchor,omitempty"`
	// BeforeMS / AfterMS 是观察窗口：T0-BeforeMS ~ T0+AfterMS。
	// 两者均为 0 表示不限制时间（整会话 / 实体完整历史）。
	BeforeMS int64 `json:"before_ms,omitempty"`
	AfterMS  int64 `json:"after_ms,omitempty"`
	// From / To 是显式时间范围，优先级高于 BeforeMS/AfterMS（用于「按时间」视图的区间选择）。
	From time.Time `json:"from,omitempty"`
	To   time.Time `json:"to,omitempty"`
	// GroupBy 决定返回的分组维度；ResultSet 始终携带全部四种分组。
	GroupBy GroupBy `json:"group_by,omitempty"`
	SortBy  SortBy  `json:"sort_by,omitempty"`
	// Desc 为 true 时倒序排列分组。
	Desc bool `json:"desc,omitempty"`
	// BucketMS 是「按时间」视图的分桶大小；0 表示自动选择。
	BucketMS int64 `json:"bucket_ms,omitempty"`
	// Limit 是加载的变更上限（0 → 默认 1000，硬上限 5000）。
	Limit  int    `json:"limit,omitempty"`
	Filter Filter `json:"filter,omitempty"`
}

// Anchor 是查询锚点。
type Anchor struct {
	Kind AnchorKind `json:"kind"`
	// ID 的语义随 Kind 而定：
	//   operation → 事件 ID 或 correlation_id
	//   entity    → "subject_type:subject_id"
	//   event     → 事件 ID
	ID string `json:"id,omitempty"`
}

// ===== 结果类型 =====

// MessageRef 描述产生变更的协议消息（请求 / 响应 / 推送）。
type MessageRef struct {
	EventID       string    `json:"event_id"`
	Timestamp     time.Time `json:"timestamp"`
	OffsetMS      int64     `json:"offset_ms"`
	MsgName       string    `json:"msg_name"`
	Kind          string    `json:"kind"` // request | response | push | unknown
	Direction     string    `json:"direction,omitempty"`
	FlowID        string    `json:"flow_id,omitempty"`
	ConnID        string    `json:"conn_id,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	CausationID   string    `json:"causation_id,omitempty"`
	// OperationKey 是操作分组键：有 correlation 用 correlation，否则用自身事件 ID
	//（未配对的推送各自成组，与 Connections 的 stream 语义一致）。
	OperationKey string `json:"operation_key"`
}

// Change 是一条富化后的字段变更：state_changes 的一行 + 来源消息 + 序号/相对时间。
type Change struct {
	ID          string          `json:"id"`
	Seq         int             `json:"seq"`
	EventID     string          `json:"event_id"`
	SessionID   string          `json:"session_id,omitempty"`
	FlowID      string          `json:"flow_id,omitempty"`
	Timestamp   time.Time       `json:"timestamp"`
	OffsetMS    int64           `json:"offset_ms"`
	SubjectType string          `json:"subject_type"`
	SubjectID   string          `json:"subject_id"`
	EntityKey   string          `json:"entity_key"`
	Op          string          `json:"op"`
	Path        string          `json:"path"`
	Before      json.RawMessage `json:"before,omitempty"`
	After       json.RawMessage `json:"after,omitempty"`
	Version     int64           `json:"version,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	Source      MessageRef      `json:"source"`
}

// EntityGroup 是一个实体的变更聚合。
type EntityGroup struct {
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	Key         string    `json:"key"`
	ChangeCount int       `json:"change_count"`
	FieldCount  int       `json:"field_count"`
	FieldPaths  []string  `json:"field_paths"`
	FirstChange time.Time `json:"first_change"`
	LastChange  time.Time `json:"last_change"`
	// 相对锚点的偏移，用于「T+220ms」展示。
	FirstOffsetMS int64 `json:"first_offset_ms"`
	LastOffsetMS  int64 `json:"last_offset_ms"`
	// Operations 是「按实体」视图下的下一层：产生该实体变更的操作/事件。
	Operations []OperationHit `json:"operations,omitempty"`
	Changes    []Change       `json:"changes,omitempty"`
}

// OperationHit 是「某操作/事件对某实体造成了多少次变化」的轻量聚合。
type OperationHit struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Kind        string    `json:"kind"`
	EventID     string    `json:"event_id"`
	Timestamp   time.Time `json:"timestamp"`
	OffsetMS    int64     `json:"offset_ms"`
	ChangeCount int       `json:"change_count"`
	FieldPaths  []string  `json:"field_paths"`
	// Events 是该操作下真正改动了本实体的消息（请求/响应/推送），按时间排序。
	Events []MessageRef `json:"events,omitempty"`
}

// OperationGroup 是一次协议操作的变更聚合：协议连 → 实体 → 字段变化。
type OperationGroup struct {
	Key         string       `json:"key"`
	Label       string       `json:"label"`
	Kind        string       `json:"kind"`
	Anchor      MessageRef   `json:"anchor"`
	Chain       []MessageRef `json:"chain"`
	ChangeCount int          `json:"change_count"`
	EntityCount int          `json:"entity_count"`
	FieldCount  int          `json:"field_count"`
	// 相对锚点的起止偏移（窗口视角）。
	StartOffsetMS int64         `json:"start_offset_ms"`
	EndOffsetMS   int64         `json:"end_offset_ms"`
	Entities      []EntityGroup `json:"entities,omitempty"`
	Changes       []Change      `json:"changes,omitempty"`
}

// EventGroup 是单个事件（消息）产生的变更聚合。
type EventGroup struct {
	EventID     string     `json:"event_id"`
	Source      MessageRef `json:"source"`
	ChangeCount int        `json:"change_count"`
	EntityCount int        `json:"entity_count"`
	Changes     []Change   `json:"changes,omitempty"`
}

// BucketEntity 是时间桶内某个实体的轻量聚合。
type BucketEntity struct {
	Key         string   `json:"key"`
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	ChangeCount int      `json:"change_count"`
	FieldPaths  []string `json:"field_paths"`
}

// BucketOperation 是时间桶内某个操作的轻量聚合。
type BucketOperation struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	Kind        string         `json:"kind"`
	OffsetMS    int64          `json:"offset_ms"`
	ChangeCount int            `json:"change_count"`
	Entities    []BucketEntity `json:"entities"`
}

// TimeBucket 是一个时间段内的变更密度与顺序。
type TimeBucket struct {
	Index         int               `json:"index"`
	StartOffsetMS int64             `json:"start_offset_ms"`
	EndOffsetMS   int64             `json:"end_offset_ms"`
	Start         time.Time         `json:"start"`
	ChangeCount   int               `json:"change_count"`
	EntityCount   int               `json:"entity_count"`
	Operations    []BucketOperation `json:"operations,omitempty"`
}

// Summary 是结果集的总体统计。
type Summary struct {
	ChangeCount    int       `json:"change_count"`
	EntityCount    int       `json:"entity_count"`
	FieldCount     int       `json:"field_count"`
	OperationCount int       `json:"operation_count"`
	EventCount     int       `json:"event_count"`
	FirstChange    time.Time `json:"first_change,omitempty"`
	LastChange     time.Time `json:"last_change,omitempty"`
	// EntityChanges 是「每个实体变了几次、涉及哪些字段」的快速索引（按变化数降序）。
	EntityChanges []EntityGroup `json:"entity_changes,omitempty"`
}

// ResultSet 是一次查询的完整结果：扁平变更流 + 四种维度分组。
type ResultSet struct {
	SessionID  string           `json:"session_id"`
	GroupBy    GroupBy          `json:"group_by"`
	SortBy     SortBy           `json:"sort_by"`
	Desc       bool             `json:"desc"`
	Anchor     *AnchorRef       `json:"anchor,omitempty"`
	Window     Window           `json:"window"`
	Truncated  bool             `json:"truncated"`
	Summary    Summary          `json:"summary"`
	Changes    []Change         `json:"changes"`
	Operations []OperationGroup `json:"operations,omitempty"`
	Entities   []EntityGroup    `json:"entities,omitempty"`
	Events     []EventGroup     `json:"events,omitempty"`
	Buckets    []TimeBucket     `json:"buckets,omitempty"`
}

// AnchorRef 描述解析后的锚点。
type AnchorRef struct {
	Kind AnchorKind `json:"kind"`
	ID   string     `json:"id"`
	// T0 是相对时间原点。
	T0 time.Time `json:"t0"`
	// Resolved 为 false 表示锚点没找到（时间原点退化为首条变更）。
	Resolved bool        `json:"resolved"`
	Note     string      `json:"note,omitempty"`
	Source   *MessageRef `json:"source,omitempty"`
}

// Window 是查询覆盖的时间范围。
type Window struct {
	From time.Time `json:"from,omitempty"`
	To   time.Time `json:"to,omitempty"`
	// BeforeMS/AfterMS 是生效的观察窗口（相对 T0）。
	BeforeMS int64 `json:"before_ms,omitempty"`
	AfterMS  int64 `json:"after_ms,omitempty"`
}

// ===== 引擎 =====

const (
	defaultChangeLimit = 1000
	maxChangeLimit     = 5000
	defaultEventScan   = 5000
)

// Run 执行一次状态变更查询。
func Run(ctx context.Context, src DataSource, q Query) (*ResultSet, error) {
	if src == nil {
		return nil, fmt.Errorf("state: nil data source")
	}
	if q.SessionID == "" {
		return nil, fmt.Errorf("state: session_id is required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultChangeLimit
	}
	if limit > maxChangeLimit {
		limit = maxChangeLimit
	}

	anchor, err := resolveAnchor(ctx, src, q)
	if err != nil {
		return nil, err
	}
	from, to := resolveWindow(q, anchor)

	rows, err := src.QueryStateChanges(ctx, store.StateChangeQuery{
		SessionID:   q.SessionID,
		SubjectType: anchor.subjectType,
		SubjectID:   anchor.subjectID,
		From:        from,
		To:          to,
		Limit:       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("state: query state changes: %w", err)
	}

	// 事件上下文：窗口内一次拉齐，用于把每条变更挂到请求/响应/推送上。
	msgIdx, err := buildMessageIndex(ctx, src, q, rows, from, to, anchor)
	if err != nil {
		return nil, err
	}

	t0 := anchor.T0
	changes := make([]Change, 0, len(rows))
	for _, r := range rows {
		if !q.Filter.matches(r.SubjectType, r.SubjectID, r.Path, r.Op) {
			continue
		}
		src := msgIdx.byID[r.EventID]
		if src.EventID == "" {
			src = MessageRef{EventID: r.EventID, Timestamp: r.Timestamp, Kind: "unknown", OperationKey: r.EventID}
		}
		if src.FlowID == "" {
			src.FlowID = r.FlowID
		}
		changes = append(changes, Change{
			ID:          r.ID,
			EventID:     r.EventID,
			SessionID:   r.SessionID,
			FlowID:      r.FlowID,
			Timestamp:   r.Timestamp,
			SubjectType: r.SubjectType,
			SubjectID:   r.SubjectID,
			EntityKey:   EntityKeyOf(r.SubjectType, r.SubjectID),
			Op:          r.Op,
			Path:        r.Path,
			Before:      rawJSON(r.Before),
			After:       rawJSON(r.After),
			Version:     r.Version,
			Metadata:    rawJSON(r.Metadata),
			Source:      src,
		})
	}
	if len(changes) == 0 {
		return emptyResult(q, anchor, from, to), nil
	}

	// 时间原点：无锚点时退化为首条变更时间，保证 T+0ms 有明确含义。
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Timestamp.Equal(changes[j].Timestamp) {
			return changes[i].ID < changes[j].ID
		}
		return changes[i].Timestamp.Before(changes[j].Timestamp)
	})
	if t0.IsZero() {
		t0 = changes[0].Timestamp
		anchor.T0 = t0
	}
	applyOffsets(changes, t0)

	rs := &ResultSet{
		SessionID:  q.SessionID,
		GroupBy:    q.GroupBy,
		SortBy:     q.SortBy,
		Desc:       q.Desc,
		Anchor:     &anchor.AnchorRef,
		Window:     Window{From: from, To: to, BeforeMS: q.BeforeMS, AfterMS: q.AfterMS},
		Truncated:  len(rows) >= limit,
		Changes:    changes,
		Operations: groupByOperation(changes, q, msgIdx),
		Entities:   groupByEntity(changes, q),
		Events:     groupByEvent(changes, q),
		Buckets:    groupByTime(changes, q, t0),
	}
	rs.Summary = summarize(changes, rs)
	return rs, nil
}

// ===== 锚点与窗口 =====

type resolvedAnchor struct {
	AnchorRef
	// subjectType/subjectID 是实体锚点带来的约束（可为空）。
	subjectType string
	subjectID   string
}

func resolveAnchor(ctx context.Context, src DataSource, q Query) (resolvedAnchor, error) {
	out := resolvedAnchor{AnchorRef: AnchorRef{Kind: q.Anchor.Kind, ID: q.Anchor.ID}}
	switch q.Anchor.Kind {
	case AnchorNone:
		return out, nil
	case AnchorEntity:
		st, sid, ok := splitEntityID(q.Anchor.ID)
		if !ok {
			return out, fmt.Errorf("state: entity anchor %q is not subject_type:subject_id", q.Anchor.ID)
		}
		out.subjectType, out.subjectID = st, sid
		rows, err := src.QueryStateChanges(ctx, store.StateChangeQuery{
			SessionID:   q.SessionID,
			SubjectType: st,
			SubjectID:   sid,
			Limit:       1,
		})
		if err != nil {
			return out, err
		}
		if len(rows) > 0 {
			out.T0 = rows[0].Timestamp
			out.Resolved = true
		} else {
			out.Note = "实体在本次会话中没有状态变更"
		}
		return out, nil
	case AnchorEvent, AnchorOperation:
		// 锚点 ID 可能是本引擎生成的分组键（corr:xxx / evt:xxx），先拆前缀再查。
		eventID, corrID := splitOperationKey(q.Anchor.ID)
		ev, err := src.GetEventByID(ctx, eventID)
		if err != nil {
			return out, err
		}
		if ev == nil && corrID != "" {
			evs, cerr := src.QueryEventsByCorrelation(ctx, corrID, defaultEventScan, 0)
			if cerr == nil && len(evs) > 0 {
				earliest := evs[0]
				for _, e := range evs {
					if e.Identity.Timestamp.Before(earliest.Identity.Timestamp) {
						earliest = e
					}
				}
				ev = earliest
			}
		}
		if ev == nil {
			out.Note = "锚点未找到，时间原点退化为首条变更"
			return out, nil
		}
		out.T0 = ev.Identity.Timestamp
		out.Resolved = true
		ref := newMessageRef(ev, 0)
		out.Source = &ref
		return out, nil
	default:
		return out, fmt.Errorf("state: unknown anchor kind %q", q.Anchor.Kind)
	}
}

// splitOperationKey 拆开分组键：corr:xxx → ("", "xxx")；evt:xxx → ("xxx", "")；
// 无前缀时按事件 ID 处理（并同时允许它是 correlation_id）。
func splitOperationKey(id string) (eventID, corrID string) {
	switch {
	case strings.HasPrefix(id, "corr:"):
		return "", strings.TrimPrefix(id, "corr:")
	case strings.HasPrefix(id, "evt:"):
		return strings.TrimPrefix(id, "evt:"), ""
	default:
		return id, id
	}
}

// resolveWindow 计算生效的时间窗口：显式 From/To 优先，否则按锚点前后 N 毫秒。
func resolveWindow(q Query, a resolvedAnchor) (time.Time, time.Time) {
	if !q.From.IsZero() || !q.To.IsZero() {
		return q.From, q.To
	}
	if a.T0.IsZero() || (q.BeforeMS == 0 && q.AfterMS == 0) {
		return time.Time{}, time.Time{}
	}
	return a.T0.Add(-time.Duration(q.BeforeMS) * time.Millisecond),
		a.T0.Add(time.Duration(q.AfterMS) * time.Millisecond)
}

// ===== 事件上下文 =====

// buildMessageIndex 构造 event_id → MessageRef 索引。
// 优先用窗口内事件；窗口为空时按变更自身的时间跨度补齐，避免全会话加载。
type messageIndex struct {
	// byID 是 event_id → 消息。
	byID map[string]MessageRef
	// byCorr 是 correlation_id → 该操作下的全部消息（请求/响应/推送），
	// 用于把「没产生变更的请求」也补进协议连。
	byCorr map[string][]MessageRef
}

func buildMessageIndex(ctx context.Context, src DataSource, q Query, rows []store.StateChangeRow, from, to time.Time, a resolvedAnchor) (messageIndex, error) {
	scanFrom, scanTo := from, to
	if scanFrom.IsZero() && len(rows) > 0 {
		scanFrom = rows[0].Timestamp
	}
	if scanTo.IsZero() && len(rows) > 0 {
		scanTo = rows[len(rows)-1].Timestamp
	}
	// 上下文需要比变更窗口略宽：来源消息可能略早于窗口下界被写入。
	if !scanFrom.IsZero() {
		scanFrom = scanFrom.Add(-time.Second)
	}
	if !scanTo.IsZero() {
		scanTo = scanTo.Add(time.Second)
	}
	events, err := src.QueryEventsInRange(ctx, q.SessionID, scanFrom, scanTo, defaultEventScan)
	if err != nil {
		return messageIndex{}, fmt.Errorf("state: query events: %w", err)
	}
	idx := messageIndex{
		byID:   make(map[string]MessageRef, len(events)),
		byCorr: make(map[string][]MessageRef),
	}
	for _, ev := range events {
		ref := newMessageRef(ev, 0)
		idx.byID[ref.EventID] = ref
		if ref.CorrelationID != "" {
			idx.byCorr[ref.CorrelationID] = append(idx.byCorr[ref.CorrelationID], ref)
		}
	}
	if a.Source != nil {
		idx.byID[a.Source.EventID] = *a.Source
		if a.Source.CorrelationID != "" {
			idx.byCorr[a.Source.CorrelationID] = append(idx.byCorr[a.Source.CorrelationID], *a.Source)
		}
	}
	// 兜底：极少数事件落在扫描范围外（写入乱序）时按 ID 单点补。
	for _, r := range rows {
		if _, ok := idx.byID[r.EventID]; ok {
			continue
		}
		ev, err := src.GetEventByID(ctx, r.EventID)
		if err != nil || ev == nil {
			continue
		}
		ref := newMessageRef(ev, 0)
		idx.byID[ref.EventID] = ref
		if ref.CorrelationID != "" {
			idx.byCorr[ref.CorrelationID] = append(idx.byCorr[ref.CorrelationID], ref)
		}
	}
	return idx, nil
}

// newMessageRef 从事件构造 MessageRef。offset 在 applyOffsets 阶段统一计算。
func newMessageRef(ev *event.Event, offsetMS int64) MessageRef {
	ref := MessageRef{
		EventID:       string(ev.Identity.ID),
		Timestamp:     ev.Identity.Timestamp,
		OffsetMS:      offsetMS,
		MsgName:       messageName(ev),
		Direction:     messageDirection(ev),
		FlowID:        ev.Context.FlowID,
		ConnID:        ev.Context.ConnID,
		CorrelationID: string(ev.Trace.CorrelationID),
		CausationID:   string(ev.Trace.CausationID),
	}
	ref.Kind = messageKind(ev, ref.Direction, ref.MsgName)
	ref.OperationKey = operationKey(ref)
	return ref
}

// operationKey 计算操作分组键：有 correlation 就用它（请求/响应/推送归为同一操作），
// 否则用自身事件 ID（未配对消息各自成组）。
func operationKey(ref MessageRef) string {
	if ref.CorrelationID != "" {
		return "corr:" + ref.CorrelationID
	}
	return "evt:" + ref.EventID
}

func messageName(ev *event.Event) string {
	if v, ok := ev.MetaValue("msg_name"); ok {
		if s, ok := v.AsString(); ok && s != "" {
			return s
		}
	}
	if obj, ok := ev.Payload.Value.AsObject(); ok {
		if v, ok := obj["msg_name"]; ok {
			if s, ok := v.AsString(); ok && s != "" {
				return s
			}
		}
	}
	return string(ev.Identity.Type)
}

func messageDirection(ev *event.Event) string {
	if ev.Context.Direction != "" {
		return ev.Context.Direction
	}
	if v, ok := ev.MetaValue("direction"); ok {
		if s, ok := v.AsString(); ok {
			return s
		}
	}
	if obj, ok := ev.Payload.Value.AsObject(); ok {
		if v, ok := obj["direction"]; ok {
			if s, ok := v.AsString(); ok {
				return s
			}
		}
	}
	return ""
}

// messageKind 判定消息语义：request / response / push / unknown。
// 判定顺序：_meta 显式标记 → 方向 → 消息名后缀。
func messageKind(ev *event.Event, direction, msgName string) string {
	if v, ok := ev.MetaValue("is_push"); ok {
		if b, ok := v.AsBool(); ok && b {
			return "push"
		}
	}
	if v, ok := ev.MetaValue("type"); ok {
		if s, ok := v.AsString(); ok && strings.EqualFold(s, "push") {
			return "push"
		}
	}
	switch strings.ToLower(direction) {
	case "client_to_server", "c2s", "request", "outgoing", "up":
		return "request"
	case "server_to_client", "s2c", "response", "incoming", "down":
		return "response"
	}
	name := strings.ToLower(msgName)
	switch {
	case strings.HasSuffix(name, "req"), strings.HasSuffix(name, "request"), strings.HasSuffix(name, "_c"), strings.HasSuffix(name, ".c"):
		return "request"
	case strings.HasSuffix(name, "resp"), strings.HasSuffix(name, "response"), strings.HasSuffix(name, "ack"), strings.HasSuffix(name, "_s"), strings.HasSuffix(name, ".s"):
		return "response"
	case strings.Contains(name, "push"), strings.Contains(name, "notify"), strings.Contains(name, "update"):
		return "push"
	}
	return "unknown"
}

// ===== 分组 =====

func groupByOperation(changes []Change, q Query, idx messageIndex) []OperationGroup {
	byKey := make(map[string]*OperationGroup)
	order := make([]string, 0, 8)
	for _, c := range changes {
		key := c.Source.OperationKey
		g, ok := byKey[key]
		if !ok {
			g = &OperationGroup{Key: key, Anchor: c.Source}
			byKey[key] = g
			order = append(order, key)
		}
		g.Changes = append(g.Changes, c)
	}
	groups := make([]OperationGroup, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		finalizeOperation(g, idx)
		groups = append(groups, *g)
	}
	sortOperationGroups(groups, q)
	return groups
}

// finalizeOperation 汇总协议连、实体与统计。
func finalizeOperation(g *OperationGroup, idx messageIndex) {
	seenMsg := make(map[string]bool)
	entities := make(map[string]*EntityGroup)
	for _, c := range g.Changes {
		if !seenMsg[c.Source.EventID] {
			seenMsg[c.Source.EventID] = true
			g.Chain = append(g.Chain, c.Source)
		}
		e, ok := entities[c.EntityKey]
		if !ok {
			e = &EntityGroup{SubjectType: c.SubjectType, SubjectID: c.SubjectID, Key: c.EntityKey}
			entities[c.EntityKey] = e
		}
		e.Changes = append(e.Changes, c)
	}
	// 协议连要含「没产生变更的请求」：用户看的是发出去的协议，不是只看有变更的消息。
	if corr := strings.TrimPrefix(g.Key, "corr:"); corr != "" && corr != g.Key {
		seen := make(map[string]bool, len(g.Chain))
		for _, m := range g.Chain {
			seen[m.EventID] = true
		}
		for _, m := range idx.byCorr[corr] {
			if !seen[m.EventID] {
				seen[m.EventID] = true
				g.Chain = append(g.Chain, m)
			}
		}
	}
	sort.Slice(g.Chain, func(i, j int) bool { return g.Chain[i].Timestamp.Before(g.Chain[j].Timestamp) })
	// 操作标签取「请求优先、其次最早」的那条消息——用户认的是发出去的协议名。
	anchor := g.Anchor
	for _, m := range g.Chain {
		if m.Kind == "request" {
			anchor = m
			break
		}
	}
	if anchor.Kind != "request" && len(g.Chain) > 0 {
		anchor = g.Chain[0]
	}
	g.Anchor = anchor
	g.Label = anchor.MsgName
	g.Kind = anchor.Kind
	g.ChangeCount = len(g.Changes)
	g.EntityCount = len(entities)
	for _, e := range entities {
		finalizeEntity(e)
		g.Entities = append(g.Entities, *e)
	}
	sort.Slice(g.Entities, func(i, j int) bool {
		if g.Entities[i].FirstChange.Equal(g.Entities[j].FirstChange) {
			return g.Entities[i].Key < g.Entities[j].Key
		}
		return g.Entities[i].FirstChange.Before(g.Entities[j].FirstChange)
	})
	if len(g.Changes) > 0 {
		g.StartOffsetMS = g.Changes[0].OffsetMS
		g.EndOffsetMS = g.Changes[len(g.Changes)-1].OffsetMS
	}
}

func groupByEntity(changes []Change, q Query) []EntityGroup {
	byKey := make(map[string]*EntityGroup)
	order := make([]string, 0, 8)
	for _, c := range changes {
		g, ok := byKey[c.EntityKey]
		if !ok {
			g = &EntityGroup{SubjectType: c.SubjectType, SubjectID: c.SubjectID, Key: c.EntityKey}
			byKey[c.EntityKey] = g
			order = append(order, c.EntityKey)
		}
		g.Changes = append(g.Changes, c)
	}
	groups := make([]EntityGroup, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		finalizeEntity(g)
		groups = append(groups, *g)
	}
	sortEntityGroups(groups, q)
	return groups
}

// finalizeEntity 汇总字段路径与「哪些操作改过我」。
func finalizeEntity(g *EntityGroup) {
	if len(g.Changes) == 0 {
		return
	}
	g.FirstChange = g.Changes[0].Timestamp
	g.LastChange = g.Changes[len(g.Changes)-1].Timestamp
	g.FirstOffsetMS = g.Changes[0].OffsetMS
	g.LastOffsetMS = g.Changes[len(g.Changes)-1].OffsetMS
	g.ChangeCount = len(g.Changes)

	paths := make(map[string]bool)
	ops := make(map[string]*OperationHit)
	for _, c := range g.Changes {
		paths[c.Path] = true
		hit, ok := ops[c.Source.OperationKey]
		if !ok {
			hit = &OperationHit{
				Key:       c.Source.OperationKey,
				Label:     c.Source.MsgName,
				Kind:      c.Source.Kind,
				EventID:   c.Source.EventID,
				Timestamp: c.Source.Timestamp,
				OffsetMS:  c.Source.OffsetMS,
			}
			ops[c.Source.OperationKey] = hit
		}
		hit.ChangeCount++
		hit.FieldPaths = appendUnique(hit.FieldPaths, c.Path)
		if !hitHasEvent(hit, c.Source.EventID) {
			hit.Events = append(hit.Events, c.Source)
		}
	}
	g.FieldPaths = sortedKeys(paths)
	g.FieldCount = len(g.FieldPaths)
	g.Operations = make([]OperationHit, 0, len(ops))
	for _, hit := range ops {
		sort.Slice(hit.Events, func(i, j int) bool { return hit.Events[i].Timestamp.Before(hit.Events[j].Timestamp) })
		g.Operations = append(g.Operations, *hit)
	}
	sort.Slice(g.Operations, func(i, j int) bool {
		if g.Operations[i].Timestamp.Equal(g.Operations[j].Timestamp) {
			return g.Operations[i].Key < g.Operations[j].Key
		}
		return g.Operations[i].Timestamp.Before(g.Operations[j].Timestamp)
	})
}

func groupByEvent(changes []Change, q Query) []EventGroup {
	byKey := make(map[string]*EventGroup)
	order := make([]string, 0, 8)
	for _, c := range changes {
		g, ok := byKey[c.EventID]
		if !ok {
			g = &EventGroup{EventID: c.EventID, Source: c.Source}
			byKey[c.EventID] = g
			order = append(order, c.EventID)
		}
		g.Changes = append(g.Changes, c)
	}
	groups := make([]EventGroup, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		g.ChangeCount = len(g.Changes)
		entities := make(map[string]bool)
		for _, c := range g.Changes {
			entities[c.EntityKey] = true
		}
		g.EntityCount = len(entities)
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		ti, tj := groups[i].Source.Timestamp, groups[j].Source.Timestamp
		if ti.Equal(tj) {
			return groups[i].EventID < groups[j].EventID
		}
		return ti.Before(tj)
	})
	if q.Desc {
		reverseSlice(groups)
	}
	return groups
}

func groupByTime(changes []Change, q Query, t0 time.Time) []TimeBucket {
	if len(changes) == 0 {
		return nil
	}
	bucketMS := q.BucketMS
	if bucketMS <= 0 {
		bucketMS = autoBucketMS(changes[0].OffsetMS, changes[len(changes)-1].OffsetMS)
	}
	buckets := make([]TimeBucket, 0, 8)
	index := make(map[int64]int)
	for _, c := range changes {
		startOff := floorTo(c.OffsetMS, bucketMS)
		i, ok := index[startOff]
		if !ok {
			buckets = append(buckets, TimeBucket{
				Index:         len(buckets),
				StartOffsetMS: startOff,
				EndOffsetMS:   startOff + bucketMS,
				Start:         t0.Add(time.Duration(startOff) * time.Millisecond),
			})
			i = len(buckets) - 1
			index[startOff] = i
		}
		b := &buckets[i]
		b.ChangeCount++
		// 操作 → 实体 的两层聚合（时间视图只关心密度与顺序，不展开字段值）。
		opIdx := -1
		for j := range b.Operations {
			if b.Operations[j].Key == c.Source.OperationKey {
				opIdx = j
				break
			}
		}
		if opIdx < 0 {
			b.Operations = append(b.Operations, BucketOperation{
				Key:      c.Source.OperationKey,
				Label:    c.Source.MsgName,
				Kind:     c.Source.Kind,
				OffsetMS: floorTo(c.Source.OffsetMS, bucketMS),
			})
			opIdx = len(b.Operations) - 1
		}
		op := &b.Operations[opIdx]
		op.ChangeCount++
		entIdx := -1
		for j := range op.Entities {
			if op.Entities[j].Key == c.EntityKey {
				entIdx = j
				break
			}
		}
		if entIdx < 0 {
			op.Entities = append(op.Entities, BucketEntity{
				Key:         c.EntityKey,
				SubjectType: c.SubjectType,
				SubjectID:   c.SubjectID,
			})
			entIdx = len(op.Entities) - 1
		}
		ent := &op.Entities[entIdx]
		ent.ChangeCount++
		ent.FieldPaths = appendUnique(ent.FieldPaths, c.Path)
	}
	for i := range buckets {
		entities := make(map[string]bool)
		for _, op := range buckets[i].Operations {
			for _, e := range op.Entities {
				entities[e.Key] = true
			}
		}
		buckets[i].EntityCount = len(entities)
	}
	if q.Desc {
		reverseSlice(buckets)
	}
	return buckets
}

// autoBucketMS 选一个「人读得懂」的桶宽，目标约 20 个桶。
func autoBucketMS(firstMS, lastMS int64) int64 {
	span := lastMS - firstMS
	if span < 0 {
		span = -span
	}
	target := span / 20
	if target < 1 {
		target = 1
	}
	steps := []int64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2000, 5000, 10_000, 30_000, 60_000, 300_000}
	for _, s := range steps {
		if target <= s {
			return s
		}
	}
	return 600_000
}

func floorTo(v, unit int64) int64 {
	if unit <= 0 {
		return v
	}
	q := v / unit
	if v < 0 && v%unit != 0 {
		q-- // 负偏移向下取整，保证桶边界连续
	}
	return q * unit
}

func summarize(changes []Change, rs *ResultSet) Summary {
	s := Summary{
		ChangeCount:    len(changes),
		OperationCount: len(rs.Operations),
		EventCount:     len(rs.Events),
	}
	if len(changes) > 0 {
		s.FirstChange = changes[0].Timestamp
		s.LastChange = changes[len(changes)-1].Timestamp
	}
	entities := make([]EntityGroup, len(rs.Entities))
	copy(entities, rs.Entities)
	sort.SliceStable(entities, func(i, j int) bool {
		if entities[i].ChangeCount == entities[j].ChangeCount {
			return entities[i].Key < entities[j].Key
		}
		return entities[i].ChangeCount > entities[j].ChangeCount
	})
	s.EntityChanges = entities
	s.EntityCount = len(entities)
	fields := make(map[string]bool)
	for _, c := range changes {
		fields[c.SubjectType+"."+c.Path] = true
	}
	s.FieldCount = len(fields)
	return s
}

func emptyResult(q Query, a resolvedAnchor, from, to time.Time) *ResultSet {
	return &ResultSet{
		SessionID:  q.SessionID,
		GroupBy:    q.GroupBy,
		SortBy:     q.SortBy,
		Desc:       q.Desc,
		Anchor:     &AnchorRef{Kind: a.Kind, ID: a.ID, T0: a.T0, Resolved: a.Resolved, Note: a.Note, Source: a.Source},
		Window:     Window{From: from, To: to, BeforeMS: q.BeforeMS, AfterMS: q.AfterMS},
		Changes:    []Change{},
		Operations: []OperationGroup{},
		Entities:   []EntityGroup{},
		Events:     []EventGroup{},
		Buckets:    []TimeBucket{},
	}
}

// ===== 排序 =====

func sortOperationGroups(groups []OperationGroup, q Query) {
	switch q.SortBy {
	case SortByChangeCount:
		sort.SliceStable(groups, func(i, j int) bool { return groups[i].ChangeCount > groups[j].ChangeCount })
	case SortByTime:
		sort.SliceStable(groups, func(i, j int) bool {
			return groups[i].EndOffsetMS > groups[j].EndOffsetMS
		})
	default: // SortByFirstChange
		sort.SliceStable(groups, func(i, j int) bool {
			return groups[i].StartOffsetMS < groups[j].StartOffsetMS
		})
	}
	if q.Desc {
		reverseSlice(groups)
	}
}

func sortEntityGroups(groups []EntityGroup, q Query) {
	switch q.SortBy {
	case SortByChangeCount:
		sort.SliceStable(groups, func(i, j int) bool { return groups[i].ChangeCount > groups[j].ChangeCount })
	case SortByTime:
		sort.SliceStable(groups, func(i, j int) bool {
			return groups[i].LastOffsetMS > groups[j].LastOffsetMS
		})
	default:
		sort.SliceStable(groups, func(i, j int) bool {
			return groups[i].FirstOffsetMS < groups[j].FirstOffsetMS
		})
	}
	if q.Desc {
		reverseSlice(groups)
	}
}

// ===== 工具 =====

// EntityKeyOf 拼实体的稳定键（baseline.go 的 EntityKey 是基线上下文结构，别混）。
func EntityKeyOf(subjectType, subjectID string) string { return subjectType + ":" + subjectID }

// SplitEntityID 拆分 "type:id"；subject_id 含冒号时只拆第一段。
func SplitEntityID(id string) (string, string, bool) { return splitEntityID(id) }

func splitEntityID(id string) (string, string, bool) {
	i := strings.Index(id, ":")
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}

func (f Filter) matches(subjectType, subjectID, path, op string) bool {
	if f.Empty() {
		return true
	}
	if len(f.SubjectTypes) > 0 && !containsFold(f.SubjectTypes, subjectType) {
		return false
	}
	if len(f.SubjectIDs) > 0 && !containsFold(f.SubjectIDs, subjectID) {
		return false
	}
	if len(f.Ops) > 0 && !containsFold(f.Ops, op) {
		return false
	}
	if len(f.Paths) > 0 {
		ok := false
		for _, p := range f.Paths {
			if path == p || strings.HasPrefix(path, p+".") || strings.HasPrefix(path, p) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func hitHasEvent(hit *OperationHit, eventID string) bool {
	for _, e := range hit.Events {
		if e.EventID == eventID {
			return true
		}
	}
	return false
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}

func applyOffsets(changes []Change, t0 time.Time) {
	for i := range changes {
		changes[i].Seq = i + 1
		changes[i].OffsetMS = changes[i].Timestamp.Sub(t0).Milliseconds()
	}
	// 来源消息的相对时间也要跟着锚点走（同一消息可能被多条变更引用）。
	srcOffset := make(map[string]int64)
	for i := range changes {
		src := &changes[i].Source
		if _, ok := srcOffset[src.EventID]; ok {
			src.OffsetMS = srcOffset[src.EventID]
			continue
		}
		off := src.Timestamp.Sub(t0).Milliseconds()
		srcOffset[src.EventID] = off
		src.OffsetMS = off
	}
}

func rawJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func appendUnique(list []string, v string) []string {
	for _, item := range list {
		if item == v {
			return list
		}
	}
	return append(list, v)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func reverseSlice[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
