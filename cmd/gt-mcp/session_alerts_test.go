package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

// TestListSessionAlerts 验证「命中提醒」查询面：落库的命中记录能按会话读回，
// trigger/context 以原样 JSON 直通（前端据此渲染触发记录与前后上下文）。
func TestListSessionAlerts(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	sessionMgr := newSessionManager(workDir)
	m := &mcpCapture{
		workDir:      workDir,
		sessionMgr:   sessionMgr,
		authz:        newProjectAuthorizer(nil),
		readerOpener: sqliteReaderOpener(),
	}
	sessionID := sessionMgr.generateSessionID()
	if err := os.MkdirAll(sessionMgr.sessionDir(sessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := sessionMgr.absDBPath(sessionID)

	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	base := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	// 事件表里放触发记录本身 + 其后三条，用于验证 after 补齐（同纳秒锚点要按 id 去重）。
	writeAlertEventFixture(t, st, sessionID, []string{"ev2", "ev3", "ev4", "ev5"}, base.Add(time.Minute))
	rows := []store.AlertRow{
		{AlertID: "al-old", RuleID: "r1", RuleName: "血量过低", Title: "T1", Message: "M1",
			Timestamp:   base,
			TriggerJSON: `{"id":"ev1","timestamp":"2026-09-26T09:00:00.000000Z","direction":"response","data":{"hp":0}}`},
		{AlertID: "al-new", RuleID: "r2", RuleName: "频率异常", Title: "T2", Message: "M2",
			Timestamp:   base.Add(time.Minute),
			TriggerJSON: `{"id":"ev2"}`, ContextJSON: `{"request":[{"id":"ev1"}]}`},
	}
	for _, r := range rows {
		if err := st.AppendAlert(ctx, sessionID, r); err != nil {
			t.Fatalf("AppendAlert: %v", err)
		}
	}
	st.Close()

	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID,
		StartedAt: base.Format(time.RFC3339),
		Status:    "stopped",
		DBPath:    dbPath,
	}); err != nil {
		t.Fatal(err)
	}

	out := callAlertsTool(t, m, ctx, map[string]any{"session_id": sessionID, "limit": 10, "after": 2})
	if got := out["count"].(float64); got != 2 {
		t.Fatalf("count = %v, want 2", got)
	}
	alerts := out["alerts"].([]any)
	if len(alerts) != 2 {
		t.Fatalf("alerts = %d, want 2", len(alerts))
	}
	first := alerts[0].(map[string]any)
	if first["alert_id"] != "al-new" {
		t.Errorf("latest row = %v, want al-new", first)
	}
	// formatEventTime 按本地时区渲染，比较时刻而不是字符串。
	if got, err := time.Parse(time.RFC3339Nano, first["timestamp"].(string)); err != nil || !got.Equal(base.Add(time.Minute)) {
		t.Errorf("latest timestamp = %v, want %v (err %v)", first["timestamp"], base.Add(time.Minute), err)
	}
	// context 缺省时字段不出现（前端按存在与否决定渲染上下文区）。
	if _, ok := first["context"]; !ok {
		t.Errorf("al-new context missing: %v", first)
	}
	second := alerts[1].(map[string]any)
	if _, ok := second["context"]; ok {
		t.Errorf("al-old should have no context key: %v", second)
	}
	var trig map[string]any
	raw, _ := json.Marshal(second["trigger"])
	if err := json.Unmarshal(raw, &trig); err != nil || trig["id"] != "ev1" {
		t.Errorf("al-old trigger = %v err=%v", second["trigger"], err)
	}

	// after=N：按触发时刻现查「其后 N 条」，触发记录自身（同纳秒锚点）不重复出现。
	if ids := afterIDs(first["after"]); len(ids) != 2 || ids[0] != "ev3" || ids[1] != "ev4" {
		t.Errorf("al-new after = %v, want [ev3 ev4]", ids)
	}
	if ids := afterIDs(second["after"]); len(ids) != 2 || ids[0] != "ev2" || ids[1] != "ev3" {
		t.Errorf("al-old after = %v, want [ev2 ev3]", ids)
	}

	// 不传 after：不现查后续上下文（默认省一次查询）。
	bare := callAlertsTool(t, m, ctx, map[string]any{"session_id": sessionID, "limit": 10})
	if _, ok := bare["alerts"].([]any)[0].(map[string]any)["after"]; ok {
		t.Errorf("after should be absent without the parameter: %v", bare["alerts"])
	}

	// alert_id 下钻：只回那一条命中（展开详情时的查询形态）。
	one := callAlertsTool(t, m, ctx, map[string]any{"session_id": sessionID, "alert_id": "al-old", "after": 2})
	oneRows := one["alerts"].([]any)
	if got := one["count"].(float64); got != 1 {
		t.Fatalf("count with alert_id = %v, want 1", got)
	}
	if len(oneRows) != 1 || oneRows[0].(map[string]any)["alert_id"] != "al-old" {
		t.Fatalf("rows with alert_id = %v, want [al-old]", oneRows)
	}
}

// afterIDs 把 after 字段（[]checkrule.AlertRecord 的 JSON）取成 id 列表。
func afterIDs(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m["id"].(string))
		}
	}
	return out
}

// writeAlertEventFixture 按 ids 顺序写入间隔 10s 的解码事件（第一条即锚点时刻）。
func writeAlertEventFixture(t *testing.T, st *store.SQLiteStore, sessionID string, ids []string, at time.Time) {
	t.Helper()
	evs := make([]*event.Event, 0, len(ids))
	for i, id := range ids {
		ev := event.NewEventWithTime(sessionID, event.EventType("game."+id), "test",
			event.ValueFromAny(map[string]any{
				"_meta": map[string]any{"msg_name": id, "direction": "server_to_client"},
				"hp":    i,
			}), at.Add(time.Duration(i*10)*time.Second),
			event.EventContext{Direction: "server_to_client"})
		ev.Identity.ID = event.EventID(id)
		evs = append(evs, ev)
	}
	if err := st.AppendEvents(context.Background(), evs); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
}

// TestListSessionAlertsPagination 验证 offset 翻页时 count 仍是会话内命中总数。
func TestListSessionAlertsPagination(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	sessionMgr := newSessionManager(workDir)
	m := &mcpCapture{
		workDir:      workDir,
		sessionMgr:   sessionMgr,
		authz:        newProjectAuthorizer(nil),
		readerOpener: sqliteReaderOpener(),
	}
	sessionID := sessionMgr.generateSessionID()
	if err := os.MkdirAll(sessionMgr.sessionDir(sessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := sessionMgr.absDBPath(sessionID)

	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	base := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := st.AppendAlert(ctx, sessionID, store.AlertRow{
			AlertID: "al" + string(rune('A'+i)), RuleID: "r1",
			Timestamp: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("AppendAlert: %v", err)
		}
	}
	st.Close()
	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID, Status: "stopped", DBPath: dbPath,
	}); err != nil {
		t.Fatal(err)
	}

	out := callAlertsTool(t, m, ctx, map[string]any{"session_id": sessionID, "limit": 1, "offset": 1})
	if got := out["count"].(float64); got != 3 {
		t.Fatalf("count = %v, want 3", got)
	}
	alerts := out["alerts"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["alert_id"] != "alB" {
		t.Fatalf("page = %v, want [alB]", alerts)
	}
}

func callAlertsTool(t *testing.T, m *mcpCapture, ctx context.Context, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := m.handleListSessionAlerts(ctx, req)
	if err != nil {
		t.Fatalf("list_session_alerts: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_session_alerts returned error: %s", contentText(res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(contentText(res)), &out); err != nil {
		t.Fatalf("unmarshal list_session_alerts response: %v", err)
	}
	return out
}
