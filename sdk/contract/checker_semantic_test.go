package contract

import (
	"encoding/json"
	"testing"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	"github.com/OwnSecurityGuard/gametrace/sdk/event"
)

// 覆盖方案 §12 的完整例子：四条规则（pair / notification / SyncDbData 提取 / error 标注）。
// 语义契约 v1 的 schemas/states 层已移除，semantic_rules 是唯一的语义声明。
const semanticPluginManifest = `
api_version: gt.decoder/v2
name: my-game
protocol: my_game
type: decoder
capabilities:
  decode: true
semantic_rules:
  - id: my_game.pair_request_response
    when:
      all:
        - { path: seqId, op: neq, value: 0 }
        - { path: direction, op: in, value: [client_to_server, server_to_client] }
    effect:
      type: pair
      sides:
        - { path: direction, op: eq, value: client_to_server, key: seqId }
        - { path: direction, op: eq, value: server_to_client, key: meta.req_seq }
  - id: my_game.mark_push
    when:
      - { path: seqId, op: eq, value: 0 }
    effect:
      type: annotate
      semantic: notification
  - id: my_game.extract_sync_db
    when:
      - { path: data.SyncDbData, op: exists }
    effect:
      type: extract
      source: data.SyncDbData
      child:
        event_type: my_game.db.update
        schema_id: my_game.db_update.v1
  - id: my_game.mark_error
    when:
      - { path: error, op: exists }
    effect:
      type: annotate
      semantic: error
`

func parseSemanticManifest(t *testing.T) *sdk.Manifest {
	t.Helper()
	m, err := sdk.ParseManifest([]byte(semanticPluginManifest))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m
}

// 声明合法的四条规则 → 声明期零违规。
func TestSemanticRulesDeclarationValid(t *testing.T) {
	r := NewPluginChecker().Check(parseSemanticManifest(t))
	if r.HasErrors() {
		t.Fatalf("expected clean manifest, got:\n%s", mustJSON(t, r))
	}
}

// 各类声明错误都必须以 gt.semantic.* 报出（semantic 层）。
func TestSemanticRulesDeclarationErrors(t *testing.T) {
	pc := NewPluginChecker()

	cases := []struct {
		name   string
		break_ func(m *sdk.Manifest)
		ruleID string
	}{
		{"duplicate rule id", func(m *sdk.Manifest) {
			m.SemanticRules[3].ID = m.SemanticRules[0].ID
		}, "gt.semantic.rule-id-duplicate"},
		{"unknown op", func(m *sdk.Manifest) {
			m.SemanticRules[0].When.All[0].Op = "regex"
		}, "gt.semantic.op-unknown"},
		{"pair side without key", func(m *sdk.Manifest) {
			m.SemanticRules[0].Effect.Sides[0].Key = ""
		}, "gt.semantic.pair-key-required"},
		{"pair with one side", func(m *sdk.Manifest) {
			m.SemanticRules[0].Effect.Sides = m.SemanticRules[0].Effect.Sides[:1]
		}, "gt.semantic.pair-sides-limit"},
		{"unknown annotate semantic", func(m *sdk.Manifest) {
			m.SemanticRules[1].Effect.Semantic = "warning"
		}, "gt.semantic.annotate-semantic-unknown"},
		{"extract without child", func(m *sdk.Manifest) {
			m.SemanticRules[2].Effect.Child = nil
		}, "gt.semantic.extract-child-required"},
		{"in without array", func(m *sdk.Manifest) {
			m.SemanticRules[0].When.All[1].Value = "client_to_server"
		}, "gt.semantic.value-array-required"},
	}
	for _, c := range cases {
		clone := parseSemanticManifest(t)
		c.break_(clone)
		r := pc.Check(clone)
		if !hasRule(r, c.ruleID, LayerSemantic) {
			t.Errorf("%s: want %s (layer semantic), got:\n%s", c.name, c.ruleID, mustJSON(t, r))
		}
	}
}

// 运行期：规则评估成功 → 零违规（pair/annotate/extract 的命中是平台执行事实）。
func TestSemanticRulesEventValid(t *testing.T) {
	m := parseSemanticManifest(t)
	pc := NewPluginChecker()
	d := &event.Draft{
		Type: "my_game.response.received",
		Value: event.ValueObject(map[string]event.Value{
			"seqId": event.ValueInt(123),
			"error": event.ValueString("hp insufficient"),
			"data": event.ValueObject(map[string]event.Value{
				"SyncDbData": event.ValueArray([]event.Value{
					event.ValueObject(map[string]event.Value{"kind": event.ValueString("player")}),
				}),
			}),
		}),
	}
	r := pc.CheckEvent(m, d)
	if r.HasErrors() {
		t.Fatalf("expected clean event, got:\n%s", mustJSON(t, r))
	}
}

// mustJSON 序列化 Report 供失败信息展示。
func mustJSON(t *testing.T, r *Report) string {
	t.Helper()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(b)
}

// hasRule 报告 Report 中是否存在指定规则 ID + 层的违规。
func hasRule(r *Report, ruleID string, layer Layer) bool {
	for _, v := range r.Violations {
		if v.RuleID == ruleID && v.Layer == layer {
			return true
		}
	}
	return false
}
