package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/plugindev"
)

// toolReq 构造一个带参数的 MCP 工具调用请求。
func toolReq(args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	return req
}

// toolText 取出工具结果的 JSON 文本。
func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("expected tool content")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	return tc.Text
}

// TestHandleScaffoldPluginReturnsContentsOnly 锁定平台侧不落盘：scaffold 只返回
// 文件清单与内容，不返回任何输出目录，也不创建任何文件 —— 源码由 Agent 在自己的
// workspace 落盘，平台不持有用户插件源码。
func TestHandleScaffoldPluginReturnsContentsOnly(t *testing.T) {
	m := &mcpCapture{}
	res, err := m.handleScaffoldPlugin(context.Background(), toolReq(map[string]any{
		"name":     "my-game-decoder",
		"protocol": "my_game",
	}))
	if err != nil {
		t.Fatal(err)
	}
	text := toolText(t, res)

	var parsed struct {
		Name     string            `json:"name"`
		Template string            `json:"template"`
		Files    []string          `json:"files"`
		Contents map[string]string `json:"contents"`
		Notes    []string          `json:"notes"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("unmarshal scaffold result: %v\n%s", err, text)
	}
	if parsed.Template == "" {
		t.Errorf("expected a template id, got %s", text)
	}
	if len(parsed.Files) == 0 {
		t.Fatalf("expected files, got %s", text)
	}
	for _, f := range parsed.Files {
		if parsed.Contents[f] == "" {
			t.Errorf("file %q has no content: %s", f, text)
		}
	}
	if strings.Contains(text, "output_dir") {
		t.Errorf("scaffold must not return an output_dir (the platform writes nothing): %s", text)
	}
	if len(parsed.Notes) == 0 {
		t.Errorf("expected usage notes, got %s", text)
	}
}

func TestHandleScaffoldPluginValidatesInput(t *testing.T) {
	m := &mcpCapture{}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing name", map[string]any{"protocol": "my_game"}},
		{"bad kebab-case", map[string]any{"name": "My_Game", "protocol": "my_game"}},
		{"missing protocol", map[string]any{"name": "my-game"}},
	}
	for _, c := range cases {
		res, err := m.handleScaffoldPlugin(context.Background(), toolReq(c.args))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if text := toolText(t, res); !strings.Contains(text, `"ok":false`) {
			t.Errorf("%s: expected a validation error, got %s", c.name, text)
		}
	}
}

// TestHandleStatusPluginOffline 锁定单一状态视图：平台没有制品/dev 进程视角，
// 未注册的插件报 runtime.state=offline、无验证证明，并把用户推向 connect_plugin。
func TestHandleStatusPluginOffline(t *testing.T) {
	m := &mcpCapture{}
	res, err := m.handleStatusPlugin(context.Background(), toolReq(map[string]any{"name": "ghost"}))
	if err != nil {
		t.Fatal(err)
	}
	text := toolText(t, res)

	var parsed struct {
		Name     string `json:"name"`
		Runtime  struct {
			State string `json:"state"`
		} `json:"runtime"`
		Validation struct {
			Validated bool `json:"validated"`
		} `json:"validation"`
		NextAction map[string]any `json:"next_action"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("unmarshal status: %v\n%s", err, text)
	}
	if parsed.Name != "ghost" {
		t.Errorf("expected name=ghost, got %q", parsed.Name)
	}
	if parsed.Runtime.State != "offline" {
		t.Errorf("expected runtime.state=offline (no registry link), got %q", parsed.Runtime.State)
	}
	if parsed.Validation.Validated {
		t.Errorf("expected no validation proof for an unknown plugin: %s", text)
	}
	if parsed.NextAction == nil || parsed.NextAction["tool"] != "connect_plugin" {
		t.Errorf("expected next_action.tool=connect_plugin, got %v", parsed.NextAction)
	}
	if strings.Contains(text, "artifact") || strings.Contains(text, "dev_process") {
		t.Errorf("status must not expose artifact/dev_process: %s", text)
	}
}

// TestHandleConnectPluginValidatesName 锁定 connect 的入参校验（name 必填且 kebab-case）。
func TestHandleConnectPluginValidatesName(t *testing.T) {
	m := &mcpCapture{}
	for _, args := range []map[string]any{{}, {"name": "Bad_Name"}} {
		res, err := m.handleConnectPlugin(context.Background(), toolReq(args))
		if err != nil {
			t.Fatal(err)
		}
		if text := toolText(t, res); !strings.Contains(text, `"ok":false`) {
			t.Errorf("args %v: expected a validation error, got %s", args, text)
		}
	}
}

// TestHandleExplainPluginInlineVerify 锁定 in-process 归因：verify 参数里的高熵 +
// 多数不可解被归成 suspected-encryption，并引用 contract.yaml 的 rule_id。
func TestHandleExplainPluginInlineVerify(t *testing.T) {
	m := &mcpCapture{}
	res, err := m.handleExplainPlugin(context.Background(), toolReq(map[string]any{
		"name": "explain-inline",
		"verify": map[string]any{
			"verdict": "fail",
			"quality": map[string]any{
				"input":  map[string]any{"raw": 10, "candidate": 10},
				"decode": map[string]any{"success": 2, "unknown": 8, "unknown_ratio": 0.8, "errors": 0},
				"entropy_estimate": 7.8,
			},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	text := toolText(t, res)
	for _, want := range []string{"suspected-encryption", "inspect-bytes-first", "expl_"} {
		if !strings.Contains(text, want) {
			t.Errorf("explain result missing %q: %s", want, text)
		}
	}
}

// TestHandleExplainPluginNoRecordedVerify 锁定「没有 verify 结果可归因」时的兜底：
// 给出去跑 verify_plugin 的下一步，而不是编造结论。
func TestHandleExplainPluginNoRecordedVerify(t *testing.T) {
	m := &mcpCapture{}
	res, err := m.handleExplainPlugin(context.Background(), toolReq(map[string]any{"name": "never-verified"}))
	if err != nil {
		t.Fatal(err)
	}
	text := toolText(t, res)
	if !strings.Contains(text, "no verify result recorded") {
		t.Errorf("expected the no-result fallback: %s", text)
	}
	if !strings.Contains(text, "verify_plugin") {
		t.Errorf("expected next_action to point at verify_plugin: %s", text)
	}
}

// TestHandleExplainPluginRecordedVerify 锁定兜底路径：调用方不传 verify 时，用
// plugindev 进程内 Tracker 记下的最近一次 verify 结果归因。
func TestHandleExplainPluginRecordedVerify(t *testing.T) {
	name := "recorded-verify"
	plugindev.RecordVerify(name, &plugindev.VerifyResult{
		Verdict: "fail",
		Quality: &plugindev.QualityStats{InputCandidate: 4, DecodeUnknown: 4},
	})

	m := &mcpCapture{}
	res, err := m.handleExplainPlugin(context.Background(), toolReq(map[string]any{"name": name}))
	if err != nil {
		t.Fatal(err)
	}
	text := toolText(t, res)
	if !strings.Contains(text, "all-unknown") {
		t.Errorf("expected the recorded verify to be attributed as all-unknown: %s", text)
	}
}