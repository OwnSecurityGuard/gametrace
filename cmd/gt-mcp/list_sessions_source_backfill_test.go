package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/store"
)

// TestListAllSessionsBackfillsSourceFromMetadata 锁住「探针会话不再被叫成服务器网卡」：
// controlStore 的 sessions 表没有 source/extra 列，而 gt-mcp 在探针链路建会话时会
// 另写一份 metadata.json。列表以 controlStore 为主源，命中后跳过文件侧记录——若不
// 回填，探针会话的 source 就是空串，前端据此判定为「服务网卡上抓的」。
func TestListAllSessionsBackfillsSourceFromMetadata(t *testing.T) {
	workDir := t.TempDir()
	cs, err := store.NewControlStore(filepath.Join(workDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	ctx := context.Background()
	if err := cs.CreateSession(ctx, store.SessionMeta{
		SessionID: "prb-1",
		Status:    "running",
		Port:      8080,
		DBPath:    filepath.Join(workDir, "sessions", "prb-1", "capture.sqlite"),
	}); err != nil {
		t.Fatal(err)
	}

	sm := newSessionManager(workDir)
	if err := os.MkdirAll(filepath.Join(workDir, "sessions", "prb-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sm.writeSessionMetadata("prb-1", sessionMetadata{
		SessionID: "prb-1",
		Status:    "running",
		Port:      8080,
		Source:    "probe",
		Extra:     map[string]any{"probe_id": "prb_x"},
	}); err != nil {
		t.Fatal(err)
	}

	m := &mcpCapture{sessionMgr: sm, controlStore: cs}
	res, err := m.handleListAllSessions(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sessions []struct {
			SessionID string         `json:"session_id"`
			Source    string         `json:"source"`
			Extra     map[string]any `json:"extra"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 1 {
		t.Fatalf("会话数 = %d, want 1", len(parsed.Sessions))
	}
	got := parsed.Sessions[0]
	if got.Source != "probe" {
		t.Errorf("source = %q, want %q（metadata.json 的 source 未回填）", got.Source, "probe")
	}
	if got.Extra["probe_id"] != "prb_x" {
		t.Errorf("extra = %v, want probe_id=prb_x", got.Extra)
	}
}
