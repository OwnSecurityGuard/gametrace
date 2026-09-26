package checkrule

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
)

func leaf(path string, op rule.Op, v any) rule.Predicate {
	return rule.Predicate{Path: path, Op: op, Value: v}
}

func TestValidateActiveRule(t *testing.T) {
	r := CheckRule{ID: "a", Name: "login", Enabled: true, When: leaf("type", rule.OpEq, "login")}
	if issues := r.Validate(); HasError(issues) {
		t.Fatalf("valid rule reported errors: %+v", issues)
	}
}

func TestValidateRulesReport(t *testing.T) {
	cases := []struct {
		name   string
		rules  []CheckRule
		wantID string
	}{
		{"missing id", []CheckRule{{Name: "n", Enabled: true, When: leaf("t", rule.OpEq, "x")}}, IDRequired},
		{"missing name", []CheckRule{{ID: "a", Enabled: true, When: leaf("t", rule.OpEq, "x")}}, NameRequired},
		{"enabled without when", []CheckRule{{ID: "a", Name: "n", Enabled: true}}, WhenRequired},
		{"bad op", []CheckRule{{ID: "a", Name: "n", Enabled: true, When: leaf("t", rule.Op("nope"), "x")}}, WhenInvalid},
		{"dup id", []CheckRule{
			{ID: "a", Name: "n1", Enabled: true, When: leaf("t", rule.OpEq, "x")},
			{ID: "a", Name: "n2", Enabled: true, When: leaf("t", rule.OpEq, "y")},
		}, IDDuplicate},
		{"cooldown too big", []CheckRule{{ID: "a", Name: "n", Enabled: true,
			When: leaf("t", rule.OpEq, "x"), CooldownSec: 99999999}}, CooldownRange},
		{"context too big", []CheckRule{{ID: "a", Name: "n", Enabled: true,
			When: leaf("t", rule.OpEq, "x"), ContextPerDirection: MaxContext + 1}}, ContextRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := RulesReport(tc.rules)
			if !HasError(issues) {
				t.Fatalf("expected error-level issue %q, got none", tc.wantID)
			}
			var found bool
			for _, iss := range issues {
				if iss.RuleID == tc.wantID {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected issue %q, got %+v", tc.wantID, issues)
			}
		})
	}
}

func TestRulesReportTooMany(t *testing.T) {
	rules := make([]CheckRule, MaxRulesPerProject+1)
	for i := range rules {
		rules[i] = CheckRule{ID: string(rune('a'+i%26)) + itoa(i), Name: "n",
			Enabled: true, When: leaf("t", rule.OpEq, "x")}
	}
	if !HasError(RulesReport(rules)) {
		t.Fatal("expected too-many-rules error")
	}
}

func TestDisabledRuleAllowsEmptyWhen(t *testing.T) {
	// 禁用规则允许 when 为空（草稿/占位），不应被校验拦下。
	issues := CheckRule{ID: "a", Name: "n", Enabled: false}.Validate()
	if HasError(issues) {
		t.Fatalf("disabled rule with empty when should validate, got %+v", issues)
	}
}

func TestActiveFilters(t *testing.T) {
	in := []CheckRule{
		{ID: "on", Name: "n", Enabled: true, When: leaf("t", rule.OpEq, "x")},
		{ID: "off", Name: "n", Enabled: false, When: leaf("t", rule.OpEq, "x")},
		{ID: "empty", Name: "n", Enabled: true}, // enabled but no when → 不激活
		{ID: "legacy", Name: "n"},               // 旧 {id,name}：enabled=false + 空 when
	}
	got := Active(in)
	if len(got) != 1 || got[0].ID != "on" {
		t.Fatalf("Active() = %+v, want only [on]", got)
	}
}

func TestEncodeDecodeListRoundTrip(t *testing.T) {
	in := []CheckRule{
		{ID: "a", Name: "login", Enabled: true, When: leaf("type", rule.OpEq, "login"), CooldownSec: 45, ContextPerDirection: 5},
		{ID: "b", Name: "combo", Enabled: true, When: rule.Predicate{Any: []rule.Predicate{
			leaf("type", rule.OpEq, "x"), leaf("code", rule.OpGt, 0),
		}}},
	}
	enc := EncodeList(in)
	if len(enc) != 2 {
		t.Fatalf("EncodeList len=%d, want 2", len(enc))
	}
	out, dropped := DecodeList(append(enc, "{not json"))
	if dropped != 1 {
		t.Fatalf("dropped=%d, want 1 (bad entry skipped)", dropped)
	}
	if len(out) != 2 || out[0].ID != "a" || out[1].ID != "b" {
		t.Fatalf("round-trip lost rules: %+v", out)
	}
	if out[0].CooldownSec != 45 || out[0].ContextPerDirection != 5 {
		t.Fatalf("cooldown/context not preserved: %+v", out[0])
	}
	if len(out[1].When.Any) != 2 {
		t.Fatalf("combinator not preserved: %+v", out[1].When)
	}
}

func TestPredicateJSONShorthand(t *testing.T) {
	// 数组简写 → 隐式 all（与 UnmarshalYAML 对称）。
	var p rule.Predicate
	if err := json.Unmarshal([]byte(`[{"path":"t","op":"eq","value":"a"},{"path":"c","op":"gt","value":0}]`), &p); err != nil {
		t.Fatalf("unmarshal shorthand: %v", err)
	}
	if len(p.All) != 2 {
		t.Fatalf("shorthand not folded into All: %+v", p)
	}
}

func TestMatch(t *testing.T) {
	doc := []byte(`{"type":"login","data":{"code":7},"_meta":{"msg_name":"LoginReq"}}`)
	if !Match(leaf("type", rule.OpEq, "login"), doc) {
		t.Fatal("expected type==login to match")
	}
	if !Match(leaf("data.code", rule.OpGt, 5), doc) {
		t.Fatal("expected data.code>5 to match")
	}
	if !Match(leaf("_meta.msg_name", rule.OpSuffix, "Req"), doc) {
		t.Fatal("expected _meta.msg_name suffix match")
	}
	if Match(leaf("type", rule.OpEq, "logout"), doc) {
		t.Fatal("type==logout should not match")
	}
	if Match(rule.Predicate{}, doc) {
		t.Fatal("empty predicate must never match")
	}
	if Match(leaf("type", rule.OpEq, "login"), nil) {
		t.Fatal("empty doc must not match")
	}
}

func TestDefaultsAndFallbacks(t *testing.T) {
	r := CheckRule{ID: "a", Name: "login"}
	if r.Cooldown() != DefaultCooldown {
		t.Fatalf("cooldown default = %v, want %v", r.Cooldown(), DefaultCooldown)
	}
	if r.Context() != DefaultContext {
		t.Fatalf("context default = %d, want %d", r.Context(), DefaultContext)
	}
	if r.NotifyTitle() != "login" {
		t.Fatalf("NotifyTitle fallback to name = %q", r.NotifyTitle())
	}
	if !strings.Contains(r.NotifyMessage(), "login") {
		t.Fatalf("NotifyMessage should embed name: %q", r.NotifyMessage())
	}
	r2 := CheckRule{ID: "b", Title: "T", Message: "M"}
	if r2.NotifyTitle() != "T" || r2.NotifyMessage() != "M" {
		t.Fatal("explicit title/message should win")
	}
}

func TestTruncateData(t *testing.T) {
	small := TruncateData([]byte(`{"a":1}`), 2048)
	if _, ok := small.(json.RawMessage); !ok {
		t.Fatalf("small data should stay json.RawMessage, got %T", small)
	}
	big := TruncateData([]byte(strings.Repeat("x", 5000)), 100)
	s, ok := big.(string)
	if !ok || !strings.Contains(s, "truncated") {
		t.Fatalf("oversized data should truncate to marker string, got %T %q", big, s)
	}
	if TruncateData(nil, 100) != nil {
		t.Fatal("nil raw should map to nil")
	}
}

func TestAlertBundleJSONRoundTrip(t *testing.T) {
	b := AlertBundle{
		AlertID: "al1", RuleID: "r1", RuleName: "login", SessionID: "s1",
		Title: "命中", Message: "登录消息", GeneratedAt: "2026-09-26T00:00:00Z",
		Trigger: AlertRecord{ID: "e1", Type: "game.login", Direction: "request", Data: json.RawMessage(`{"u":"a"}`)},
		Context: map[string][]AlertRecord{"response": {{ID: "e2", Type: "game.login_ack", Direction: "response"}}},
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back AlertBundle
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.AlertID != "al1" || back.Trigger.ID != "e1" || len(back.Context["response"]) != 1 {
		t.Fatalf("round-trip lost fields: %+v", back)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}
