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

// evalRules 对 JSON 载荷跑全部规则。payload 形状与宿主求值视图一致
// （decoder 产出的业务 payload 合并 Meta）：msg_type 在顶层（name 规则的
// 提取源），direction 在 _meta。msg_name 不再由解码器硬编码，而是由
// name 规则第一遍求值注入 _meta.msg_name，供 pair/annotate 规则判定。
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

// TestSemanticNameMsgType 验证 name_msg 规则：从 payload 顶层 msg_type
// 提取消息名称（写入 Result.Names，宿主据此写 meta.msg_name）。
func TestSemanticNameMsgType(t *testing.T) {
	rules := loadSemanticRules(t)

	cases := []struct {
		payload string
		want    string
	}{
		{`{"msg_type":"ping","ts":1}`, "ping"},
		{`{"msg_type":"pong","ts":1,"tick":2}`, "pong"},
		{`{"msg_type":"state","tick":1}`, "state"},
		{`{"msg_type":"welcome","id":7,"hz":30}`, "welcome"},
		{`{"msg_type":"pos","x":1,"y":2,"z":3}`, "pos"},
	}
	for _, c := range cases {
		res := evalRules(t, rules, c.payload)
		if len(res.Names) != 1 || res.Names[0].Value != c.want {
			t.Errorf("%s names = %+v, want [{%s}]", c.payload, res.Names, c.want)
		}
	}

	// msg_type 缺失 → name 规则不命中（无法命名的事件交给解码器默认值）
	res := evalRules(t, rules, `{"ts":1}`)
	if len(res.Names) != 0 {
		t.Errorf("payload without msg_type names = %+v, want none", res.Names)
	}
}

// TestSemanticPairPingPong 验证 pair_ping_pong 规则：
// 同 ts 的 ping/pong 各占一个 side 可配对；不同 ts 或同侧不配对；
// 非 ping/pong 帧不产生 pair 命中。
// 消息名由 name 规则从 msg_type 提取后注入 _meta.msg_name，pair 规则据此判定。
func TestSemanticPairPingPong(t *testing.T) {
	rules := loadSemanticRules(t)

	ping := evalRules(t, rules, `{"msg_type":"ping","ts":1725714000123,"_meta":{"direction":"c2s"}}`)
	pong := evalRules(t, rules, `{"msg_type":"pong","ts":1725714000123,"tick":4242,"_meta":{"direction":"s2c"}}`)

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
	pongOther := evalRules(t, rules, `{"msg_type":"pong","ts":1725714000999,"tick":4243}`)
	if len(pongOther.Pairs) != 1 {
		t.Fatalf("pong(other ts) pair hits = %d, want 1", len(pongOther.Pairs))
	}
	if rule.MatchPair(hp, pongOther.Pairs[0]) {
		t.Error("ping and pong with different ts must not pair")
	}

	// state 帧不参与配对
	state := evalRules(t, rules, `{"msg_type":"state","tick":1}`)
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
		{`{"msg_type":"ping","ts":1}`, rule.SemRequest},
		{`{"msg_type":"pong","ts":1,"tick":2}`, rule.SemResponse},
		{`{"msg_type":"state","tick":1}`, rule.SemNotification},
		{`{"msg_type":"welcome","id":7,"hz":30}`, rule.SemNotification},
		{`{"msg_type":"pos","x":1.5,"y":2,"z":3}`, rule.SemNotification},
	}
	for _, c := range cases {
		res := evalRules(t, rules, c.payload)
		if len(res.Semantics) != 1 || res.Semantics[0] != c.want {
			t.Errorf("%s semantics = %v, want [%s]", c.payload, res.Semantics, c.want)
		}
	}
}
