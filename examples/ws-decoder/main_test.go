package main

import (
	"encoding/binary"
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
		t.Fatal("plugin.yaml must declare semantic_rules (current contract)")
	}
	rep := sdkcontract.NewPluginChecker().Check(m)
	for _, v := range rep.Violations {
		t.Errorf("contract %s: %s", v.RuleID, v.Message)
	}
}

// rawFrame is a helper that builds a raw RFC 6455 frame for parse tests.
// masked=false produces a server frame, masked=true a client frame.
func rawFrame(opcode byte, fin, masked bool, payload []byte) []byte {
	b0 := opcode
	if fin {
		b0 |= 0x80
	}
	var hdr []byte
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch {
	case len(payload) < 126:
		hdr = []byte{b0, maskBit | byte(len(payload))}
	case len(payload) <= 0xFFFF:
		hdr = []byte{b0, maskBit | 126, 0, 0}
		binary.BigEndian.PutUint16(hdr[2:4], uint16(len(payload)))
	default:
		hdr = []byte{b0, maskBit | 127, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(hdr[2:10], uint64(len(payload)))
	}
	if !masked {
		return append(hdr, payload...)
	}
	mask := [4]byte{0x01, 0x02, 0x03, 0x04}
	out := append(append([]byte{}, hdr...), mask[:]...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out
}

func TestParseFrameUnmasked(t *testing.T) {
	buf := rawFrame(wsOpText, true, false, []byte("abc"))
	f, n, ok := parseFrame(buf)
	if !ok || n != len(buf) {
		t.Fatalf("parse: ok=%v consumed=%d want=%d", ok, n, len(buf))
	}
	if f.opcode != wsOpText || !f.fin || f.masked || string(f.payload) != "abc" {
		t.Fatalf("unexpected frame: %+v", f)
	}
}

func TestParseFrameMasked(t *testing.T) {
	buf := rawFrame(wsOpText, true, true, []byte("hello"))
	f, n, ok := parseFrame(buf)
	if !ok || n != len(buf) {
		t.Fatalf("parse: ok=%v consumed=%d want=%d", ok, n, len(buf))
	}
	if f.opcode != wsOpText || !f.masked || string(f.payload) != "hello" {
		t.Fatalf("unexpected frame: %+v", f)
	}
}

func TestParseFrameExtendedLength(t *testing.T) {
	for _, size := range []int{126, 200, 65536} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i)
		}
		buf := rawFrame(wsOpBinary, true, false, payload)
		f, n, ok := parseFrame(buf)
		if !ok || n != len(buf) {
			t.Fatalf("size %d: ok=%v consumed=%d want=%d", size, ok, n, len(buf))
		}
		if f.opcode != wsOpBinary || len(f.payload) != size {
			t.Fatalf("size %d: opcode=%d payload=%d", size, f.opcode, len(f.payload))
		}
	}
}

func TestParseFrameIncomplete(t *testing.T) {
	cases := [][]byte{
		{0x81},                       // 不足 2 字节头
		{0x81, 0x03, 'a'},            // payload 不完整
		{0x81, 0x7E, 0x00},           // 扩展长度头不完整
		{0x81, 0x7F, 0, 0, 0, 0},     // 64 位扩展长度头不完整
		{0x81, 0x83, 0x01, 0x02, 0x03}, // mask key 不完整
	}
	for _, buf := range cases {
		if _, n, ok := parseFrame(buf); ok || n != 0 {
			t.Errorf("%v: want ok=false consumed=0, got ok=%v n=%d", buf, ok, n)
		}
	}
}

func TestParseFrameTwoInOneBuffer(t *testing.T) {
	first := rawFrame(wsOpText, true, true, []byte("one"))
	second := rawFrame(wsOpText, true, true, []byte("two"))
	buf := append(append([]byte{}, first...), second...)
	f, n, ok := parseFrame(buf)
	if !ok || n != len(first) || string(f.payload) != "one" {
		t.Fatalf("first frame: ok=%v consumed=%d payload=%q", ok, n, f.payload)
	}
	f2, n2, ok2 := parseFrame(buf[n:])
	if !ok2 || n2 != len(second) || string(f2.payload) != "two" {
		t.Fatalf("second frame: ok=%v consumed=%d payload=%q", ok2, n2, f2.payload)
	}
}

func TestParseHandshakeRequest(t *testing.T) {
	raw := "GET /ws HTTP/1.1\r\n" +
		"Host: 127.0.0.1:8990\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	hs, n, ok := parseHandshake([]byte(raw))
	if !ok || n != len(raw) {
		t.Fatalf("parse: ok=%v consumed=%d want=%d", ok, n, len(raw))
	}
	if !hs.isRequest || !hs.upgrade || hs.method != "GET" || hs.path != "/ws" ||
		hs.host != "127.0.0.1:8990" || hs.version != "13" {
		t.Fatalf("unexpected handshake: %+v", hs)
	}
}

func TestParseHandshakeResponse(t *testing.T) {
	raw := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"
	hs, n, ok := parseHandshake([]byte(raw))
	if !ok || n != len(raw) {
		t.Fatalf("parse: ok=%v consumed=%d want=%d", ok, n, len(raw))
	}
	if hs.isRequest || hs.status != 101 {
		t.Fatalf("unexpected handshake: %+v", hs)
	}
}

func TestParseHandshakeNonUpgrade(t *testing.T) {
	raw := "GET /index.html HTTP/1.1\r\nHost: h\r\n\r\n"
	hs, _, ok := parseHandshake([]byte(raw))
	if !ok || !hs.isRequest || hs.upgrade {
		t.Fatalf("unexpected handshake: %+v ok=%v", hs, ok)
	}
}

func TestParseHandshakeIncomplete(t *testing.T) {
	if _, n, ok := parseHandshake([]byte("GET /ws HTTP/1.1\r\nHost: h\r\n")); ok || n != 0 {
		t.Fatalf("incomplete handshake: ok=%v consumed=%d", ok, n)
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
		map[string]any{"type": "echo", "seq": int64(1234), "is_error": false},
		map[string]any{"direction": "client_to_server"},
	)
	if len(req.Pairs) != 1 || req.Pairs[0].Side != 0 || req.Pairs[0].Key != "1234" {
		t.Fatalf("echo pair hit = %+v, want side 0 key 1234", req.Pairs)
	}
	if len(req.Semantics) != 0 {
		t.Fatalf("echo semantics = %v, want none", req.Semantics)
	}

	resp := eval(
		map[string]any{"type": "echo_reply", "seq": int64(1234), "error_code": int64(1), "is_error": true},
		map[string]any{"direction": "server_to_client"},
	)
	if len(resp.Pairs) != 1 || resp.Pairs[0].Side != 1 || resp.Pairs[0].Key != "1234" {
		t.Fatalf("echo_reply pair hit = %+v, want side 1 key 1234", resp.Pairs)
	}
	if !rule.MatchPair(req.Pairs[0], resp.Pairs[0]) {
		t.Fatalf("echo/echo_reply pair hits must match: %+v vs %+v", req.Pairs[0], resp.Pairs[0])
	}
	if !hasSemantic(resp.Semantics, rule.SemError) {
		t.Fatalf("echo_reply semantics = %v, want error", resp.Semantics)
	}

	push := eval(
		map[string]any{"type": "push", "seq": int64(0), "is_error": false},
		map[string]any{"direction": "server_to_client"},
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

// TestEmitSchemaConformance drives the full handshake -> frame lifecycle and
// asserts Payload / Meta / Analysis separation plus semantic-rule conformance.
func TestEmitSchemaConformance(t *testing.T) {
	m := loadManifest(t)
	pc := sdkcontract.NewPluginChecker()
	d := newDecoder()
	flowID := "tcp 127.0.0.1:12345=127.0.0.1:8990"

	stream := &captureStream{}
	reqHS, _, _ := parseHandshake([]byte(
		"GET /ws HTTP/1.1\r\nHost: 127.0.0.1:8990\r\nUpgrade: websocket\r\n" +
			"Connection: Upgrade\r\nSec-WebSocket-Key: x\r\nSec-WebSocket-Version: 13\r\n\r\n"))
	if err := d.emitHandshake(stream, "in-1", flowID, reqHS); err != nil {
		t.Fatalf("emit handshake request: %v", err)
	}
	respHS, _, _ := parseHandshake([]byte(
		"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n" +
			"Connection: Upgrade\r\nSec-WebSocket-Accept: y\r\n\r\n"))
	if err := d.emitHandshake(stream, "in-2", flowID, respHS); err != nil {
		t.Fatalf("emit handshake response: %v", err)
	}
	frames := []wsFrame{
		{opcode: wsOpText, fin: true, masked: true, payload: []byte(`{"type":"echo","seq":1234,"text":"hi"}`)},
		{opcode: wsOpText, fin: true, masked: false, payload: []byte(`{"type":"echo_reply","seq":1234,"text":"hi","error_code":1}`)},
		{opcode: wsOpPing, fin: true, masked: false, payload: []byte("ping")},
	}
	for i, f := range frames {
		if err := d.emitFrame(stream, fmt.Sprintf("in-%d", i+3), flowID, f); err != nil {
			t.Fatalf("emit frame %d: %v", i, err)
		}
	}

	// 2 握手 + 2 数据 + 1 控制帧。
	if len(stream.responses) != 5 {
		t.Fatalf("responses = %d, want 5", len(stream.responses))
	}
	wantDir := []string{"client_to_server", "server_to_client", "client_to_server", "server_to_client", "server_to_client"}
	for i, r := range stream.responses {
		if r.Done {
			t.Fatalf("unexpected done response for %s", r.EventType)
		}
		checkEmittedEvent(t, pc, m, r)
		payload := unmarshalToMap(t, r.PayloadMsgpack)
		if _, has := payload["flow_id"]; has {
			t.Errorf("%s: flow_id belongs in meta, not payload", r.EventType)
		}
		if _, has := payload["_state_changes"]; has {
			t.Errorf("%s: payload must not carry _state_changes", r.EventType)
		}
		meta := unmarshalToMap(t, r.MetaMsgpack)
		if meta["flow_id"] != flowID {
			t.Errorf("%s: meta.flow_id = %v, want %s", r.EventType, meta["flow_id"], flowID)
		}
		if meta["direction"] != wantDir[i] {
			t.Errorf("%s: meta.direction = %v, want %s", r.EventType, meta["direction"], wantDir[i])
		}
		analysis := unmarshalToMap(t, r.AnalysisMsgpack)
		if _, has := analysis["_state_changes"]; !has {
			t.Errorf("%s: analysis must carry _state_changes", r.EventType)
		}
	}
}

// TestEmitFragmentReassembly verifies continuation frames are reassembled into
// a single fragmented message.
func TestEmitFragmentReassembly(t *testing.T) {
	d := newDecoder()
	flowID := "tcp 127.0.0.1:12345=127.0.0.1:8990"
	stream := &captureStream{}

	if err := d.emitFrame(stream, "in-1", flowID, wsFrame{
		opcode: wsOpText, fin: false, masked: true,
		payload: []byte(`{"type":"echo","seq":`),
	}); err != nil {
		t.Fatalf("emit fragment start: %v", err)
	}
	if len(stream.responses) != 0 {
		t.Fatalf("fragment start must not emit, got %d responses", len(stream.responses))
	}
	if err := d.emitFrame(stream, "in-2", flowID, wsFrame{
		opcode: wsOpContinuation, fin: true, masked: true,
		payload: []byte(`1234}`),
	}); err != nil {
		t.Fatalf("emit fragment end: %v", err)
	}
	if len(stream.responses) != 1 {
		t.Fatalf("reassembled message must emit once, got %d responses", len(stream.responses))
	}

	r := stream.responses[0]
	if r.EventType != "ws.text" {
		t.Fatalf("event type = %s, want ws.text", r.EventType)
	}
	payload := unmarshalToMap(t, r.PayloadMsgpack)
	if payload["fragmented"] != true {
		t.Fatalf("fragmented = %v, want true", payload["fragmented"])
	}
	if payload["text"] != `{"type":"echo","seq":1234}` {
		t.Fatalf("reassembled text = %v", payload["text"])
	}
	if payload["type"] != "echo" || payload["seq"] != int64(1234) {
		t.Fatalf("envelope = type:%v seq:%v", payload["type"], payload["seq"])
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
