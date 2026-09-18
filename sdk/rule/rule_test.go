package rule

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
)

// doc 必须覆盖用户实际协议的主要形态（§12）：
// request/response（seqId 配对）、push（seqId=0）、error 标注。
const sampleEvent = `{
  "uri": "/battle/attack",
  "seqId": 123,
  "direction": "client_to_server",
  "data": {"targetId": 1001},
  "error": "timeout"
}`

func docOf(t *testing.T, json string) gjson.Result {
	t.Helper()
	return gjson.Parse(json)
}

func TestPredicateLeafOps(t *testing.T) {
	d := docOf(t, sampleEvent)

	cases := []struct {
		name string
		p    Predicate
		want bool
	}{
		{"eq number", Predicate{Path: "seqId", Op: OpEq, Value: 123}, true},
		{"eq number mismatch", Predicate{Path: "seqId", Op: OpEq, Value: 0}, false},
		{"eq string", Predicate{Path: "direction", Op: OpEq, Value: "client_to_server"}, true},
		{"eq string kind mismatch", Predicate{Path: "seqId", Op: OpEq, Value: "123"}, false},
		{"neq", Predicate{Path: "seqId", Op: OpNeq, Value: 0}, true},
		{"missing path eq", Predicate{Path: "nope", Op: OpEq, Value: 1}, false},
		{"missing path neq", Predicate{Path: "nope", Op: OpNeq, Value: 1}, false}, // 缺失即 false
		{"exists", Predicate{Path: "data.targetId", Op: OpExists}, true},
		{"not exists", Predicate{Path: "data.hp", Op: OpNotExists}, true},
		{"gt", Predicate{Path: "seqId", Op: OpGt, Value: 100}, true},
		{"gte", Predicate{Path: "seqId", Op: OpGte, Value: 123}, true},
		{"lt string", Predicate{Path: "uri", Op: OpLt, Value: "/battle/z"}, true},
		{"gt kind mismatch", Predicate{Path: "uri", Op: OpGt, Value: 1}, false},
		{"in", Predicate{Path: "direction", Op: OpIn, Value: []any{"client_to_server", "server_to_client"}}, true},
		{"not in", Predicate{Path: "uri", Op: OpNotIn, Value: []any{"/login"}}, true},
		{"contains string", Predicate{Path: "uri", Op: OpContains, Value: "battle"}, true},
		{"contains array", Predicate{Path: "data", Op: OpContains, Value: 1001}, false}, // data 是 object
		{"prefix", Predicate{Path: "uri", Op: OpPrefix, Value: "/battle"}, true},
		{"suffix", Predicate{Path: "uri", Op: OpSuffix, Value: "/attack"}, true},
		{"prefix mismatch", Predicate{Path: "uri", Op: OpPrefix, Value: "/login"}, false},
	}
	for _, c := range cases {
		if got := c.p.Evaluate(d); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPredicateCombinators(t *testing.T) {
	d := docOf(t, sampleEvent)

	all := Predicate{All: []Predicate{
		{Path: "seqId", Op: OpNeq, Value: 0},
		{Path: "direction", Op: OpEq, Value: "client_to_server"},
	}}
	if !all.Evaluate(d) {
		t.Error("all: want true")
	}

	any := Predicate{Any: []Predicate{
		{Path: "seqId", Op: OpEq, Value: 999},
		{Path: "direction", Op: OpEq, Value: "server_to_client"},
	}}
	if any.Evaluate(d) {
		t.Error("any: want false")
	}

	// 嵌套：any[ all ]
	nested := Predicate{Any: []Predicate{
		{All: []Predicate{
			{Path: "seqId", Op: OpGt, Value: 100},
			{Path: "error", Op: OpExists},
		}},
	}}
	if !nested.Evaluate(d) {
		t.Error("nested any(all): want true")
	}
}

func TestPredicateValidate(t *testing.T) {
	cases := []struct {
		name   string
		p      Predicate
		ruleID string
	}{
		{"unknown op", Predicate{Path: "a", Op: "regex", Value: "x"}, OpUnknown},
		{"missing op", Predicate{Path: "a", Value: "x"}, OpRequired},
		{"missing path", Predicate{Op: OpEq, Value: 1}, PathRequired},
		{"missing value", Predicate{Path: "a", Op: OpEq}, ValueRequired},
		{"in needs array", Predicate{Path: "a", Op: OpIn, Value: "x"}, ValueArrayRequired},
		{"combinator mixed", Predicate{All: []Predicate{{Path: "a", Op: OpExists}}, Path: "b", Op: OpExists}, CombinatorMixed},
		{"all+any", Predicate{All: []Predicate{{Path: "a", Op: OpExists}}, Any: []Predicate{{Path: "b", Op: OpExists}}}, CombinatorMixed},
		{"empty all", Predicate{All: nil, Path: "a", Op: OpExists}, ""}, // 非组合器叶子，合法
	}
	for _, c := range cases {
		issues := c.p.validate()
		if c.ruleID == "" {
			if len(issues) != 0 {
				t.Errorf("%s: want no issues, got %v", c.name, issues)
			}
			continue
		}
		found := false
		for _, iss := range issues {
			if iss.RuleID == c.ruleID {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want issue %s, got %v", c.name, c.ruleID, issues)
		}
	}
}

func TestRuleValidate(t *testing.T) {
	good := Rule{
		ID: "game.pair_request_response",
		When: &Predicate{All: []Predicate{
			{Path: "seqId", Op: OpNeq, Value: 0},
			{Path: "direction", Op: OpIn, Value: []any{"client_to_server", "server_to_client"}},
		}},
		Effect: Effect{
			Type: EffectPair,
			Sides: []PairSide{
				{Key: "seqId", Predicate: Predicate{Path: "direction", Op: OpEq, Value: "client_to_server"}},
				{Key: "meta.req_seq", Predicate: Predicate{Path: "direction", Op: OpEq, Value: "server_to_client"}},
			},
		},
	}
	if issues := good.Validate(); len(issues) != 0 {
		t.Errorf("good pair rule: want no issues, got %v", issues)
	}

	// pair 某侧缺 key
	noKey := good
	noKey.Effect.Sides[0].Key = ""
	if issues := noKey.Validate(); len(issues) == 0 || issues[0].RuleID != PairKeyRequired {
		t.Errorf("pair side without key: want %s, got %v", PairKeyRequired, issues)
	}

	// pair sides=1
	oneSide := good
	oneSide.Effect.Sides = oneSide.Effect.Sides[:1]
	if issues := oneSide.Validate(); len(issues) == 0 || issues[0].RuleID != PairSidesLimit {
		t.Errorf("pair with 1 side: want %s, got %v", PairSidesLimit, issues)
	}

	// id 格式：拒绝 kebab
	badID := good
	badID.ID = "game.pair-request"
	if issues := badID.Validate(); len(issues) == 0 || issues[0].RuleID != RuleIDFormat {
		t.Errorf("kebab rule id: want %s, got %v", RuleIDFormat, issues)
	}

	// 历史 extract 类型已删除：声明它必须报 effect-unknown
	gone := good
	gone.ID = "game.extract_sync_db"
	gone.Effect = Effect{Type: "extract"}
	if issues := gone.Validate(); len(issues) == 0 || issues[0].RuleID != EffectUnknown {
		t.Errorf("removed extract effect: want %s, got %v", EffectUnknown, issues)
	}

	// annotate 未知 semantic
	badAnn := Rule{
		ID:     "game.mark_x",
		When:   &Predicate{Path: "error", Op: OpExists},
		Effect: Effect{Type: EffectAnnotate, Semantic: "warning"},
	}
	if issues := badAnn.Validate(); len(issues) == 0 || issues[0].RuleID != AnnotateSemanticUnknown {
		t.Errorf("bad annotate: want %s, got %v", AnnotateSemanticUnknown, issues)
	}

	// 缺 when
	noWhen := good
	noWhen.When = nil
	if issues := noWhen.Validate(); len(issues) == 0 || issues[0].RuleID != WhenRequired {
		t.Errorf("rule without when: want %s, got %v", WhenRequired, issues)
	}
}

func TestRulesReportDuplicate(t *testing.T) {
	r := Rule{ID: "game.x", When: &Predicate{Path: "a", Op: OpExists}, Effect: Effect{Type: EffectAnnotate, Semantic: SemError}}
	issues := RulesReport([]Rule{r, r})
	found := false
	for _, iss := range issues {
		if iss.RuleID == RuleIDDuplicate {
			found = true
		}
	}
	if !found {
		t.Errorf("want duplicate issue, got %v", issues)
	}
}

// TestEvaluateSpecExample 复现方案 §12 的例子：三条规则命中后
// 平台得到 Request↔Response 配对与 notification/error 标注。
func TestEvaluateSpecExample(t *testing.T) {
	rules := []Rule{
		{
			ID: "game.pair_request_response",
			When: &Predicate{All: []Predicate{
				{Path: "seqId", Op: OpNeq, Value: 0},
				{Path: "direction", Op: OpIn, Value: []any{"client_to_server", "server_to_client"}},
			}},
			Effect: Effect{Type: EffectPair, Sides: []PairSide{
				{Key: "seqId", Predicate: Predicate{Path: "direction", Op: OpEq, Value: "client_to_server"}},
				// 响应侧的配对键在不同位置（meta.req_seq）：per-side key 保证两侧字段结构不同也能配对。
				{Key: "meta.req_seq", Predicate: Predicate{Path: "direction", Op: OpEq, Value: "server_to_client"}},
			}},
		},
		{
			ID:     "game.mark_push",
			When:   &Predicate{Path: "seqId", Op: OpEq, Value: 0},
			Effect: Effect{Type: EffectAnnotate, Semantic: SemNotification},
		},
		{
			ID:     "game.mark_error",
			When:   &Predicate{Path: "error", Op: OpExists},
			Effect: Effect{Type: EffectAnnotate, Semantic: SemError},
		},
	}

	req := mustValue(t, `{"uri":"/battle/attack","seqId":123,"direction":"client_to_server","data":{"targetId":1001}}`)
	// 响应的配对键不在 seqId，而在 meta.req_seq：验证 per-side key 跨字段配对。
	resp := mustValue(t, `{"uri":"/battle/attack","seqId":123,"meta":{"req_seq":123},"direction":"server_to_client","error":"hp insufficient","data":{"SyncDbData":[{"player":{"id":1}},{"item":{"id":2}}]}}`)
	push := mustValue(t, `{"uri":"/push/hp","seqId":0,"direction":"server_to_client"}`)

	reqRes, err := Evaluate(rules, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqRes.Pairs) != 1 || reqRes.Pairs[0].Key != "123" || reqRes.Pairs[0].Side != 0 {
		t.Errorf("request pair: got %+v", reqRes.Pairs)
	}
	if len(reqRes.Semantics) != 0 {
		t.Errorf("request should be bare: %+v", reqRes)
	}

	respRes, err := Evaluate(rules, resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(respRes.Pairs) != 1 || respRes.Pairs[0].Key != "123" || respRes.Pairs[0].Side != 1 {
		t.Errorf("response pair: got %+v", respRes.Pairs)
	}
	if len(respRes.Semantics) != 1 || respRes.Semantics[0] != SemError {
		t.Errorf("response semantics: got %+v", respRes.Semantics)
	}

	// 配对判定：request/response 各占一个 side，可配对；两个请求不可配对。
	if !MatchPair(reqRes.Pairs[0], respRes.Pairs[0]) {
		t.Error("MatchPair(req, resp): want true")
	}
	if MatchPair(reqRes.Pairs[0], reqRes.Pairs[0]) {
		t.Error("MatchPair(req, req): want false")
	}

	pushRes, err := Evaluate(rules, push)
	if err != nil {
		t.Fatal(err)
	}
	// seqId=0 被 pair 规则的 when 排除，命中 notification。
	if len(pushRes.Semantics) != 1 || pushRes.Semantics[0] != SemNotification {
		t.Errorf("push semantics: got %+v", pushRes.Semantics)
	}
	if len(pushRes.Pairs) != 0 {
		t.Errorf("push should not pair: got %+v", pushRes.Pairs)
	}
}

func mustValue(t *testing.T, json string) event.Value {
	t.Helper()
	v, err := event.ValueFromJSON([]byte(json))
	if err != nil {
		t.Fatalf("ValueFromJSON: %v", err)
	}
	return v
}

// TestPairSideYAMLUnmarshal 验证 per-side key 的 YAML 解析：
// key 归 PairSide，其余字段（含 all 组合器）归角色判定 Predicate。
func TestPairSideYAMLUnmarshal(t *testing.T) {
	yamlText := `type: pair
sides:
  - { path: direction, op: eq, value: client_to_server, key: seqId }
  - { all: [ { path: direction, op: eq, value: server_to_client } ], key: meta.req_seq }
`
	var e Effect
	if err := yaml.Unmarshal([]byte(yamlText), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.Type != EffectPair || len(e.Sides) != 2 {
		t.Fatalf("effect = %+v", e)
	}
	if e.Sides[0].Key != "seqId" || e.Sides[0].Path != "direction" || e.Sides[0].Op != OpEq {
		t.Errorf("sides[0] = %+v", e.Sides[0])
	}
	if e.Sides[1].Key != "meta.req_seq" || len(e.Sides[1].All) != 1 || e.Sides[1].All[0].Path != "direction" {
		t.Errorf("sides[1] = %+v", e.Sides[1])
	}
}

// TestPairSideYAMLRoundTrip 验证 PairSide 序列化往返无损：
// 序列化必须输出扁平形式（不得出现嵌套 predicate: 键），读回后角色判定完整。
func TestPairSideYAMLRoundTrip(t *testing.T) {
	effect := Effect{
		Type: EffectPair,
		Sides: []PairSide{
			{Predicate: Predicate{Path: "direction", Op: OpEq, Value: "client_to_server"}, Key: "seqId"},
			{Predicate: Predicate{All: []Predicate{{Path: "direction", Op: OpEq, Value: "server_to_client"}}}, Key: "meta.req_seq"},
		},
	}
	raw, err := yaml.Marshal(effect)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(raw, []byte("predicate:")) {
		t.Errorf("marshal must be flat, got nested predicate key:\n%s", raw)
	}

	var back Effect
	if err := yaml.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if len(back.Sides) != 2 {
		t.Fatalf("sides = %d, want 2", len(back.Sides))
	}
	s0 := back.Sides[0]
	if s0.Key != "seqId" || s0.Path != "direction" || s0.Op != OpEq || s0.Value != "client_to_server" {
		t.Errorf("round-trip sides[0] = %+v", s0)
	}
	s1 := back.Sides[1]
	if s1.Key != "meta.req_seq" || len(s1.All) != 1 || s1.All[0].Path != "direction" {
		t.Errorf("round-trip sides[1] = %+v", s1)
	}
}

// TestPairSideYAMLUnmarshal_Nested 验证兼容旧序列化器生成的嵌套 predicate 形式
// （yaml.Marshal 对嵌入结构体默认包 predicate: 键的历史输出）。
func TestPairSideYAMLUnmarshal_Nested(t *testing.T) {
	yamlText := `sides:
  - predicate:
      path: direction
      op: eq
      value: client_to_server
    key: seqId
`
	var e Effect
	if err := yaml.Unmarshal([]byte(yamlText), &e); err != nil {
		t.Fatalf("unmarshal nested: %v", err)
	}
	if len(e.Sides) != 1 {
		t.Fatalf("sides = %d, want 1", len(e.Sides))
	}
	s := e.Sides[0]
	if s.Key != "seqId" || s.Path != "direction" || s.Op != OpEq || s.Value != "client_to_server" {
		t.Errorf("nested side = %+v", s)
	}
	if !s.Evaluate(gjson.Parse(`{"direction":"client_to_server"}`)) {
		t.Error("nested side predicate should evaluate true")
	}
}

func TestRuleValidateName(t *testing.T) {
	good := Rule{
		ID:     "game.name_from_type",
		When:   &Predicate{Path: "msg_type", Op: OpExists},
		Effect: Effect{Type: EffectName, Key: "msg_type"},
	}
	if issues := good.Validate(); len(issues) != 0 {
		t.Errorf("good name rule: want no issues, got %v", issues)
	}

	noKey := good
	noKey.Effect.Key = ""
	if issues := noKey.Validate(); len(issues) == 0 || issues[0].RuleID != NameKeyRequired {
		t.Errorf("name without key: want %s, got %v", NameKeyRequired, issues)
	}
}

// TestEvaluateNameInjectsMeta 验证 name 效果：从 payload 提取消息名写入
// _meta.msg_name 求值视图，供同事件内 annotate/pair 规则的 _meta.msg_name 谓词使用。
func TestEvaluateNameInjectsMeta(t *testing.T) {
	rules := []Rule{
		{
			ID:     "game.name_from_type",
			When:   &Predicate{Path: "msg_type", Op: OpExists},
			Effect: Effect{Type: EffectName, Key: "msg_type"},
		},
		{
			ID:     "game.mark_request",
			When:   &Predicate{Path: "_meta.msg_name", Op: OpEq, Value: "LoginRequest"},
			Effect: Effect{Type: EffectAnnotate, Semantic: SemRequest},
		},
	}

	// payload 无 _meta.msg_name：msg_name 由 name 规则提取并注入求值视图。
	v := mustValue(t, `{"msg_type":"LoginRequest","account":"a"}`)
	res, err := Evaluate(rules, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Names) != 1 || res.Names[0].Value != "LoginRequest" {
		t.Errorf("names: got %+v", res.Names)
	}
	if len(res.Semantics) != 1 || res.Semantics[0] != SemRequest {
		t.Errorf("semantics: got %+v (injection failed?)", res.Semantics)
	}

	// 无可提取字段：name 不命中，后续规则也无注入可用。
	other := mustValue(t, `{"account":"a"}`)
	res2, err := Evaluate(rules, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Names) != 0 || len(res2.Semantics) != 0 {
		t.Errorf("bare payload: got %+v", res2)
	}

	// 已有 _meta.msg_name 时被 name 提取值覆盖（规则优先于解码器硬编码）。
	withMeta := mustValue(t, `{"msg_type":"LoginRequest","_meta":{"msg_name":"OldName"}}`)
	res3, err := Evaluate(rules, withMeta)
	if err != nil {
		t.Fatal(err)
	}
	if len(res3.Names) != 1 || res3.Names[0].Value != "LoginRequest" {
		t.Errorf("names with existing meta: got %+v", res3.Names)
	}
	if len(res3.Semantics) != 1 || res3.Semantics[0] != SemRequest {
		t.Errorf("semantics with existing meta: got %+v (injection failed?)", res3.Semantics)
	}
}
