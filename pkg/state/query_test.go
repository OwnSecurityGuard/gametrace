package state

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// ===== 内存 DataSource（测试替身） =====

type fakeSource struct {
	events  []*event.Event
	changes []store.StateChangeRow
}

func (f *fakeSource) GetEventByID(ctx context.Context, id string) (*event.Event, error) {
	for _, e := range f.events {
		if string(e.Identity.ID) == id {
			return e, nil
		}
	}
	return nil, nil
}

func (f *fakeSource) QueryEventsInRange(ctx context.Context, sessionID string, from, to time.Time, limit int) ([]*event.Event, error) {
	var out []*event.Event
	for _, e := range f.events {
		if e.Identity.SessionID != sessionID {
			continue
		}
		if !from.IsZero() && e.Identity.Timestamp.Before(from) {
			continue
		}
		if !to.IsZero() && e.Identity.Timestamp.After(to) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Timestamp.Before(out[j].Identity.Timestamp) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeSource) QueryEventsByCorrelation(ctx context.Context, correlationID string, limit, offset int) ([]*event.Event, error) {
	var out []*event.Event
	for _, e := range f.events {
		if e.Trace.CorrelationID == correlationID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeSource) QueryStateChanges(ctx context.Context, q store.StateChangeQuery) ([]store.StateChangeRow, error) {
	out := make([]store.StateChangeRow, 0, len(f.changes))
	for _, r := range f.changes {
		if q.ID != "" && r.ID != q.ID {
			continue
		}
		if q.SessionID != "" && r.SessionID != q.SessionID {
			continue
		}
		if q.EventID != "" && r.EventID != q.EventID {
			continue
		}
		if q.SubjectType != "" && r.SubjectType != q.SubjectType {
			continue
		}
		if q.SubjectID != "" && r.SubjectID != q.SubjectID {
			continue
		}
		if q.Op != "" && r.Op != q.Op {
			continue
		}
		if q.Path != "" && r.Path != q.Path {
			continue
		}
		if !q.From.IsZero() && r.Timestamp.Before(q.From) {
			continue
		}
		if !q.To.IsZero() && r.Timestamp.After(q.To) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].ID < out[j].ID
		}
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	if limit := q.Limit; limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ===== 夹具 =====

var (
	baseTime = time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	sessID   = "sess-1"
)

func makeEvent(id, msgName, direction string, offsetMS int64, correlation, causation string) *event.Event {
	payload := event.ValueFromAny(map[string]any{
		"_meta": map[string]any{
			"msg_name":  msgName,
			"direction": direction,
		},
		"level": 1,
	})
	ev := event.NewEventWithTime(sessID, event.EventType(msgName), "test", payload,
		baseTime.Add(time.Duration(offsetMS)*time.Millisecond),
		event.EventContext{FlowID: "flow-1", Direction: direction})
	ev.Identity.ID = event.EventID(id)
	ev.Trace = event.TraceContext{CorrelationID: correlation, CausationID: event.EventID(causation)}
	return ev
}

func makeChange(id, eventID string, offsetMS int64, subjectType, subjectID, path string, before, after int) store.StateChangeRow {
	return store.StateChangeRow{
		ID:          id,
		EventID:     eventID,
		SessionID:   sessID,
		FlowID:      "flow-1",
		Timestamp:   baseTime.Add(time.Duration(offsetMS) * time.Millisecond),
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Op:          "set",
		Path:        path,
		Before:      itoa(before),
		After:       itoa(after),
		Version:     int64(1),
	}
}

func itoa(v int) string { b, _ := json.Marshal(v); return string(b) }

// newFixture 构造「一次升级操作」：
//
//	req(UpgradeReq, T+0) → resp(UpgradeResp, T+200ms) → push(BuildingUpdate, T+250ms)
//	                                                  ↘ Player:7 gold 变化
//	另有一条无关的 Heartbeat 推送（T+5000ms），用于验证窗口过滤。
func newFixture() *fakeSource {
	return &fakeSource{
		events: []*event.Event{
			makeEvent("e1", "UpgradeReq", "client_to_server", 0, "corr-1", ""),
			makeEvent("e2", "UpgradeResp", "server_to_client", 200, "corr-1", "e1"),
			makeEvent("e3", "BuildingUpdate", "server_to_client", 250, "corr-1", ""),
			makeEvent("e4", "Heartbeat", "server_to_client", 5000, "", ""),
		},
		changes: []store.StateChangeRow{
			makeChange("c1", "e2", 200, "Building", "1001", "level", 1, 2),
			makeChange("c2", "e3", 250, "Building", "1001", "level", 2, 3),
			makeChange("c3", "e3", 250, "Player", "7", "gold", 100, 90),
			makeChange("c4", "e4", 5000, "Player", "7", "gold", 90, 95),
		},
	}
}

// ===== 查询引擎 =====

func TestRunFullSession(t *testing.T) {
	rs, err := Run(context.Background(), newFixture(), Query{SessionID: sessID})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rs.Summary.ChangeCount != 4 {
		t.Fatalf("change count = %d, want 4", rs.Summary.ChangeCount)
	}
	if rs.Summary.EntityCount != 2 {
		t.Fatalf("entity count = %d, want 2", rs.Summary.EntityCount)
	}
	// 无锚点时 T0 退化为首条变更，offset 从 0 开始。
	if got := rs.Changes[0].OffsetMS; got != 0 {
		t.Fatalf("first offset = %d, want 0", got)
	}
	if got := rs.Changes[3].OffsetMS; got != 4800 {
		t.Fatalf("last offset = %d, want 4800", got)
	}
	// 按操作：corr-1 三条归一组，Heartbeat 未配对自成一团。
	if len(rs.Operations) != 2 {
		t.Fatalf("operations = %d, want 2", len(rs.Operations))
	}
	op := rs.Operations[0]
	if op.Key != "corr:corr-1" {
		t.Fatalf("first operation key = %q, want corr:corr-1", op.Key)
	}
	if op.ChangeCount != 3 || op.EntityCount != 2 {
		t.Fatalf("operation stats = %d changes / %d entities, want 3 / 2", op.ChangeCount, op.EntityCount)
	}
	// 操作标签取请求名，不是第一条变更所在的响应。
	if op.Label != "UpgradeReq" || op.Kind != "request" {
		t.Fatalf("operation label = %q (%s), want UpgradeReq (request)", op.Label, op.Kind)
	}
	// 协议连按时间排：req → resp → push。
	wantChain := []string{"e1", "e2", "e3"}
	if len(op.Chain) != len(wantChain) {
		t.Fatalf("chain len = %d, want %d", len(op.Chain), len(wantChain))
	}
	for i, id := range wantChain {
		if op.Chain[i].EventID != id {
			t.Fatalf("chain[%d] = %s, want %s", i, op.Chain[i].EventID, id)
		}
	}
	// 按实体：Building 变 2 次 1 个字段，Player 变 2 次。
	byKey := map[string]EntityGroup{}
	for _, e := range rs.Entities {
		byKey[e.Key] = e
	}
	b := byKey["Building:1001"]
	if b.ChangeCount != 2 || b.FieldCount != 1 || b.FieldPaths[0] != "level" {
		t.Fatalf("Building group = %+v", b)
	}
	if len(rs.Buckets) == 0 {
		t.Fatal("time buckets are empty")
	}
}

func TestRunOperationAnchorWindow(t *testing.T) {
	// 锚在请求上、窗口 1s：只看这次操作之后 1 秒内发生了什么。
	rs, err := Run(context.Background(), newFixture(), Query{
		SessionID: sessID,
		Anchor:    Anchor{Kind: AnchorOperation, ID: "e1"},
		AfterMS:   1000,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rs.Anchor.Resolved {
		t.Fatal("anchor should be resolved")
	}
	if rs.Summary.ChangeCount != 3 {
		t.Fatalf("change count = %d, want 3 (Heartbeat at T+5000ms 落在窗口外)", rs.Summary.ChangeCount)
	}
	if rs.Summary.EntityCount != 2 {
		t.Fatalf("entity count = %d, want 2", rs.Summary.EntityCount)
	}
	// 相对时间以锚点为原点：响应在 T+200ms。
	if got := rs.Changes[0].OffsetMS; got != 200 {
		t.Fatalf("first change offset = %d, want 200", got)
	}
	if len(rs.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(rs.Operations))
	}
}

func TestRunEntityAnchorFullHistory(t *testing.T) {
	rs, err := Run(context.Background(), newFixture(), Query{
		SessionID: sessID,
		Anchor:    Anchor{Kind: AnchorEntity, ID: "Building:1001"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rs.Summary.ChangeCount != 2 {
		t.Fatalf("change count = %d, want 2 (实体锚点带实体约束)", rs.Summary.ChangeCount)
	}
	if len(rs.Entities) != 1 || rs.Entities[0].Key != "Building:1001" {
		t.Fatalf("entities = %+v", rs.Entities)
	}
	// 每个实体变了几次、涉及哪些字段。
	e := rs.Entities[0]
	if e.ChangeCount != 2 || e.FieldCount != 1 {
		t.Fatalf("entity = %+v", e)
	}
	// resp 与 push 同属 corr-1，算一次操作；操作内要能看出是哪两条消息改的。
	if len(e.Operations) != 1 {
		t.Fatalf("entity operations = %d, want 1", len(e.Operations))
	}
	if got := len(e.Operations[0].Events); got != 2 {
		t.Fatalf("operation hit events = %d, want 2 (resp + push)", got)
	}
}

func TestRunSortByChangeCountAndFilter(t *testing.T) {
	rs, err := Run(context.Background(), newFixture(), Query{
		SessionID: sessID,
		SortBy:    SortByChangeCount,
		Filter:    Filter{SubjectTypes: []string{"Player"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rs.Summary.ChangeCount != 2 {
		t.Fatalf("filtered change count = %d, want 2", rs.Summary.ChangeCount)
	}
	if len(rs.Entities) != 1 || rs.Entities[0].SubjectType != "Player" {
		t.Fatalf("filtered entities = %+v", rs.Entities)
	}
	// 过滤后 change_count 降序仍然稳定（只有一组，这里只验不崩）。
	if rs.Entities[0].ChangeCount != 2 {
		t.Fatalf("player change count = %d, want 2", rs.Entities[0].ChangeCount)
	}
}

func TestRunPathPrefixFilter(t *testing.T) {
	src := newFixture()
	src.changes = append(src.changes, makeChange("c5", "e3", 251, "Building", "1001", "cost.gold", 10, 20))
	rs, err := Run(context.Background(), src, Query{
		SessionID: sessID,
		Filter:    Filter{Paths: []string{"cost"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rs.Summary.ChangeCount != 1 {
		t.Fatalf("path filter matched %d changes, want 1", rs.Summary.ChangeCount)
	}
}

func TestRunMissingSession(t *testing.T) {
	if _, err := Run(context.Background(), newFixture(), Query{}); err == nil {
		t.Fatal("empty session_id should error")
	}
}

func TestAutoBucketMS(t *testing.T) {
	cases := []struct{ span, want int64 }{
		{0, 1},
		{200, 10},
		{20_000, 1000},
		{3_600_000, 300_000},
	}
	for _, c := range cases {
		if got := autoBucketMS(0, c.span); got != c.want {
			t.Fatalf("autoBucketMS(span=%d) = %d, want %d", c.span, got, c.want)
		}
	}
}

func TestFloorToNegative(t *testing.T) {
	if got := floorTo(-250, 100); got != -300 {
		t.Fatalf("floorTo(-250,100) = %d, want -300", got)
	}
	if got := floorTo(250, 100); got != 200 {
		t.Fatalf("floorTo(250,100) = %d, want 200", got)
	}
}

// ===== 详情 =====

func TestLoadDetailByChangeID(t *testing.T) {
	d, err := LoadDetail(context.Background(), newFixture(), DetailQuery{SessionID: sessID, ChangeID: "c2"})
	if err != nil {
		t.Fatalf("LoadDetail: %v", err)
	}
	if d.Change == nil || d.Change.ID != "c2" {
		t.Fatalf("change = %+v", d.Change)
	}
	if d.Chain == nil {
		t.Fatal("chain is nil")
	}
	// 协议链起点是请求，且包含 req → resp → push 三步。
	if d.Chain.Operation.MsgName != "UpgradeReq" {
		t.Fatalf("chain operation = %q, want UpgradeReq", d.Chain.Operation.MsgName)
	}
	if len(d.Chain.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(d.Chain.Steps))
	}
	// 链条每一步挂着它自己造成的字段变化。
	if d.Chain.Steps[2].ChangeCount != 2 {
		t.Fatalf("push step change count = %d, want 2", d.Chain.Steps[2].ChangeCount)
	}
	// 实体完整历史：跨事件累计，不只是窗口内。
	if d.History == nil || d.History.Key != "Building:1001" {
		t.Fatalf("history = %+v", d.History)
	}
	if d.History.ChangeCount != 2 {
		t.Fatalf("history change count = %d, want 2", d.History.ChangeCount)
	}
	if len(d.History.Fields) != 1 || d.History.Fields[0].Path != "level" {
		t.Fatalf("history fields = %+v", d.History.Fields)
	}
	f := d.History.Fields[0]
	if string(f.FirstValue) != "1" || string(f.LastValue) != "3" {
		t.Fatalf("field history values = %s → %s, want 1 → 3", f.FirstValue, f.LastValue)
	}
	if d.FieldHistory == nil || d.FieldHistory.Path != "level" {
		t.Fatalf("field history = %+v", d.FieldHistory)
	}
}

func TestLoadDetailRequiresLocator(t *testing.T) {
	if _, err := LoadDetail(context.Background(), newFixture(), DetailQuery{SessionID: sessID}); err == nil {
		t.Fatal("missing locator should error")
	}
	if _, err := LoadDetail(context.Background(), newFixture(), DetailQuery{SessionID: sessID, Entity: "Building"}); err == nil {
		t.Fatal("malformed entity should error")
	}
}
