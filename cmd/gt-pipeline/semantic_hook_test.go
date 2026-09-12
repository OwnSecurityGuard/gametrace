package main

import (
	"io"
	"log/slog"
	"testing"

	sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
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