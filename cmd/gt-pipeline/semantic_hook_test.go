package main

import (
	"io"
	"log/slog"
	"testing"

	sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/rule"
	"gametrace/pkg/event"
)

// 用 wesnoth 真实上报里的 msg_type:"turn" 负载，走通语义 name 规则，
// 验证 msg_name 是否被写入事件 Meta（复现前端 "unknown" 问题）。
func TestSemanticMsgNameFromTurn(t *testing.T) {
	const manifest = `api_version: gt.decoder/v2
name: gt-wesnoth-decoder
protocol: wesnoth
type: decoder
semantic_rules:
  - id: wesnoth.name_msg
    when:
      all:
        - path: msg_type
          op: exists
    effect:
      type: name
      key: msg_type
`
	m, err := sdk.ParseManifest([]byte(manifest))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(m.SemanticRules) == 0 {
		t.Fatal("no semantic rules parsed")
	}

	e := newSemanticEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	e.setRules(m.SemanticRules)

	// 与真实事件一致的负载：data 顶层含 msg_type
	payload := event.ValueObject(map[string]event.Value{
		"from_side": event.ValueString("1"),
		"move": event.ValueObject(map[string]event.Value{
			"x": event.ValueString("29,30,31"),
			"y": event.ValueString("18,17,17"),
		}),
		"msg_type": event.ValueString("turn"),
	})
	ev := event.NewEvent("test_sess", event.EventType("wesnoth.message"), "", payload, event.EventContext{})

	e.enrichSemantics(ev)

	got, ok := ev.MetaValue("msg_name")
	if !ok {
		t.Fatalf("msg_name not written into Meta; full meta=%v", ev.Meta)
	}
	if s, ok := got.AsString(); !ok || s != "turn" {
		t.Fatalf("msg_name = %v, want 'turn'", got)
	}
	t.Logf("OK: msg_name = %q", got)
}

// TestApplyPairsDirectionBySide 验证 pair 配对的请求/响应方向由 sides 下标
// （Side 0 = 请求方、Side 1 = 响应方）决定，而非到达顺序——响应先到也能得到
// 正确的 causation_id（响应方 → 请求方）与 correlation_id。
func TestApplyPairsDirectionBySide(t *testing.T) {
	e := newSemanticEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	mk := func(tag string) *event.Event {
		return event.NewEvent("sess", event.EventType("wesnoth.message"), "",
			event.ValueObject(map[string]event.Value{"x": event.ValueString(tag)}), event.EventContext{})
	}
	req := mk("req")   // Side 0 = 请求方
	resp := mk("resp") // Side 1 = 响应方

	// 响应先到、请求后到：方向仍按 Side 判定，与到达顺序无关。
	e.applyPairs(resp, []rule.PairHit{{RuleID: "wesnoth.pair", Key: "42", Side: 1}})
	e.applyPairs(req, []rule.PairHit{{RuleID: "wesnoth.pair", Key: "42", Side: 0}})

	if resp.Trace.CausationID != req.Identity.ID {
		t.Fatalf("resp.CausationID = %q, want %q（响应方应指向请求方）", resp.Trace.CausationID, req.Identity.ID)
	}
	if req.Trace.CausationID != "" {
		t.Fatalf("req.CausationID = %q, want empty（请求方无前驱）", req.Trace.CausationID)
	}
	if corr := string(req.Identity.ID); req.Trace.CorrelationID != corr || resp.Trace.CorrelationID != corr {
		t.Fatalf("correlation 应等于请求方 id：req=%q resp=%q want %q", req.Trace.CorrelationID, resp.Trace.CorrelationID, corr)
	}
}