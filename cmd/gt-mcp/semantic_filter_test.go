package main

import (
	"context"
	"os"
	"testing"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// TestListDecodedDataSemanticFilter 验证 list_decoded_data 的 semantic 过滤：
// annotate 标签（meta.semantic 数组成员）精确匹配；分页精确；与 conn_id 叠加；
// 空值时仍走纯 SQL 分页路径（回归，不破坏默认路径）。
func TestListDecodedDataSemanticFilter(t *testing.T) {
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
	writeSemanticFixture(t, st, sessionID)
	st.Close()

	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID,
		StartedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Status:    "stopped",
		DBPath:    dbPath,
	}); err != nil {
		t.Fatal(err)
	}

	// ---- 1. semantic=error：只返回 meta.semantic 含 error 的行 ----
	errs := callDecodedTool(t, m, ctx, map[string]any{"session_id": sessionID, "semantic": "error"})
	if got := errs["total_matched"].(float64); got != 3 {
		t.Fatalf("semantic=error total_matched = %v, want 3", got)
	}
	for _, r := range errs["events"].([]any) {
		row := r.(map[string]any)
		if !rowHasSemantic(row, "error") {
			t.Errorf("row %v lacks semantic=error (meta=%v)", row["id"], row["meta"])
		}
	}

	// ---- 2. semantic + offset/limit：分页精确（时间倒序：e6,e5,e2 → offset=1 取 e5,e2） ----
	page := callDecodedTool(t, m, ctx, map[string]any{
		"session_id": sessionID, "semantic": "error", "limit": 2, "offset": 1,
	})
	if got := page["total_matched"].(float64); got != 3 {
		t.Fatalf("paged total_matched = %v, want 3", got)
	}
	rows := page["events"].([]any)
	if len(rows) != 2 {
		t.Fatalf("paged rows = %d, want 2", len(rows))
	}
	want := map[string]bool{"e5": true, "e2": true}
	for _, r := range rows {
		delete(want, r.(map[string]any)["id"].(string))
	}
	if len(want) != 0 {
		t.Fatalf("paged rows = %v, want {e5, e2}", page["events"])
	}

	// ---- 3. semantic + conn_id 叠加（AND） ----
	conn := callDecodedTool(t, m, ctx, map[string]any{
		"session_id": sessionID, "semantic": "error", "conn_id": "conn-1",
	})
	if got := conn["total_matched"].(float64); got != 1 {
		t.Fatalf("semantic+conn total_matched = %v, want 1", got)
	}
	if id := conn["events"].([]any)[0].(map[string]any)["id"]; id != "e5" {
		t.Fatalf("semantic+conn row id = %v, want e5", id)
	}

	// ---- 4. semantic=response：多标签事件命中其一即算 ----
	resp := callDecodedTool(t, m, ctx, map[string]any{"session_id": sessionID, "semantic": "response"})
	if got := resp["total_matched"].(float64); got != 1 {
		t.Fatalf("semantic=response total_matched = %v, want 1", got)
	}

	// ---- 5. 未知标签：空结果而非错误 ----
	unknown := callDecodedTool(t, m, ctx, map[string]any{"session_id": sessionID, "semantic": "warning"})
	if got := unknown["total_matched"].(float64); got != 0 {
		t.Fatalf("semantic=warning total_matched = %v, want 0", got)
	}

	// ---- 6. semantic 为空：全部返回（纯 SQL 分页路径回归） ----
	all := callDecodedTool(t, m, ctx, map[string]any{"session_id": sessionID})
	if got := all["total_matched"].(float64); got != 6 {
		t.Fatalf("no semantic total_matched = %v, want 6", got)
	}
}

// writeSemanticFixture 写入带不同 annotate 语义标签的事件：
// e1 request / e2 response+error（多标签）/ e3 notification / e4 未标注 /
// e5 error@conn-1 / e6 error@conn-2。
func writeSemanticFixture(t *testing.T, st *store.SQLiteStore, sessionID string) {
	t.Helper()
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	mk := func(id string, offsetMS int64, semantic []string, connID string) *event.Event {
		ctx := event.EventContext{Direction: "client_to_server"}
		if connID != "" {
			ctx.ConnID = connID // AppendEvents 据此写入 event_index，供 conn_id 过滤
		}
		ev := event.NewEventWithTime(sessionID, event.EventType("proto.msg"), "test",
			event.ValueObject(map[string]event.Value{"seq": event.ValueInt(1)}),
			base.Add(time.Duration(offsetMS)*time.Millisecond), ctx)
		ev.Identity.ID = event.EventID(id)
		if len(semantic) > 0 {
			labels := make([]event.Value, 0, len(semantic))
			for _, s := range semantic {
				labels = append(labels, event.ValueString(s))
			}
			// 新模型：与 semantic_hook.applySemantics 一致的 Meta.semantic 结构。
			ev.Meta = event.ValueObject(map[string]event.Value{"semantic": event.ValueArray(labels)})
		}
		return ev
	}
	evs := []*event.Event{
		mk("e1", 0, []string{"request"}, ""),
		mk("e2", 1, []string{"response", "error"}, ""),
		mk("e3", 2, []string{"notification"}, ""),
		mk("e4", 3, nil, ""),
		mk("e5", 4, []string{"error"}, "conn-1"),
		mk("e6", 5, []string{"error"}, "conn-2"),
	}
	if err := st.AppendEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// rowHasSemantic 报告解码出的行 JSON 携带指定语义标签。
func rowHasSemantic(row map[string]any, label string) bool {
	meta, ok := row["meta"].(map[string]any)
	if !ok {
		return false
	}
	sem, _ := meta["semantic"].([]any)
	for _, s := range sem {
		if s == label {
			return true
		}
	}
	return false
}