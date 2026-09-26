package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAlertStore_AppendAndQuery(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	base := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	// 倒序写入，验证读出仍按时间倒序（最新在前）。
	rows := []AlertRow{
		{AlertID: "a3", RuleID: "r1", RuleName: "血量过低", Title: "T3", Message: "M3",
			Timestamp: base.Add(3 * time.Minute), GeneratedAt: "2026-09-26T08:03:00.000000Z",
			TriggerJSON: `{"id":"ev3","timestamp":"2026-09-26T08:03:00.000000Z","type":"packet","direction":"server_to_client","data":{"hp":0}}`},
		{AlertID: "a1", RuleID: "r1", RuleName: "血量过低", Title: "T1", Message: "M1",
			Timestamp: base, GeneratedAt: "2026-09-26T08:00:00.000000Z",
			TriggerJSON: `{"id":"ev1"}`, ContextJSON: `{"request":[{"id":"ev0"}]}`},
		{AlertID: "a2", RuleID: "r2", RuleName: "频率异常", Title: "T2", Message: "M2",
			Timestamp: base.Add(time.Minute), GeneratedAt: "2026-09-26T08:01:00.000000Z",
			TriggerJSON: `{"id":"ev2"}`},
	}
	for _, r := range rows {
		if err := s.AppendAlert(ctx, "sess-1", r); err != nil {
			t.Fatalf("AppendAlert %s: %v", r.AlertID, err)
		}
	}

	got, total, err := s.QueryAlerts(ctx, AlertQuery{SessionID: "sess-1", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("QueryAlerts: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(got) != 3 || got[0].AlertID != "a3" || got[1].AlertID != "a2" || got[2].AlertID != "a1" {
		t.Fatalf("order = %+v, want a3/a2/a1", got)
	}
	if !got[0].Timestamp.Equal(base.Add(3 * time.Minute)) {
		t.Errorf("a3 timestamp = %v, want %v", got[0].Timestamp, base.Add(3*time.Minute))
	}
	if got[0].ContextJSON != "" {
		t.Errorf("a3 context_json = %q, want empty", got[0].ContextJSON)
	}
	if got[2].ContextJSON != `{"request":[{"id":"ev0"}]}` {
		t.Errorf("a1 context_json = %q", got[2].ContextJSON)
	}

	// 分页：总数恒为条件命中数，与当页条数无关。
	page, pageTotal, err := s.QueryAlerts(ctx, AlertQuery{SessionID: "sess-1", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("QueryAlerts page: %v", err)
	}
	if pageTotal != 3 || len(page) != 1 || page[0].AlertID != "a1" {
		t.Fatalf("page = total %d rows %+v, want total 3 / a1", pageTotal, page)
	}

	// 幂等：同 (session, alert_id) 重写覆盖而非追加。
	updated := rows[0]
	updated.Message = "M3-updated"
	if err := s.AppendAlert(ctx, "sess-1", updated); err != nil {
		t.Fatalf("AppendAlert replace: %v", err)
	}
	_, total2, err := s.QueryAlerts(ctx, AlertQuery{SessionID: "sess-1", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("QueryAlerts after replace: %v", err)
	}
	if total2 != 3 {
		t.Fatalf("total after replace = %d, want 3", total2)
	}
	latest, _, err := s.QueryAlerts(ctx, AlertQuery{SessionID: "sess-1", Limit: 1, Offset: 0})
	if err != nil {
		t.Fatalf("QueryAlerts latest: %v", err)
	}
	if len(latest) != 1 || latest[0].Message != "M3-updated" {
		t.Fatalf("latest = %+v, want M3-updated", latest)
	}

	// 单条下钻：AlertID 只取那一轮命中，total 也随条件收紧。
	one, oneTotal, err := s.QueryAlerts(ctx, AlertQuery{SessionID: "sess-1", AlertID: "a2", Limit: 10})
	if err != nil {
		t.Fatalf("QueryAlerts by id: %v", err)
	}
	if oneTotal != 1 || len(one) != 1 || one[0].AlertID != "a2" {
		t.Fatalf("by id = total %d rows %+v, want 1 / a2", oneTotal, one)
	}

	// 会话隔离：另一会话查不到。
	if _, otherTotal, err := s.QueryAlerts(ctx, AlertQuery{SessionID: "sess-2", Limit: 10, Offset: 0}); err != nil || otherTotal != 0 {
		t.Fatalf("sess-2 total = %d err = %v, want 0 nil", otherTotal, err)
	}
}
