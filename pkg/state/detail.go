// Package state 的 detail.go 实现 get_state_change_detail 的取数逻辑：
// 完整协议链（操作 → 请求/响应/推送 → 实体 → 字段变化）与实体/字段的完整变化历史。
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// DetailQuery 是详情查询参数。三个定位参数至少给一个：
// change_id（某条变更）、event_id（某条协议消息）、entity（某实体）。
type DetailQuery struct {
	SessionID string
	ChangeID  string
	EventID   string
	// Entity 形如 "subject_type:subject_id"。
	Entity string
	// Path 非空时额外返回该字段的历史（需配合 Entity，或由 change_id 推导）。
	Path string
	// BeforeMS / AfterMS 是协议链的展开窗口，默认 1000 / 2000 毫秒。
	BeforeMS int64
	AfterMS  int64
	// Limit 是历史加载上限（0 → 5000）。
	Limit int
}

// ChainStep 是协议链的一环：一条协议消息及其造成的实体变化。
type ChainStep struct {
	Message     MessageRef    `json:"message"`
	OffsetMS    int64         `json:"offset_ms"`
	ChangeCount int           `json:"change_count"`
	Entities    []ChainEntity `json:"entities,omitempty"`
}

// ChainEntity 是协议链某一步内实体的变化（含字段级明细）。
type ChainEntity struct {
	Key         string   `json:"key"`
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	ChangeCount int      `json:"change_count"`
	FieldPaths  []string `json:"field_paths"`
	Changes     []Change `json:"changes,omitempty"`
}

// Chain 是一次操作的完整协议链。
type Chain struct {
	// Operation 是链条的起点（请求优先）。
	Operation  MessageRef  `json:"operation"`
	EventCount int         `json:"event_count"`
	Steps      []ChainStep `json:"steps"`
}

// FieldHistory 是单个字段的完整变化历史。
type FieldHistory struct {
	Path        string          `json:"path"`
	ChangeCount int             `json:"change_count"`
	Ops         []string        `json:"ops"`
	FirstValue  json.RawMessage `json:"first_value,omitempty"`
	LastValue   json.RawMessage `json:"last_value,omitempty"`
	FirstChange time.Time       `json:"first_change,omitempty"`
	LastChange  time.Time       `json:"last_change,omitempty"`
	Changes     []Change        `json:"changes,omitempty"`
}

// EntityHistory 是一个实体的完整变化历史。
type EntityHistory struct {
	SubjectType string         `json:"subject_type"`
	SubjectID   string         `json:"subject_id"`
	Key         string         `json:"key"`
	ChangeCount int            `json:"change_count"`
	FirstChange time.Time      `json:"first_change,omitempty"`
	LastChange  time.Time      `json:"last_change,omitempty"`
	Fields      []FieldHistory `json:"fields,omitempty"`
	Timeline    []Change       `json:"timeline,omitempty"`
}

// Detail 是详情查询结果。
type Detail struct {
	SessionID string `json:"session_id"`
	// T0 是相对时间原点：定位到的那条协议消息的时间。
	T0      time.Time      `json:"t0,omitempty"`
	Change  *Change        `json:"change,omitempty"`
	Chain   *Chain         `json:"chain,omitempty"`
	History *EntityHistory `json:"history,omitempty"`
	// FieldHistory 是指定字段的历史（DetailQuery.Path 非空时返回）。
	FieldHistory *FieldHistory `json:"field_history,omitempty"`
}

const (
	defaultChainBeforeMS = 1000
	defaultChainAfterMS  = 2000
	maxChainSteps        = 50
	maxHistory           = 5000
)

// Detail 查询一条变更 / 一条协议消息 / 一个实体的完整上下文。
func LoadDetail(ctx context.Context, src DataSource, q DetailQuery) (*Detail, error) {
	if src == nil {
		return nil, fmt.Errorf("state: nil data source")
	}
	if q.SessionID == "" {
		return nil, fmt.Errorf("state: session_id is required")
	}
	if q.ChangeID == "" && q.EventID == "" && q.Entity == "" {
		return nil, fmt.Errorf("state: one of change_id / event_id / entity is required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = maxHistory
	}

	out := &Detail{SessionID: q.SessionID}

	// 1) 定位：优先 change_id，其次 event_id，最后 entity。
	var focus Change
	switch {
	case q.ChangeID != "":
		rows, err := src.QueryStateChanges(ctx, store.StateChangeQuery{ID: q.ChangeID, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("state: change %q not found", q.ChangeID)
		}
		c, err := buildChange(ctx, src, rows[0])
		if err != nil {
			return nil, err
		}
		focus = c
		out.Change = &focus
		if q.Entity == "" {
			q.Entity = focus.EntityKey
		}
		if q.Path == "" {
			q.Path = focus.Path
		}
	case q.EventID != "":
		rows, err := src.QueryStateChanges(ctx, store.StateChangeQuery{
			SessionID: q.SessionID, EventID: q.EventID, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			c, err := buildChange(ctx, src, rows[0])
			if err != nil {
				return nil, err
			}
			focus = c
			out.Change = &focus
			if q.Entity == "" {
				q.Entity = focus.EntityKey
			}
		}
	}

	// 2) 时间原点：定位到的消息优先，否则用实体的首次变化。
	var t0 time.Time
	var anchorEvent *event.Event
	if focus.EventID != "" {
		ev, err := src.GetEventByID(ctx, focus.EventID)
		if err == nil && ev != nil {
			anchorEvent = ev
			t0 = ev.Identity.Timestamp
		}
	}
	if t0.IsZero() && q.EventID != "" {
		ev, err := src.GetEventByID(ctx, q.EventID)
		if err == nil && ev != nil {
			anchorEvent = ev
			t0 = ev.Identity.Timestamp
		}
	}
	if t0.IsZero() && q.Entity != "" {
		st, sid, ok := splitEntityID(q.Entity)
		if ok {
			rows, err := src.QueryStateChanges(ctx, store.StateChangeQuery{
				SessionID: q.SessionID, SubjectType: st, SubjectID: sid, Limit: 1,
			})
			if err == nil && len(rows) > 0 {
				t0 = rows[0].Timestamp
				if ev, err := src.GetEventByID(ctx, rows[0].EventID); err == nil && ev != nil {
					anchorEvent = ev
				}
			}
		}
	}
	out.T0 = t0

	// 3) 协议链：窗口内事件 ∪ 同 correlation 事件。
	if anchorEvent != nil {
		chain, err := buildChain(ctx, src, q, anchorEvent, t0, limit)
		if err != nil {
			return nil, err
		}
		out.Chain = chain
	}

	// 4) 实体/字段完整历史。
	if q.Entity != "" {
		st, sid, ok := splitEntityID(q.Entity)
		if !ok {
			return nil, fmt.Errorf("state: entity %q is not subject_type:subject_id", q.Entity)
		}
		hist, err := loadEntityHistory(ctx, src, q, st, sid, t0, limit)
		if err != nil {
			return nil, err
		}
		out.History = hist
		if q.Path != "" && hist != nil {
			for i := range hist.Fields {
				if hist.Fields[i].Path == q.Path {
					fh := hist.Fields[i]
					out.FieldHistory = &fh
					break
				}
			}
		}
	}
	return out, nil
}

// buildChange 把一行状态变更富化成 Change（含来源消息），事件需按 ID 现查。
func buildChange(ctx context.Context, src DataSource, row store.StateChangeRow) (Change, error) {
	var events map[string]*event.Event
	if src != nil {
		if ev, err := src.GetEventByID(ctx, row.EventID); err == nil && ev != nil {
			events = map[string]*event.Event{row.EventID: ev}
		}
	}
	return buildChangeFromEvent(row, events)
}

// buildChangeFromEvent 用已加载的事件表富化变更行；查不到事件时来源标记为 unknown。

// buildChain 组装「操作 → 请求/响应/推送 → 实体 → 字段变化」。
func buildChain(ctx context.Context, src DataSource, q DetailQuery, anchor *event.Event, t0 time.Time, limit int) (*Chain, error) {
	beforeMS, afterMS := q.BeforeMS, q.AfterMS
	if beforeMS == 0 && afterMS == 0 {
		beforeMS, afterMS = defaultChainBeforeMS, defaultChainAfterMS
	}
	from := t0.Add(-time.Duration(beforeMS) * time.Millisecond)
	to := t0.Add(time.Duration(afterMS) * time.Millisecond)

	events, err := src.QueryEventsInRange(ctx, q.SessionID, from, to, defaultEventScan)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*event.Event, len(events)+1)
	for _, e := range events {
		byID[string(e.Identity.ID)] = e
	}
	byID[string(anchor.Identity.ID)] = anchor
	corrID := string(anchor.Trace.CorrelationID)
	if corrID != "" {
		correlated, err := src.QueryEventsByCorrelation(ctx, corrID, defaultEventScan, 0)
		if err == nil {
			for _, e := range correlated {
				byID[string(e.Identity.ID)] = e
			}
		}
	}
	// 因果链：响应指回请求。
	if anchor.Trace.CausationID != "" {
		if ev, err := src.GetEventByID(ctx, string(anchor.Trace.CausationID)); err == nil && ev != nil {
			byID[string(ev.Identity.ID)] = ev
		}
	}

	ordered := make([]*event.Event, 0, len(byID))
	for _, e := range byID {
		ordered = append(ordered, e)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Identity.Timestamp.Equal(ordered[j].Identity.Timestamp) {
			return string(ordered[i].Identity.ID) < string(ordered[j].Identity.ID)
		}
		return ordered[i].Identity.Timestamp.Before(ordered[j].Identity.Timestamp)
	})
	if len(ordered) > maxChainSteps {
		ordered = ordered[:maxChainSteps]
	}
	if len(ordered) == 0 {
		return nil, nil
	}

	// 变化按链条覆盖的时间跨度加载（相关事件可能落在窗口外）。
	spanFrom, spanTo := ordered[0].Identity.Timestamp, ordered[len(ordered)-1].Identity.Timestamp
	rows, err := src.QueryStateChanges(ctx, store.StateChangeQuery{
		SessionID: q.SessionID,
		From:      spanFrom,
		To:        spanTo,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	byEvent := make(map[string][]Change)
	for _, r := range rows {
		c, err := buildChangeFromEvent(r, byID)
		if err != nil {
			return nil, err
		}
		c.OffsetMS = c.Timestamp.Sub(t0).Milliseconds()
		byEvent[c.EventID] = append(byEvent[c.EventID], c)
	}

	chain := &Chain{EventCount: len(ordered)}
	for _, ev := range ordered {
		ref := newMessageRef(ev, ev.Identity.Timestamp.Sub(t0).Milliseconds())
		step := ChainStep{Message: ref, OffsetMS: ref.OffsetMS}
		changes := byEvent[ref.EventID]
		step.ChangeCount = len(changes)
		step.Entities = groupChainEntities(changes)
		chain.Steps = append(chain.Steps, step)
	}
	// 链条起点：请求优先，其次最早一条。
	op := chain.Steps[0].Message
	for _, s := range chain.Steps {
		if s.Message.Kind == "request" {
			op = s.Message
			break
		}
	}
	chain.Operation = op
	return chain, nil
}

func buildChangeFromEvent(row store.StateChangeRow, events map[string]*event.Event) (Change, error) {
	ref := MessageRef{EventID: row.EventID, Timestamp: row.Timestamp, Kind: "unknown", OperationKey: "evt:" + row.EventID}
	if ev, ok := events[row.EventID]; ok && ev != nil {
		ref = newMessageRef(ev, 0)
	}
	if ref.FlowID == "" {
		ref.FlowID = row.FlowID
	}
	if ref.Timestamp.IsZero() {
		ref.Timestamp = row.Timestamp
	}
	return Change{
		ID:          row.ID,
		EventID:     row.EventID,
		SessionID:   row.SessionID,
		FlowID:      row.FlowID,
		Timestamp:   row.Timestamp,
		SubjectType: row.SubjectType,
		SubjectID:   row.SubjectID,
		EntityKey:   EntityKeyOf(row.SubjectType, row.SubjectID),
		Op:          row.Op,
		Path:        row.Path,
		Before:      rawJSON(row.Before),
		After:       rawJSON(row.After),
		Version:     row.Version,
		Metadata:    rawJSON(row.Metadata),
		Source:      ref,
	}, nil
}

func groupChainEntities(changes []Change) []ChainEntity {
	var out []ChainEntity
	idx := make(map[string]int)
	for _, c := range changes {
		i, ok := idx[c.EntityKey]
		if !ok {
			out = append(out, ChainEntity{Key: c.EntityKey, SubjectType: c.SubjectType, SubjectID: c.SubjectID})
			i = len(out) - 1
			idx[c.EntityKey] = i
		}
		out[i].ChangeCount++
		out[i].FieldPaths = appendUnique(out[i].FieldPaths, c.Path)
		out[i].Changes = append(out[i].Changes, c)
	}
	return out
}

// loadEntityHistory 加载某实体的完整变化历史并按字段组织。
func loadEntityHistory(ctx context.Context, src DataSource, q DetailQuery, subjectType, subjectID string, t0 time.Time, limit int) (*EntityHistory, error) {
	rows, err := src.QueryStateChanges(ctx, store.StateChangeQuery{
		SessionID:   q.SessionID,
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Path:        "",
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	hist := &EntityHistory{
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Key:         EntityKeyOf(subjectType, subjectID),
	}
	fieldIdx := make(map[string]int)
	timeline := make([]Change, 0, len(rows))
	for _, r := range rows {
		// 历史里可以叠加字段过滤：path 过滤在内存做，避免为了一个字段多查一次库。
		if q.Path != "" && r.Path != q.Path {
			continue
		}
		c, err := buildChangeFromEvent(r, nil)
		if err != nil {
			return nil, err
		}
		if !t0.IsZero() {
			c.OffsetMS = c.Timestamp.Sub(t0).Milliseconds()
		}
		timeline = append(timeline, c)
		i, ok := fieldIdx[c.Path]
		if !ok {
			hist.Fields = append(hist.Fields, FieldHistory{Path: c.Path})
			i = len(hist.Fields) - 1
			fieldIdx[c.Path] = i
		}
		f := &hist.Fields[i]
		f.ChangeCount++
		f.Ops = appendUnique(f.Ops, c.Op)
		f.Changes = append(f.Changes, c)
		if f.FirstChange.IsZero() {
			f.FirstChange = c.Timestamp
			f.FirstValue = c.Before
		}
		f.LastChange = c.Timestamp
		f.LastValue = c.After
	}
	if len(timeline) == 0 {
		return nil, nil
	}
	sort.SliceStable(timeline, func(i, j int) bool {
		if timeline[i].Timestamp.Equal(timeline[j].Timestamp) {
			return timeline[i].ID < timeline[j].ID
		}
		return timeline[i].Timestamp.Before(timeline[j].Timestamp)
	})
	for i := range timeline {
		timeline[i].Seq = i + 1
	}
	hist.Timeline = timeline
	hist.ChangeCount = len(timeline)
	hist.FirstChange = timeline[0].Timestamp
	hist.LastChange = timeline[len(timeline)-1].Timestamp
	sort.SliceStable(hist.Fields, func(i, j int) bool { return hist.Fields[i].Path < hist.Fields[j].Path })
	return hist, nil
}
