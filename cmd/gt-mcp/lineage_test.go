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

// TestListDecodedDataLineageFields 验证协议血缘分析依赖的两个 Trace 本源字段
// （correlation_id / causation_id）在 list_decoded_data 的输出行与 expr filter
// 中都可用。
func TestListDecodedDataLineageFields(t *testing.T) {
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
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	writeLineageFixture(t, st, sessionID, base)
	st.Close()

	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID,
		StartedAt: base.Format(time.RFC3339),
		Status:    "stopped",
		DBPath:    dbPath,
	}); err != nil {
		t.Fatal(err)
	}

	// ---- 1. 无 filter：输出行携带全部血缘字段 ----
	all := callDecodedTool(t, m, ctx, map[string]any{"session_id": sessionID, "limit": 50})
	rows := all["events"].([]any)
	if len(rows) != 5 {
		t.Fatalf("events = %d, want 5", len(rows))
	}
	byID := map[string]map[string]any{}
	for _, r := range rows {
		row := r.(map[string]any)
		byID[row["id"].(string)] = row
	}
	// pair：请求/响应同组，响应 causation 指回请求。
	if got := byID["r1"]["correlation_id"]; got != "corr-1" {
		t.Errorf("r1 correlation_id = %v, want corr-1", got)
	}
	if got := byID["r2"]["causation_id"]; got != "r1" {
		t.Errorf("r2 causation_id = %v, want r1", got)
	}
	// 业务字段经 payload 透传（注意 entity/entity_id 等是平台保留分析键，
	// 会被拆进 analysis 段，业务字段须避开，见 SplitReservedKeys）。
	if data, ok := byID["c1"]["data"].(map[string]any); !ok || data["name"] != "hero" {
		t.Errorf("c1 data.name = %v, want hero (data=%v)", byID["c1"]["data"], byID["c1"]["data"])
	}

	// ---- 2. filter 按 correlation_id 拉配对组（pair 血缘） ----
	group := callDecodedTool(t, m, ctx, map[string]any{
		"session_id": sessionID, "filter": `correlation_id == "corr-1"`,
	})
	if got := group["total_matched"].(float64); got != 2 {
		t.Fatalf("correlation_id filter total_matched = %v, want 2", got)
	}

	// ---- 3. filter 按 causation_id 反查「谁响应了 r1」 ----
	resp := callDecodedTool(t, m, ctx, map[string]any{
		"session_id": sessionID, "filter": `causation_id == "r1"`,
	})
	if got := resp["total_matched"].(float64); got != 1 {
		t.Fatalf("causation_id filter total_matched = %v, want 1", got)
	}

	// ---- 4. 组合血缘过滤 ----
	mixed := callDecodedTool(t, m, ctx, map[string]any{
		"session_id": sessionID,
		"filter":     `correlation_id == "corr-1" && causation_id == "r1"`,
	})
	if got := mixed["total_matched"].(float64); got != 1 {
		t.Fatalf("combined filter total_matched = %v, want 1", got)
	}

	// ---- 5. 前端展开行的配对组查询（web/src/lib/pair-group.ts pairGroupFilter） ----
	// 展开一条响应要拿到「请求本人 + 这一组的全部响应」。expr 的 || 与字符串字面量
	// 必须被 queryEnv 接受，否则前端的并排视图永远空白。
	pair := callDecodedTool(t, m, ctx, map[string]any{
		"session_id": sessionID, "filter": `id == "r1" || causation_id == "r1"`,
	})
	if got := pair["total_matched"].(float64); got != 2 {
		t.Fatalf("配对组 total_matched = %v, want 2", got)
	}

	// 未配对消息（推送 p1）按自己反查应当为空 —— 前端据此保持单事件展示，
	// 而不是画出半个空壳的并排布局。
	none := callDecodedTool(t, m, ctx, map[string]any{
		"session_id": sessionID, "filter": `causation_id == "p1"`,
	})
	if got := none["total_matched"].(float64); got != 0 {
		t.Fatalf("未配对消息的配对组 = %v, want 0", got)
	}
}

// writeLineageFixture 写入协议血缘分析的典型数据：
// 一对 pair 配对（r1 请求 → r2 响应）+ 三个普通事件（含业务字段）。
func writeLineageFixture(t *testing.T, st *store.SQLiteStore, sessionID string, base time.Time) {
	t.Helper()
	mkEvent := func(id, msgName, direction string, offsetMS int64, correlation, causation string) *event.Event {
		ev := event.NewEventWithTime(sessionID, event.EventType(msgName), "test",
			event.ValueFromAny(map[string]any{
				"_meta": map[string]any{"msg_name": msgName, "direction": direction},
			}), base.Add(time.Duration(offsetMS)*time.Millisecond),
			event.EventContext{FlowID: "flow-1", Direction: direction})
		ev.Identity.ID = event.EventID(id)
		ev.Trace = event.TraceContext{CorrelationID: correlation, CausationID: event.EventID(causation)}
		return ev
	}
	mkEntity := func(id string, offsetMS int64, name string) *event.Event {
		ev := mkEvent(id, "EntityState", "server_to_client", offsetMS, "", "")
		// 业务字段名须避开平台保留分析键（entity/entity_type/entity_id/
		// change_count——会被拆进 analysis 段）。
		ev.Payload.Value.Object["name"] = event.Value{Kind: event.String, Str: name}
		return ev
	}
	evs := []*event.Event{
		mkEvent("r1", "UpgradeReq", "client_to_server", 0, "corr-1", ""),
		mkEvent("r2", "UpgradeResp", "server_to_client", 200, "corr-1", "r1"),
		mkEvent("p1", "Snapshot", "server_to_client", 300, "", ""),
		mkEntity("c1", 301, "hero"),
		mkEntity("c2", 302, "npc"),
	}
	if err := st.AppendEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// callDecodedTool 调用 handleListDecodedData 并解码 JSON 响应。
func callDecodedTool(t *testing.T, m *mcpCapture, ctx context.Context, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := m.handleListDecodedData(ctx, req)
	if err != nil {
		t.Fatalf("list_decoded_data: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_decoded_data returned error: %s", contentText(res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(contentText(res)), &out); err != nil {
		t.Fatalf("unmarshal list_decoded_data response: %v", err)
	}
	return out
}
