package main

import (
	"context"
	"os"
	"testing"
	"time"

	"gametrace/pkg/store"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

// TestQueryDecodeErrorsTool 验证 query_decode_errors 的接线与返回结构。
//
// 前端直接按这个结构渲染（web/src/types/decode-error.ts），字段名错位不会报错、
// 只会静默显示成空 —— 所以这里把形状钉死。
func TestQueryDecodeErrorsTool(t *testing.T) {
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
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	if err := st.ReplaceDecodeErrorGroups(ctx, sessionID, []store.DecodeErrorRow{
		{
			Fingerprint: "fp-minor", Kind: "plugin", Template: "stream ended early",
			Sample: "stream ended early", SampleRawID: "raw-2", Count: 100,
			FirstSeen: base, LastSeen: base.Add(2 * time.Second),
		},
		{
			Fingerprint: "fp-major", Kind: "plugin", Template: "unsupported frame magic at byte <n>",
			Sample: "unsupported frame magic at byte 13", SampleRawID: "pkt-0",
			SampleSrc: "10.0.0.1:5", SampleDst: "10.0.0.2:9250", Count: 900,
			FirstSeen: base, LastSeen: base.Add(time.Second),
		},
	}); err != nil {
		t.Fatalf("ReplaceDecodeErrorGroups: %v", err)
	}
	st.Close()

	if err := sessionMgr.writeCurrent(sessionMetadata{
		SessionID: sessionID, StartedAt: base.Format(time.RFC3339), Status: "stopped", DBPath: dbPath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID, StartedAt: base.Format(time.RFC3339), Status: "stopped", DBPath: dbPath,
	}); err != nil {
		t.Fatal(err)
	}

	out := callDecodeErrorTool(t, m, ctx, map[string]any{"session_id": sessionID})

	if got := out["session_id"]; got != sessionID {
		t.Errorf("session_id = %v, want %s", got, sessionID)
	}
	// 总次数是各组之和，与会话状态的 decode_errors 同口径。
	if got := out["total_failures"].(float64); got != 1000 {
		t.Errorf("total_failures = %v, want 1000", got)
	}
	if got := out["kinds"].(float64); got != 2 {
		t.Errorf("kinds = %v, want 2", got)
	}

	groups := out["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	// 按次数降序：900 次的那类在前，否则用户先看到的不是主因。
	first := groups[0].(map[string]any)
	if got := first["count"].(float64); got != 900 {
		t.Errorf("首组 count = %v, want 900（应按次数降序）", got)
	}
	if got := first["template"]; got != "unsupported frame magic at byte <n>" {
		t.Errorf("template = %v, want 归一化模板", got)
	}
	if got := first["sample"]; got != "unsupported frame magic at byte 13" {
		t.Errorf("sample = %v, want 首条原文", got)
	}
	if got := first["sample_raw_packet_id"]; got != "pkt-0" {
		t.Errorf("sample_raw_packet_id = %v, want pkt-0（前端要用它下钻）", got)
	}
	if got := first["kind"]; got != "plugin" {
		t.Errorf("kind = %v, want plugin", got)
	}
	if out["note"] == "" {
		t.Error("note 为空：查不到分组时必须说清是「未记录」而不是「没有失败」")
	}

	// limit 生效，且不改变 total_failures（它描述的是全量）。
	limited := callDecodeErrorTool(t, m, ctx, map[string]any{"session_id": sessionID, "limit": 1})
	if got := len(limited["groups"].([]any)); got != 1 {
		t.Errorf("limit=1 时 groups = %d, want 1", got)
	}
	if got := limited["total_failures"].(float64); got != 1000 {
		t.Errorf("limit 截断后 total_failures = %v, want 1000（应仍是全量）", got)
	}
}

// TestQueryDecodeErrorsToolEmpty 覆盖「有失败但没记录原因」：不能显示成没有失败。
func TestQueryDecodeErrorsToolEmpty(t *testing.T) {
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
		t.Fatal(err)
	}
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

	out := callDecodeErrorTool(t, m, ctx, map[string]any{"session_id": sessionID})
	if got := out["total_failures"].(float64); got != 0 {
		t.Errorf("total_failures = %v, want 0", got)
	}
	groups, _ := out["groups"].([]any)
	if len(groups) != 0 {
		t.Errorf("groups = %d, want 0", len(groups))
	}
	note, _ := out["note"].(string)
	if note == "" {
		t.Fatal("空结果必须带 note：否则用户会把「没记录」读成「没失败」")
	}
}

func callDecodeErrorTool(t *testing.T, m *mcpCapture, ctx context.Context, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := m.handleQueryDecodeErrors(ctx, req)
	if err != nil {
		t.Fatalf("query_decode_errors: %v", err)
	}
	return decodeToolJSON(t, contentText(res))
}
