package rule

import (
	"testing"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
)

// benchRules 与 TestEvaluateSpecExample 同构：pair（含 per-side key）+
// annotate×2，代表真实插件 manifest 的规则组合。
func benchRules() []Rule {
	return []Rule{
		{
			ID: "game.pair_request_response",
			When: &Predicate{All: []Predicate{
				{Path: "seqId", Op: OpNeq, Value: 0},
				{Path: "direction", Op: OpIn, Value: []any{"client_to_server", "server_to_client"}},
			}},
			Effect: Effect{Type: EffectPair, Sides: []PairSide{
				{Key: "seqId", Predicate: Predicate{Path: "direction", Op: OpEq, Value: "client_to_server"}},
				{Key: "meta.req_seq", Predicate: Predicate{Path: "direction", Op: OpEq, Value: "server_to_client"}},
			}},
		},
		{ID: "game.mark_push", When: &Predicate{Path: "seqId", Op: OpEq, Value: 0}, Effect: Effect{Type: EffectAnnotate, Semantic: SemNotification}},
		{ID: "game.mark_error", When: &Predicate{Path: "error", Op: OpExists}, Effect: Effect{Type: EffectAnnotate, Semantic: SemError}},
	}
}

// BenchmarkEvaluate 覆盖语义规则求热的热路径（每条事件都要过一遍）：
// GJSON 取值 → Predicate 判断 → pair/annotate 效果。
func BenchmarkEvaluate(b *testing.B) {
	rules := benchRules()
	values := map[string]string{
		"request": `{"uri":"/battle/attack","seqId":123,"direction":"client_to_server","data":{"targetId":1001}}`,
		"response": `{"uri":"/battle/attack","seqId":123,"meta":{"req_seq":123},"direction":"server_to_client",` +
			`"error":"hp insufficient","data":{"stateUpdates":[{"player":{"id":1}},{"item":{"id":2}}]}}`,
		"push":   `{"uri":"/push/hp","seqId":0,"direction":"server_to_client"}`,
		"nested": `{"a":{"b":{"c":{"d":{"e":{"seqId":9,"direction":"client_to_server"}}}}}}`,
	}
	for name, doc := range values {
		v, err := event.ValueFromJSON([]byte(doc))
		if err != nil {
			b.Fatalf("%s: %v", name, err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Evaluate(rules, v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
