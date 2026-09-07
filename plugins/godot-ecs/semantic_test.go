package main

import (
	"os"
	"testing"

	sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
	sdkevent "github.com/OwnSecurityGuard/gt-plugin-sdk/event"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/rule"
)

// loadSemanticRules 从 plugin.yaml 解出 semantic_rules，供规则语义测试共用。
// manifest 不合法直接 Fatal——声明与实现漂移要在这里暴露，而不是等抓包。
func loadSemanticRules(t *testing.T) []rule.Rule {
	t.Helper()
	data, err := os.ReadFile("plugin.yaml")
	if err != nil {
		t.Fatalf("read plugin.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(data)
	if err != nil {
		t.Fatalf("parse plugin.yaml: %v", err)
	}
	if len(m.SemanticRules) == 0 {
		t.Fatal("plugin.yaml declares no semantic_rules")
	}
	return m.SemanticRules
}

// evalRules 对 JSON 载荷跑全部规则。payload 形状与 decoder 产出一致：
// 业务字段在顶层，msg_name/direction 在 _meta（宿主保留 _meta 不剔除）。
func evalRules(t *testing.T, rules []rule.Rule, payloadJSON string) rule.Result {
	t.Helper()
	v, err := sdkevent.ValueFromJSON([]byte(payloadJSON))
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	res, err := rule.Evaluate(rules, v)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return res
}

// TestSemanticPairPingPong 验证 pair_ping_pong 规则：
// 同 ts 的 ping/pong 各占一个 side 可配对；不同 ts 或同侧不配对；
// 非 ping/pong 帧不产生 pair 命中。
func TestSemanticPairPingPong(t *testing.T) {
	rules := loadSemanticRules(t)

	ping := evalRules(t, rules, `{"ts":1725714000123,"_meta":{"msg_name":"ping","direction":"c2s"}}`)
	pong := evalRules(t, rules, `{"ts":1725714000123,"tick":4242,"_meta":{"msg_name":"pong","direction":"s2c"}}`)

	if len(ping.Pairs) != 1 {
		t.Fatalf("ping pair hits = %d, want 1", len(ping.Pairs))
	}
	if len(pong.Pairs) != 1 {
		t.Fatalf("pong pair hits = %d, want 1", len(pong.Pairs))
	}
	hp, hq := ping.Pairs[0], pong.Pairs[0]

	if hp.Key != hq.Key {
		t.Errorf("pair keys differ: %q vs %q", hp.Key, hq.Key)
	}
	if hp.Side == hq.Side {
		t.Errorf("ping and pong hit the same side %d", hp.Side)
	}
	if !rule.MatchPair(hp, hq) {
		t.Errorf("ping %+v and pong %+v should pair", hp, hq)
	}

	// ts 不同 → 键不等 → 不配对
	pongOther := evalRules(t, rules, `{"ts":1725714000999,"tick":4243,"_meta":{"msg_name":"pong"}}`)
	if len(pongOther.Pairs) != 1 {
		t.Fatalf("pong(other ts) pair hits = %d, want 1", len(pongOther.Pairs))
	}
	if rule.MatchPair(hp, pongOther.Pairs[0]) {
		t.Error("ping and pong with different ts must not pair")
	}

	// state 帧不参与配对
	state := evalRules(t, rules, `{"tick":1,"_meta":{"msg_name":"state"}}`)
	if len(state.Pairs) != 0 {
		t.Errorf("state frame pair hits = %d, want 0", len(state.Pairs))
	}
}

// TestSemanticAnnotate 验证 annotate 规则：ping→request、pong→response、state→notification。
func TestSemanticAnnotate(t *testing.T) {
	rules := loadSemanticRules(t)

	cases := []struct {
		payload string
		want    rule.Semantic
	}{
		{`{"ts":1,"_meta":{"msg_name":"ping"}}`, rule.SemRequest},
		{`{"ts":1,"tick":2,"_meta":{"msg_name":"pong"}}`, rule.SemResponse},
		{`{"tick":1,"_meta":{"msg_name":"state"}}`, rule.SemNotification},
		{`{"id":7,"hz":30,"_meta":{"msg_name":"welcome"}}`, rule.SemNotification},
		{`{"x":1.5,"y":2,"z":3,"_meta":{"msg_name":"pos"}}`, rule.SemNotification},
	}
	for _, c := range cases {
		res := evalRules(t, rules, c.payload)
		if len(res.Semantics) != 1 || res.Semantics[0] != c.want {
			t.Errorf("%s semantics = %v, want [%s]", c.payload, res.Semantics, c.want)
		}
	}
}
