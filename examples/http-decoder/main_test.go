package main

import (
	"fmt"
	"os"
	"testing"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	sdkcontract "github.com/OwnSecurityGuard/gametrace/sdk/contract"
	sdkevent "github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
	"google.golang.org/grpc"
)

// captureStream is a minimal Decoder_DecodeV2Server mock that records Send calls.
type captureStream struct {
	grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]
	responses []*pb.DecodeResponseV2
}

func (c *captureStream) Send(r *pb.DecodeResponseV2) error {
	c.responses = append(c.responses, r)
	return nil
}

func loadManifest(t *testing.T) *sdk.Manifest {
	t.Helper()
	raw, err := os.ReadFile("plugin.yaml")
	if err != nil {
		t.Fatalf("read plugin.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
	return m
}

// TestManifestContractCheck runs the declaration-phase contract check on
// plugin.yaml: the semantic_rules declaration must be violation-free.
func TestManifestContractCheck(t *testing.T) {
	m := loadManifest(t)
	if len(m.SemanticRules) == 0 {
		t.Fatal("plugin.yaml must declare semantic_rules (v0.9.0 contract)")
	}
	rep := sdkcontract.NewPluginChecker().Check(m)
	for _, v := range rep.Violations {
		t.Errorf("contract %s: %s", v.RuleID, v.Message)
	}
}

func TestParseMessageRequest(t *testing.T) {
	body := `{"header":{"cmd":1001},"body":{"seq":1234}}`
	raw := fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: 127.0.0.1:8984\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	msg, n, ok := parseMessage([]byte(raw))
	if !ok {
		t.Fatal("expected request to parse")
	}
	if n != len(raw) {
		t.Fatalf("consumed = %d, want %d", n, len(raw))
	}
	if !msg.isRequest || msg.method != "POST" || msg.path != "/echo" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if string(msg.body) != body {
		t.Fatalf("unexpected body: %q", msg.body)
	}
}

func TestParseMessageResponse(t *testing.T) {
	body := `{"header":{"cmd":1002},"body":{"seq":1234}}`
	raw := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	msg, n, ok := parseMessage([]byte(raw))
	if !ok {
		t.Fatal("expected response to parse")
	}
	if n != len(raw) {
		t.Fatalf("consumed = %d, want %d", n, len(raw))
	}
	if msg.isRequest || msg.status != 200 {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if string(msg.body) != body {
		t.Fatalf("unexpected body: %q", msg.body)
	}
}

func TestParseMessageIncomplete(t *testing.T) {
	raw := "POST /echo HTTP/1.1\r\nHost: 127.0.0.1:8984\r\nContent-Length: 39\r\n\r\n" +
		`{"header":{"cm`
	_, _, ok := parseMessage([]byte(raw))
	if ok {
		t.Fatal("incomplete message must not parse")
	}
}

func TestParseMessageTwoInOneBuffer(t *testing.T) {
	first := "POST /a HTTP/1.1\r\nHost: h\r\nContent-Length: 2\r\n\r\n{}"
	second := "POST /b HTTP/1.1\r\nHost: h\r\nContent-Length: 2\r\n\r\n{}"
	buf := []byte(first + second)
	_, n, ok := parseMessage(buf)
	if !ok || n != len(first) {
		t.Fatalf("first message: ok=%v consumed=%d want=%d", ok, n, len(first))
	}
	_, n2, ok2 := parseMessage(buf[n:])
	if !ok2 || n2 != len(second) {
		t.Fatalf("second message: ok=%v consumed=%d want=%d", ok2, n2, len(second))
	}
}

func TestParseEnvelopeFormats(t *testing.T) {
	cases := []struct {
		name string
		body string
		want envelopeSemantics
	}{
		{"login_request", `{"header":{"cmd":1001},"body":{"seq":1234}}`,
			envelopeSemantics{Cmd: 1001, MsgName: "LoginRequest", IsPush: false, Seq: 1234, IsError: false}},
		{"login_response", `{"header":{"cmd":1002},"body":{"seq":1234}}`,
			envelopeSemantics{Cmd: 1002, MsgName: "LoginResponse", IsPush: false, Seq: 1234, IsError: false}},
		{"push_by_cmd", `{"header":{"cmd":2001},"body":{"seq":0}}`,
			envelopeSemantics{Cmd: 2001, MsgName: "PlayerNotify", IsPush: true, Seq: 0, IsError: false}},
		{"push_by_seq_zero", `{"header":{"cmd":1001},"body":{"seq":0}}`,
			envelopeSemantics{Cmd: 1001, MsgName: "LoginRequest", IsPush: true, Seq: 0, IsError: false}},
		{"error", `{"header":{"cmd":1002},"body":{"seq":1234,"error_code":1}}`,
			envelopeSemantics{Cmd: 1002, MsgName: "LoginResponse", IsPush: false, Seq: 1234, ErrorCode: 1, IsError: true}},
		{"success_with_error_code_zero", `{"header":{"cmd":1002},"body":{"seq":1234,"error_code":0}}`,
			envelopeSemantics{Cmd: 1002, MsgName: "LoginResponse", IsPush: false, Seq: 1234, IsError: false}},
		{"unknown_empty", `{}`,
			envelopeSemantics{Cmd: 0, MsgName: "unknown", IsPush: true, IsError: false}},
	}
	for _, tc := range cases {
		got := parseEnvelope([]byte(tc.body))
		if got != tc.want {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
}

// evalView 模拟宿主的规则求值视图（semantic_hook.go withMetaObject）：
// payload 合并 _meta，规则按 _meta.* 路径访问 Meta 通道字段。
func evalView(payload, meta map[string]any) sdkevent.Value {
	merged := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		merged[k] = v
	}
	merged["_meta"] = meta
	return sdkevent.ValueFromMap(merged)
}

// TestSemanticRulesHostBehavior 复现宿主对 semantic_rules 的执行路径：
// 求值视图（payload+_meta）→ rule.Evaluate → 校验 pair 命中可配对、
// annotate 语义正确、推送/错误事件不参与配对。
func TestSemanticRulesHostBehavior(t *testing.T) {
	m := loadManifest(t)

	eval := func(payload, meta map[string]any) rule.Result {
		t.Helper()
		res, err := rule.Evaluate(m.SemanticRules, evalView(payload, meta))
		if err != nil {
			t.Fatalf("rule.Evaluate: %v", err)
		}
		return res
	}

	req := eval(
		map[string]any{"cmd": int64(1001), "seq": int64(1234), "error_code": int64(0)},
		map[string]any{"direction": "client_to_server"},
	)
	if len(req.Pairs) != 1 || req.Pairs[0].Side != 0 || req.Pairs[0].Key != "1234" {
		t.Fatalf("request pair hit = %+v, want side 0 key 1234", req.Pairs)
	}
	if len(req.Semantics) != 0 {
		t.Fatalf("request semantics = %v, want none", req.Semantics)
	}

	resp := eval(
		map[string]any{"cmd": int64(1002), "seq": int64(1234), "error_code": int64(1)},
		map[string]any{"direction": "server_to_client"},
	)
	if len(resp.Pairs) != 1 || resp.Pairs[0].Side != 1 || resp.Pairs[0].Key != "1234" {
		t.Fatalf("response pair hit = %+v, want side 1 key 1234", resp.Pairs)
	}
	if !rule.MatchPair(req.Pairs[0], resp.Pairs[0]) {
		t.Fatalf("request/response pair hits must match: %+v vs %+v", req.Pairs[0], resp.Pairs[0])
	}
	if !hasSemantic(resp.Semantics, rule.SemError) {
		t.Fatalf("response semantics = %v, want error", resp.Semantics)
	}

	push := eval(
		map[string]any{"cmd": int64(2001), "seq": int64(0), "error_code": int64(0)},
		map[string]any{"direction": "client_to_server"},
	)
	if len(push.Pairs) != 0 {
		t.Fatalf("push message must not participate in pairing, got %+v", push.Pairs)
	}
	if !hasSemantic(push.Semantics, rule.SemNotification) {
		t.Fatalf("push semantics = %v, want notification", push.Semantics)
	}
}

func hasSemantic(list []rule.Semantic, want rule.Semantic) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestEmitSchemaConformance emits request and response events and runs the
// semantic rule checks against the declared manifest, so any drift
// between emit.go and plugin.yaml surfaces as a test failure. It also asserts
// the Payload / Meta / Analysis channel separation: business fields only in
// payload, flow_id/direction in meta, _state_changes in analysis.
func TestEmitSchemaConformance(t *testing.T) {
	m := loadManifest(t)
	pc := sdkcontract.NewPluginChecker()
	d := newDecoder()
	flowID := "tcp 127.0.0.1:12345=127.0.0.1:8984"

	req := &httpMessage{
		isRequest: true,
		method:    "POST",
		path:      "/echo",
		body:      []byte(`{"header":{"cmd":1001},"body":{"seq":1234}}`),
	}
	stream := &captureStream{}
	if err := d.emit(stream, "in-1", flowID, req); err != nil {
		t.Fatalf("emit request: %v", err)
	}

	resp := &httpMessage{
		isRequest: false,
		status:    200,
		body:      []byte(`{"header":{"cmd":1002},"body":{"seq":1234,"error_code":1}}`),
	}
	if err := d.emit(stream, "in-2", flowID, resp); err != nil {
		t.Fatalf("emit response: %v", err)
	}

	if len(stream.responses) != 2 {
		t.Fatalf("responses = %d, want 2", len(stream.responses))
	}
	for i, r := range stream.responses {
		if r.Done {
			t.Fatalf("unexpected done response for %s", r.EventType)
		}
		checkEmittedEvent(t, pc, m, r)

		// Payload / Meta / Analysis 三通道分离断言。
		payload := unmarshalToMap(t, r.PayloadMsgpack)
		if _, has := payload["_meta"]; has {
			t.Errorf("%s: payload must not carry _meta", r.EventType)
		}
		if _, has := payload["_state_changes"]; has {
			t.Errorf("%s: payload must not carry _state_changes", r.EventType)
		}
		if _, has := payload["flow_id"]; has {
			t.Errorf("%s: flow_id belongs in meta, not payload", r.EventType)
		}
		meta := unmarshalToMap(t, r.MetaMsgpack)
		if meta["flow_id"] != flowID {
			t.Errorf("%s: meta.flow_id = %v, want %s", r.EventType, meta["flow_id"], flowID)
		}
		analysis := unmarshalToMap(t, r.AnalysisMsgpack)
		if _, has := analysis["_state_changes"]; !has {
			t.Errorf("%s: analysis must carry _state_changes", r.EventType)
		}
		if i == 1 {
			// 响应应带上 error 语义（error_code=1）。
			if meta["role"] != "response" || meta["direction"] != "server_to_client" {
				t.Errorf("%s: meta = %+v", r.EventType, meta)
			}
		}
	}
}

func checkEmittedEvent(t *testing.T, pc *sdkcontract.PluginChecker, m *sdk.Manifest, r *pb.DecodeResponseV2) {
	t.Helper()
	v, err := sdkevent.UnmarshalValueMsgpack(r.PayloadMsgpack)
	if err != nil {
		t.Fatalf("unmarshal payload for %s: %v", r.EventType, err)
	}
	draft := &sdkevent.Draft{
		Type:  sdkevent.EventType(r.EventType),
		Value: v,
	}
	rep := pc.CheckEvent(m, draft)
	for _, viol := range rep.Violations {
		t.Errorf("%s: %s: %s", r.EventType, viol.RuleID, viol.Message)
	}
}

func unmarshalToMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	v, err := sdkevent.UnmarshalValueMsgpack(b)
	if err != nil {
		t.Fatalf("unmarshal value: %v", err)
	}
	anyVal := v.ToAny()
	m, ok := anyVal.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want map[string]any", anyVal)
	}
	return m
}
