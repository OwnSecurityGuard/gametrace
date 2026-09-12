package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gametrace/pkg/event"
)

// TestQueryEventsInRange 验证时间窗口查询与 state_changes 的时间/事件过滤下推。
// 状态变更分析依赖这两个条件做「操作后 N 秒」的窗口，SQL 层错了上层算得再对也没用。
func TestQueryEventsInRange(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	sess := "sess-range"
	var evs []*event.Event
	for i, off := range []int{0, 500, 1000, 3000} {
		ev := event.NewEventWithTime(sess, event.EventType("msg"), "test",
			event.ValueFromAny(map[string]any{"_meta": map[string]any{"msg_name": "M"}}),
			base.Add(time.Duration(off)*time.Millisecond), event.EventContext{FlowID: "f1"})
		ev.Identity.ID = event.EventID(string(rune('a'+i)) + "1")
		evs = append(evs, ev)
	}
	if err := s.AppendEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}

	// 闭区间 [500ms, 1000ms]：只包含中间两条。
	got, err := s.QueryEventsInRange(ctx, sess, base.Add(500*time.Millisecond), base.Add(1000*time.Millisecond), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("range query returned %d events, want 2", len(got))
	}
	// 只给下界：三条（500/1000/3000）。
	got, err = s.QueryEventsInRange(ctx, sess, base.Add(500*time.Millisecond), time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("open-ended query returned %d events, want 3", len(got))
	}
	// limit 生效且按时间升序。
	got, err = s.QueryEventsInRange(ctx, sess, time.Time{}, time.Time{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Identity.Timestamp.Before(got[1].Identity.Timestamp) {
		t.Fatalf("limit/order check failed: %d events", len(got))
	}
}

func TestQueryStateChangesWindowAndEventFilter(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	changes := []EnrichedStateChange{
		{
			StateChange: event.StateChange{SubjectType: "Building", SubjectID: "1", Op: "set", Path: "level", Version: 1},
			EventID:     "e1", FlowID: "f1", Timestamp: base, EntityVersion: 1,
		},
		{
			StateChange: event.StateChange{SubjectType: "Building", SubjectID: "1", Op: "set", Path: "level", Version: 2},
			EventID:     "e2", FlowID: "f1", Timestamp: base.Add(200 * time.Millisecond), EntityVersion: 2,
		},
		{
			StateChange: event.StateChange{SubjectType: "Player", SubjectID: "7", Op: "set", Path: "gold", Version: 1},
			EventID:     "e3", FlowID: "f1", Timestamp: base.Add(900 * time.Millisecond), EntityVersion: 1,
		},
	}
	if err := s.WriteEnrichedStateChanges(ctx, "sess-sc", changes); err != nil {
		t.Fatal(err)
	}

	// 窗口 [base+100ms, base+500ms]：只剩 e2 那条。
	rows, err := s.QueryStateChanges(ctx, StateChangeQuery{
		SessionID: "sess-sc",
		From:      base.Add(100 * time.Millisecond),
		To:        base.Add(500 * time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EventID != "e2" {
		t.Fatalf("window filter = %+v, want only e2", rows)
	}

	// 事件过滤。
	rows, err = s.QueryStateChanges(ctx, StateChangeQuery{SessionID: "sess-sc", EventID: "e3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SubjectID != "7" {
		t.Fatalf("event filter = %+v, want Player:7", rows)
	}

	// 主键精确查询（详情定位单条变更用）。
	all, err := s.QueryStateChanges(ctx, StateChangeQuery{SessionID: "sess-sc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("total rows = %d, want 3", len(all))
	}
	byID, err := s.QueryStateChanges(ctx, StateChangeQuery{ID: all[2].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byID) != 1 || byID[0].ID != all[2].ID {
		t.Fatalf("id filter = %+v, want %s", byID, all[2].ID)
	}
}
