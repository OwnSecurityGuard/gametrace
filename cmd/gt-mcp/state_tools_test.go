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

// TestStateTools 是 query_state_changes / get_state_change_detail 的端到端用例：
// 写一次「升级建筑」操作的事件与状态变更，然后从 MCP handler 层验证
// 窗口过滤、三视图分组、相对时间与完整历史是否都对得上。
func TestStateTools(t *testing.T) {
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

	st, err := store.NewSQLiteStore(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	writeUpgradeFixture(t, st, sessionID, base)
	st.Close()

	if err := sessionMgr.writeCurrent(sessionMetadata{
		SessionID: sessionID,
		StartedAt: base.Format(time.RFC3339),
		Status:    "stopped",
		DBPath:    dbPath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID,
		StartedAt: base.Format(time.RFC3339),
		Status:    "stopped",
		DBPath:    dbPath,
	}); err != nil {
		t.Fatal(err)
	}

	// ---- 1. 全会话查询：三视图共用同一份 changes ----
	full := callStateTool(t, m, ctx, map[string]any{
		"session_id": sessionID,
		"group_by":   "operation",
	})
	if got := full["summary"].(map[string]any)["change_count"].(float64); got != 4 {
		t.Fatalf("change_count = %v, want 4", got)
	}
	if got := full["summary"].(map[string]any)["entity_count"].(float64); got != 2 {
		t.Fatalf("entity_count = %v, want 2", got)
	}
	ops := full["operations"].([]any)
	if len(ops) != 2 {
		t.Fatalf("operations = %d, want 2 (升级操作 + 未配对的 Heartbeat)", len(ops))
	}
	op := ops[0].(map[string]any)
	if op["label"] != "UpgradeReq" {
		t.Fatalf("operation label = %v, want UpgradeReq", op["label"])
	}
	if got := op["change_count"].(float64); got != 3 {
		t.Fatalf("operation change_count = %v, want 3", got)
	}
	// 协议连含请求本身（请求不产生变更，但用户要看发出去的协议）。
	chain := op["chain"].([]any)
	if len(chain) != 3 {
		t.Fatalf("chain length = %d, want 3", len(chain))
	}
	ents := full["entities"].([]any)
	if len(ents) != 2 {
		t.Fatalf("entities = %d, want 2", len(ents))
	}
	buckets := full["buckets"].([]any)
	if len(buckets) == 0 {
		t.Fatal("time buckets are empty")
	}

	// ---- 2. 操作锚点 + 1s 窗口：Heartbeat 那条 T+5s 的变更应被排除 ----
	win := callStateTool(t, m, ctx, map[string]any{
		"session_id":      sessionID,
		"anchor_type":     "operation",
		"anchor_id":       op["key"].(string),
		"window_after_ms": 1000,
	})
	if got := win["summary"].(map[string]any)["change_count"].(float64); got != 3 {
		t.Fatalf("windowed change_count = %v, want 3 (T+5s 的无关变更已排除)", got)
	}
	anchor := win["anchor"].(map[string]any)
	if anchor["resolved"] != true {
		t.Fatalf("anchor not resolved: %+v", anchor)
	}
	// 相对时间以锚点为原点：响应带来的变更是 T+200ms。
	changes := win["changes"].([]any)
	if got := changes[0].(map[string]any)["offset_ms"].(float64); got != 200 {
		t.Fatalf("first change offset = %v, want 200", got)
	}

	// ---- 3. 实体锚点 + 字段过滤 ----
	ent := callStateTool(t, m, ctx, map[string]any{
		"session_id":   sessionID,
		"anchor_type":  "entity",
		"anchor_id":    "Building:1001",
		"subject_type": "Building",
		"paths":        []any{"level"},
	})
	// Building:1001 的 level 在 resp 与 push 里各变一次：1→2→3。
	if got := ent["summary"].(map[string]any)["change_count"].(float64); got != 2 {
		t.Fatalf("entity+path filtered change_count = %v, want 2", got)
	}

	// ---- 4. 详情：协议链 + 完整历史 ----
	firstChangeID := changes[0].(map[string]any)["id"].(string)
	detail := callDetailTool(t, m, ctx, map[string]any{
		"session_id": sessionID,
		"change_id":  firstChangeID,
	})
	if detail["change"].(map[string]any)["id"] != firstChangeID {
		t.Fatalf("detail change id mismatch: %v", detail["change"])
	}
	chainObj := detail["chain"].(map[string]any)
	if chainObj["operation"].(map[string]any)["msg_name"] != "UpgradeReq" {
		t.Fatalf("chain operation = %v", chainObj["operation"])
	}
	steps := chainObj["steps"].([]any)
	if len(steps) != 3 {
		t.Fatalf("chain steps = %d, want 3", len(steps))
	}
	hist := detail["history"].(map[string]any)
	if hist["key"] != "Building:1001" {
		t.Fatalf("history key = %v, want Building:1001", hist["key"])
	}
	// 实体完整历史不受窗口限制：这里两条（resp + push）。
	if got := hist["change_count"].(float64); got != 2 {
		t.Fatalf("history change_count = %v, want 2", got)
	}
	fh := detail["field_history"].(map[string]any)
	if fh["path"] != "level" {
		t.Fatalf("field history path = %v", fh["path"])
	}
}

// writeUpgradeFixture 写入一次升级操作的事件与状态变更。
//
//	req(UpgradeReq, T+0) → resp(UpgradeResp, T+200ms) → push(BuildingUpdate, T+250ms)
//	Building:1001.level 1→2（resp）、2→3（push）；Player:7.gold 100→90（push）
//	另有 Heartbeat 推送在 T+5s 改 Player:7.gold，用于验证窗口过滤。
func writeUpgradeFixture(t *testing.T, st *store.SQLiteStore, sessionID string, base time.Time) {
	t.Helper()
	mkEvent := func(id, msgName, direction string, offsetMS int64, correlation, causation string) *event.Event {
		ev := event.NewEventWithTime(sessionID, event.EventType(msgName), "schema.test", "test",
			event.ValueFromAny(map[string]any{
				"_meta": map[string]any{"msg_name": msgName, "direction": direction},
			}), base.Add(time.Duration(offsetMS)*time.Millisecond),
			event.EventContext{FlowID: "flow-1", Direction: direction})
		ev.Identity.ID = event.EventID(id)
		ev.Trace = event.TraceContext{CorrelationID: correlation, CausationID: event.EventID(causation)}
		return ev
	}
	evs := []*event.Event{
		mkEvent("e1", "UpgradeReq", "client_to_server", 0, "corr-1", ""),
		mkEvent("e2", "UpgradeResp", "server_to_client", 200, "corr-1", "e1"),
		mkEvent("e3", "BuildingUpdate", "server_to_client", 250, "corr-1", ""),
		mkEvent("e4", "Heartbeat", "server_to_client", 5000, "", ""),
	}
	if err := st.AppendEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
	mkChange := func(id, eventID string, offsetMS int64, stype, sid, path string, before, after int) store.EnrichedStateChange {
		return store.EnrichedStateChange{
			StateChange: event.StateChange{
				SubjectType: stype, SubjectID: sid, Op: "set", Path: path,
				Before: event.ValueFromAny(before), After: event.ValueFromAny(after), Version: 1,
			},
			EventID:       event.EventID(eventID),
			FlowID:        "flow-1",
			Timestamp:     base.Add(time.Duration(offsetMS) * time.Millisecond),
			EntityVersion: 1,
			AfterResolved: true,
		}
	}
	changes := []store.EnrichedStateChange{
		mkChange("c1", "e2", 200, "Building", "1001", "level", 1, 2),
		mkChange("c2", "e3", 250, "Building", "1001", "level", 2, 3),
		mkChange("c3", "e3", 250, "Player", "7", "gold", 100, 90),
		mkChange("c4", "e4", 5000, "Player", "7", "gold", 90, 95),
	}
	if err := st.WriteEnrichedStateChanges(context.Background(), sessionID, changes); err != nil {
		t.Fatal(err)
	}
}

func callStateTool(t *testing.T, m *mcpCapture, ctx context.Context, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := m.handleQueryStateChanges(ctx, req)
	if err != nil {
		t.Fatalf("query_state_changes: %v", err)
	}
	return decodeToolJSON(t, contentText(res))
}

func callDetailTool(t *testing.T, m *mcpCapture, ctx context.Context, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := m.handleGetStateChangeDetail(ctx, req)
	if err != nil {
		t.Fatalf("get_state_change_detail: %v", err)
	}
	return decodeToolJSON(t, contentText(res))
}

func decodeToolJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if out["ok"] != true {
		t.Fatalf("tool returned not ok: %s", text)
	}
	return out
}

// TestStateToolsDefaultSession 验证省略 session_id 时回退到当前会话。
func TestStateToolsDefaultSession(t *testing.T) {
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
	st, err := store.NewSQLiteStore(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeUpgradeFixture(t, st, sessionID, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))
	st.Close()
	if err := sessionMgr.writeCurrent(sessionMetadata{
		SessionID: sessionID, StartedAt: time.Now().Format(time.RFC3339), Status: "stopped", DBPath: dbPath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID, StartedAt: time.Now().Format(time.RFC3339), Status: "stopped", DBPath: dbPath,
	}); err != nil {
		t.Fatal(err)
	}

	res := callStateTool(t, m, ctx, map[string]any{})
	if res["session_id"] != sessionID {
		t.Fatalf("session_id = %v, want %s", res["session_id"], sessionID)
	}
	if got := res["summary"].(map[string]any)["change_count"].(float64); got != 4 {
		t.Fatalf("change_count = %v, want 4", got)
	}
}
