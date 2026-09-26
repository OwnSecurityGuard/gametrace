package main

import (
	"context"
	"encoding/json"
	"testing"

	"gametrace/pkg/auth"
	"gametrace/pkg/checkrule"
	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
	"github.com/mark3labs/mcp-go/mcp"
)

func activeCheckRule(id string) checkrule.CheckRule {
	return checkrule.CheckRule{ID: id, Name: id, Enabled: true,
		When: rule.Predicate{Path: "type", Op: rule.OpEq, Value: "login"}}
}

// resultOK 解析工具返回的 JSON 文本块，报告 ok 字段与 error 文本。
// gt-mcp 的 errorResult 用 {ok:false,error:...} 表达失败（不置 MCP IsError 标志），
// Web 侧 mcp-client 据此抛错，故测试须看 payload 而非 res.IsError。
func resultOK(t *testing.T, res *mcp.CallToolResult) (bool, string) {
	t.Helper()
	var parsed struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(contentText(res)), &parsed); err != nil {
		t.Fatalf("result text not JSON: %v (%s)", err, contentText(res))
	}
	ok := parsed.OK != nil && *parsed.OK
	return ok, parsed.Error
}

// TestSetProjectRulesRejectsInvalid 验证保存期整表校验：启用规则缺 when → 拒绝。
func TestSetProjectRulesRejectsInvalid(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"})
	if _, err := m.handleCreateProject(ctx, reqWith("name", "P")); err != nil {
		t.Fatal(err)
	}
	created, err := m.projects.ListVisible(ctx, "bob", false)
	if err != nil || len(created) != 1 {
		t.Fatalf("list: %v %v", created, err)
	}
	pid := created[0].ID

	// 启用但无 when → RulesReport 报 error → 整表拒绝。
	bad := `[{"id":"r1","name":"x","enabled":true}]`
	res, err := m.handleSetProjectRules(ctx, reqWith("project_id", pid, "rules", bad))
	if err != nil {
		t.Fatal(err)
	}
	if ok, msg := resultOK(t, res); ok || msg == "" {
		t.Fatalf("expected rejection of enabled rule without when, got ok=%v msg=%q", ok, msg)
	}
	// 拒绝后项目规则不应被改动。
	got, _ := m.projects.Get(ctx, pid)
	if len(got.Rules) != 0 {
		t.Fatalf("rejected write must not persist rules: %+v", got.Rules)
	}
}

// TestSetProjectRulesPersistsValid 验证合法结构化规则整表入库并可读回。
func TestSetProjectRulesPersistsValid(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"})
	if _, err := m.handleCreateProject(ctx, reqWith("name", "P")); err != nil {
		t.Fatal(err)
	}
	created, _ := m.projects.ListVisible(ctx, "bob", false)
	pid := created[0].ID

	good := `[{"id":"r1","name":"login","enabled":true,"when":{"path":"type","op":"eq","value":"login"},"cooldown_sec":45,"context_per_direction":5}]`
	res, err := m.handleSetProjectRules(ctx, reqWith("project_id", pid, "rules", good))
	if err != nil {
		t.Fatal(err)
	}
	if ok, msg := resultOK(t, res); !ok {
		t.Fatalf("valid rules rejected: ok=false msg=%q", msg)
	}
	got, _ := m.projects.Get(ctx, pid)
	if len(got.Rules) != 1 {
		t.Fatalf("rules not persisted: %+v", got.Rules)
	}
	if got.Rules[0].ID != "r1" || got.Rules[0].ContextPerDirection != 5 || !got.Rules[0].Enabled {
		t.Fatalf("persisted rule lost fields: %+v", got.Rules[0])
	}
	if string(got.Rules[0].When.Path) != "type" || string(got.Rules[0].When.Op) != "eq" {
		t.Fatalf("when not persisted: %+v", got.Rules[0].When)
	}
}

// TestCheckRulesJSONFor 验证抓包启动期解析：只下发 active 规则，非成员读不到即 nil。
func TestCheckRulesJSONFor(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	ctx := context.Background()

	p := &project{ID: "p1", Name: "P", CreatedBy: "bob", Owner: "bob",
		Rules: []projectRule{
			activeCheckRule("on"),
			{ID: "off", Name: "off", Enabled: false, When: rule.Predicate{Path: "type", Op: rule.OpEq, Value: "x"}},
			{ID: "legacy", Name: "legacy"}, // 旧 {id,name}：enabled=false → 不激活
		},
	}
	if err := m.projects.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	bobCtx := auth.WithPrincipal(ctx, &auth.Principal{Owner: "bob"})
	enc := m.checkRulesJSONFor(bobCtx, "p1")
	if len(enc) != 1 {
		t.Fatalf("expected 1 active rule encoded, got %d: %v", len(enc), enc)
	}
	var r checkrule.CheckRule
	if err := json.Unmarshal([]byte(enc[0]), &r); err != nil {
		t.Fatalf("encoded rule not JSON: %v", err)
	}
	if r.ID != "on" {
		t.Fatalf("active filter leaked: %+v", r)
	}

	// 无读权限（非成员）→ 静默 nil。
	strangerCtx := auth.WithPrincipal(ctx, &auth.Principal{Owner: "mallory"})
	if got := m.checkRulesJSONFor(strangerCtx, "p1"); got != nil {
		t.Fatalf("non-member should get nil, got %v", got)
	}
	// 空 projectID → nil。
	if got := m.checkRulesJSONFor(bobCtx, ""); got != nil {
		t.Fatalf("empty project id should get nil, got %v", got)
	}
}
