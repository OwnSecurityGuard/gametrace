package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"gametrace/pkg/event"
	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
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

// TestApplyPairsPreservesBusinessCorrelationKey 验证插件声明的业务会话标识
// （Draft.CorrelationKey）不会被 pair 的一问一答分组键静默吃掉：覆盖前转存到
// Meta.corr_key。连接身份归 ConnID，CorrelationKey 只承载业务语义（F11）。
func TestApplyPairsPreservesBusinessCorrelationKey(t *testing.T) {
	e := newSemanticEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	mk := func(businessKey string) *event.Event {
		ev := event.NewEvent("sess", event.EventType("game.message"), "",
			event.ValueObject(map[string]event.Value{"x": event.ValueString("v")}),
			event.EventContext{ConnID: "tcp:10.0.0.2:50000<->10.0.0.1:9250#1"})
		ev.Trace.CorrelationID = businessKey // 宿主从 Draft.CorrelationKey 写入
		return ev
	}
	req := mk("battle-42")
	resp := mk("battle-42")

	e.applyPairs(req, []rule.PairHit{{RuleID: "game.pair", Key: "1", Side: 0}})
	e.applyPairs(resp, []rule.PairHit{{RuleID: "game.pair", Key: "1", Side: 1}})

	// CorrelationID 收敛为更紧的一问一答分组键（请求方事件 ID）。
	if corr := string(req.Identity.ID); req.Trace.CorrelationID != corr || resp.Trace.CorrelationID != corr {
		t.Fatalf("CorrelationID 应为请求方 id：req=%q resp=%q want %q", req.Trace.CorrelationID, resp.Trace.CorrelationID, corr)
	}
	// 业务会话标识转到 Meta.corr_key，不丢。
	for _, ev := range []*event.Event{req, resp} {
		got, ok := ev.MetaValue("corr_key")
		if !ok {
			t.Fatalf("业务关联键丢失，Meta=%v", ev.Meta)
		}
		if s, _ := got.AsString(); s != "battle-42" {
			t.Fatalf("corr_key = %q, want battle-42", s)
		}
	}
}

// TestApplyPairsShardedByConnection 验证配对池按连接分片：两条连接用同样的
// per-connection 配对键（各自从 1 开始递增的 seq）时不会跨连接错配。
// 分片前：B 的同键请求会覆盖 A 的，A 的响应最终配到 B 的请求上（F6）。
func TestApplyPairsShardedByConnection(t *testing.T) {
	e := newSemanticEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	mk := func(conn, tag string) *event.Event {
		return event.NewEvent("sess", event.EventType("game.message"), "",
			event.ValueObject(map[string]event.Value{"x": event.ValueString(tag)}),
			event.EventContext{ConnID: conn})
	}

	// 连接 A 与连接 B 都有 seq=1 的请求。
	reqA := mk("tcp:10.0.0.2:50000<->10.0.0.1:9250#1", "reqA")
	reqB := mk("tcp:10.0.0.2:50001<->10.0.0.1:9250#2", "reqB")
	respA := mk("tcp:10.0.0.2:50000<->10.0.0.1:9250#1", "respA")

	e.applyPairs(reqA, []rule.PairHit{{RuleID: "game.pair", Key: "1", Side: 0}})
	e.applyPairs(reqB, []rule.PairHit{{RuleID: "game.pair", Key: "1", Side: 0}})
	e.applyPairs(respA, []rule.PairHit{{RuleID: "game.pair", Key: "1", Side: 1}})

	// A 的响应必须配到 A 的请求上。
	if respA.Trace.CausationID != reqA.Identity.ID {
		t.Fatalf("respA.CausationID = %q, want reqA %q（跨连接错配）", respA.Trace.CausationID, reqA.Identity.ID)
	}
	if reqB.Trace.CorrelationID != "" {
		t.Fatalf("reqB 不应被配对，CorrelationID = %q", reqB.Trace.CorrelationID)
	}
	// B 的请求仍在待配对池里（A 的配对没有消费掉 B）。
	if _, ok := e.pending["tcp:10.0.0.2:50001<->10.0.0.1:9250#2\x00game.pair\x001"]; !ok {
		t.Fatalf("B 的待配对项被 A 的响应消费了，pending=%v", e.pending)
	}
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

// semanticsOf 取事件 Meta 里的 annotate 标签集合（前端徽章与语义过滤的唯一来源）。
func semanticsOf(t *testing.T, ev *event.Event) []string {
	t.Helper()
	v, ok := ev.MetaValue("semantic")
	if !ok {
		return nil
	}
	arr, ok := v.AsArray()
	if !ok {
		t.Fatalf("meta.semantic 不是数组：%v", v)
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, _ := item.AsString()
		out = append(out, s)
	}
	return out
}

// TestSemanticRulesOnLPTraffic 拿真实 lp-decoder manifest 跑通宿主语义链路：
// lp 的 pair 规则依赖解码器 Meta 里的 direction，annotate 规则依赖 payload 的
// cmd / error_code 与 Meta 的 role。这几环任一处断掉，前端就会同时出现
// 「展开看不到一问一答」和「语义标签一个都没有」——所以这条测试是那两个症状的后端侧护栏。
func TestSemanticRulesOnLPTraffic(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "lp-decoder", "plugin.yaml"))
	if err != nil {
		t.Fatalf("read lp manifest: %v", err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse lp manifest: %v", err)
	}
	e := newSemanticEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	e.setRules(m.SemanticRules)
	if len(m.SemanticRules) == 0 {
		t.Fatal("lp manifest 没有 semantic_rules")
	}

	const conn = "tcp:127.0.0.1:40001<->127.0.0.1:8998#1"
	mk := func(cmd, seq, errCode int, direction, role string) *event.Event {
		ev := event.NewEvent("sess", event.EventType("lp.message"), "",
			event.ValueObject(map[string]event.Value{
				"cmd":        event.ValueInt(int64(cmd)),
				"seq":        event.ValueInt(int64(seq)),
				"error_code": event.ValueInt(int64(errCode)),
			}),
			event.EventContext{ConnID: conn})
		ev.Meta = event.ValueObject(map[string]event.Value{
			"direction": event.ValueString(direction),
			"role":      event.ValueString(role),
			"flow_id":   event.ValueString("tcp 127.0.0.1:40001=127.0.0.1:8998"),
		})
		return ev
	}

	// ① 登录请求 → 响应：按 seq 回显配对，响应 causation 指回请求；两侧各带角色标签。
	req := mk(1001, 1, 0, "client_to_server", "request")
	resp := mk(1002, 1, 0, "server_to_client", "response")
	e.enrichSemantics(req)
	e.enrichSemantics(resp)
	if resp.Trace.CausationID != req.Identity.ID {
		t.Fatalf("lp 请求/响应未配对：resp.CausationID=%q want %q", resp.Trace.CausationID, req.Identity.ID)
	}
	if corr := string(req.Identity.ID); req.Trace.CorrelationID != corr || resp.Trace.CorrelationID != corr {
		t.Fatalf("correlation 未收敛到请求方 id：req=%q resp=%q", req.Trace.CorrelationID, resp.Trace.CorrelationID)
	}
	assertLabels(t, semanticsOf(t, req), "request")
	assertLabels(t, semanticsOf(t, resp), "response")

	// ② 道具数量推送（seq=0）：命中 push 号段 → notification，且不参与配对。
	// role 是 push 而非 response，否则推送会被标成响应。
	push := mk(2002, 0, 0, "server_to_client", "push")
	e.enrichSemantics(push)
	assertLabels(t, semanticsOf(t, push), "notification")
	if push.Trace.CausationID != "" {
		t.Fatalf("推送不应被配对，causation_id = %q", push.Trace.CausationID)
	}

	// ③ 错误回包（error_code!=0）：带着错误码的响应，response 与 error 并存。
	bad := mk(1006, 2, 7, "server_to_client", "response")
	e.enrichSemantics(bad)
	assertLabels(t, semanticsOf(t, bad), "response", "error")
}

// assertLabels 比对 annotate 标签集合（顺序无关）。
func assertLabels(t *testing.T, got []string, want ...string) {
	t.Helper()
	seen := make(map[string]bool, len(got))
	for _, g := range got {
		seen[g] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("语义标签 = %v, want %v", got, want)
	}
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("语义标签 = %v, want %v", got, want)
		}
	}
}
