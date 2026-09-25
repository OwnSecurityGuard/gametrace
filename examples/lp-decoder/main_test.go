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

// rawLP builds one lp frame by hand: magic/version/param/reserved + length
// field (width 1/2/4, big or little endian) + payload. It deliberately does
// NOT share code with the simulator so the wire format is pinned twice.
func rawLP(payload []byte, width int, little bool) []byte {
	param := byte(0)
	if little {
		param |= 0x80
	}
	switch width {
	case 2:
		param |= 1 << 3
	case 4:
		param |= 2 << 3
	}
	out := []byte{0x4C, 0x01, param, 0x00}
	switch width {
	case 1:
		out = append(out, byte(len(payload)))
	case 2:
		if little {
			out = binary.LittleEndian.AppendUint16(out, uint16(len(payload)))
		} else {
			out = binary.BigEndian.AppendUint16(out, uint16(len(payload)))
		}
	case 4:
		if little {
			out = binary.LittleEndian.AppendUint32(out, uint32(len(payload)))
		} else {
			out = binary.BigEndian.AppendUint32(out, uint32(len(payload)))
		}
	}
	return append(out, payload...)
}

// envelopePayload renders the JSON envelope the examples/lp simulators send.
func envelopePayload(cmd, seq, errorCode int64) []byte {
	return []byte(fmt.Sprintf(`{"cmd":%d,"seq":%d,"error_code":%d}`, cmd, seq, errorCode))
}

// envelopePayloadWithError renders an error reply: the server always puts a
// human readable error_msg next to error_code.
func envelopePayloadWithError(cmd, seq, errorCode int64, errMsg string) []byte {
	return []byte(fmt.Sprintf(`{"cmd":%d,"seq":%d,"error_code":%d,"error_msg":%q}`,
		cmd, seq, errorCode, errMsg))
}

// TestParseFrameWidthsAndEndian drives the core template point: the length
// field may be 1/2/4 bytes and big or little endian, per frame.
func TestParseFrameWidthsAndEndian(t *testing.T) {
	body := envelopePayload(1002, 7, 0)
	for _, width := range []int{1, 2, 4} {
		for _, little := range []bool{false, true} {
			buf := rawLP(body, width, little)
			f, n, ok := parseFrame(buf)
			if !ok || n != len(buf) {
				t.Fatalf("width=%d little=%v: ok=%v consumed=%d want=%d", width, little, ok, n, len(buf))
			}
			if f.header.lenWidth != width || f.header.little != little {
				t.Fatalf("width=%d little=%v: header=%+v", width, little, f.header)
			}
			if string(f.payload) != string(body) {
				t.Fatalf("width=%d little=%v: payload=%q", width, little, f.payload)
			}
		}
	}
}

// TestParseFrameIncomplete exercises cross-segment boundaries: the header, the
// length field and the payload may each be split across TCP segments.
func TestParseFrameIncomplete(t *testing.T) {
	buf := rawLP(envelopePayload(1001, 5, 0), 4, true)
	for i := 1; i < len(buf); i++ {
		if _, n, ok := parseFrame(buf[:i]); ok || n != 0 {
			t.Fatalf("prefix %d bytes: want ok=false consumed=0, got ok=%v n=%d", i, ok, n)
		}
	}
	// 解析不完整必须在整帧可见前保持不可用：拼上剩余字节后即可解析。
	if f, n, ok := parseFrame(buf); !ok || n != len(buf) || string(f.payload) != string(envelopePayload(1001, 5, 0)) {
		t.Fatalf("full frame: ok=%v n=%d payload=%q", ok, n, f.payload)
	}
}

// TestParseFrameRejectsForeign checks the header sanity guards: a foreign
// protocol (bad magic), a future version, and a corrupt reserved byte are all
// treated as unparseable rather than mis-decoded.
func TestParseFrameRejectsForeign(t *testing.T) {
	valid := rawLP(envelopePayload(1001, 5, 0), 1, false)

	cases := []struct {
		name string
		buf  []byte
	}{
		{"bad magic", append([]byte{0x01}, valid[1:]...)},
		{"future version", append([]byte{valid[0], 0x02}, valid[2:]...)},
		{"reserved bit set", append([]byte(valid[:3]), 0x01)},
		{"invalid width code", append(append([]byte{}, valid[:2]...), 3<<3, valid[3])},
		{"implausible length", []byte{0x4C, 0x01, 2 << 3, 0x00, 0xFF, 0xFF, 0xFF, 0xFF}}, // 4B 长度=4G
	}
	for _, c := range cases {
		if _, n, ok := parseFrame(c.buf); ok || n != 0 {
			t.Errorf("%s: want ok=false consumed=0, got ok=%v n=%d", c.name, ok, n)
		}
	}
}

// TestParseFrameTwoInOneBuffer verifies the reasm loop can consume multiple
// complete frames from a single segment.
func TestParseFrameTwoInOneBuffer(t *testing.T) {
	first := rawLP(envelopePayload(1001, 1, 0), 2, false)
	second := rawLP(envelopePayload(1002, 1, 0), 1, true)
	buf := append(append([]byte{}, first...), second...)

	f, n, ok := parseFrame(buf)
	if !ok || n != len(first) || string(f.payload) != string(envelopePayload(1001, 1, 0)) {
		t.Fatalf("first: ok=%v consumed=%d payload=%q", ok, n, f.payload)
	}
	f2, n2, ok2 := parseFrame(buf[n:])
	if !ok2 || n2 != len(second) || string(f2.payload) != string(envelopePayload(1002, 1, 0)) {
		t.Fatalf("second: ok=%v consumed=%d payload=%q", ok2, n2, f2.payload)
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
		map[string]any{"cmd": int64(1001), "seq": int64(1234), "is_error": false},
		map[string]any{"direction": "client_to_server"},
	)
	if len(req.Pairs) != 1 || req.Pairs[0].Side != 0 || req.Pairs[0].Key != "1234" {
		t.Fatalf("request pair hit = %+v, want side 0 key 1234", req.Pairs)
	}
	if len(req.Semantics) != 0 {
		t.Fatalf("request semantics = %v, want none", req.Semantics)
	}

	resp := eval(
		map[string]any{"cmd": int64(1002), "seq": int64(1234), "error_code": int64(1), "is_error": true},
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
		map[string]any{"cmd": int64(2001), "seq": int64(0), "is_error": false},
		map[string]any{"direction": "server_to_client"},
	)
	if len(push.Pairs) != 0 {
		t.Fatalf("push message must not participate in pairing, got %+v", push.Pairs)
	}
	if !hasSemantic(push.Semantics, rule.SemNotification) {
		t.Fatalf("push semantics = %v, want notification", push.Semantics)
	}

	// 推送号段的其余消息（道具 / 资源数量变化）同样命中 push rule。
	itemPush := eval(
		map[string]any{"cmd": int64(2002), "seq": int64(0), "is_error": false},
		map[string]any{"direction": "server_to_client"},
	)
	if !hasSemantic(itemPush.Semantics, rule.SemNotification) || len(itemPush.Pairs) != 0 {
		t.Fatalf("item push semantics = %v pairs = %+v, want notification / no pair",
			itemPush.Semantics, itemPush.Pairs)
	}

	// 信封级错误回包：payload 解不出 seq 时也为 0，但它不是推送，只是 error。
	envelopeErr := eval(
		map[string]any{"cmd": int64(9001), "seq": int64(0), "error_code": int64(1), "is_error": true},
		map[string]any{"direction": "server_to_client"},
	)
	if !hasSemantic(envelopeErr.Semantics, rule.SemError) {
		t.Fatalf("bad envelope semantics = %v, want error", envelopeErr.Semantics)
	}
	if hasSemantic(envelopeErr.Semantics, rule.SemNotification) || len(envelopeErr.Pairs) != 0 {
		t.Fatalf("bad envelope must not be a notification nor paired: %v %+v",
			envelopeErr.Semantics, envelopeErr.Pairs)
	}

	// 业务错误响应：号段内的响应号 + 回显 seq，既配对又标注 error。
	bizErr := eval(
		map[string]any{"cmd": int64(1006), "seq": int64(4321), "error_code": int64(7), "is_error": true},
		map[string]any{"direction": "server_to_client"},
	)
	if len(bizErr.Pairs) != 1 || bizErr.Pairs[0].Side != 1 || bizErr.Pairs[0].Key != "4321" {
		t.Fatalf("business error reply pairs = %+v, want side 1 key 4321", bizErr.Pairs)
	}
	if !hasSemantic(bizErr.Semantics, rule.SemError) {
		t.Fatalf("business error reply semantics = %v, want error", bizErr.Semantics)
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

// TestEmitChannels drives the full parse -> emit path across request /
// response / push (each with a different length-field width/endianness) and
// asserts Payload / Meta / Analysis separation plus semantic conformance.
func TestEmitChannels(t *testing.T) {
	m := loadManifest(t)
	pc := sdkcontract.NewPluginChecker()
	d := newDecoder()
	flowID := "tcp 127.0.0.1:40000=127.0.0.1:8998"
	stream := &captureStream{}

	// 一条连接上的完整场景：请求 → 错误响应 → 请求 → 响应 → 推送。
	// 三条消息共用一个 flowID：请求计数 +1、响应（含推送）计数 +1。
	frames := []struct {
		cmd, seq, errCode int64
		errMsg            string
		width             int
		little            bool
		wantType          string
		wantDir           string
		wantName          string
		wantRole          string
		wantPush          bool
		countKey          string
		wantCount         int64
	}{
		{1001, 1234, 0, "", 2, false, "lp.request", "client_to_server", "LoginRequest", "request", false, "requests", 1},
		{1002, 1234, 4, "该连接已登录", 1, true, "lp.response", "server_to_client", "LoginResponse", "response", false, "responses", 1},
		{1005, 1235, 0, "", 4, true, "lp.request", "client_to_server", "UseItemRequest", "request", false, "requests", 2},
		{1006, 1235, 0, "", 1, false, "lp.response", "server_to_client", "UseItemResponse", "response", false, "responses", 2},
		{2002, 0, 0, "", 2, true, "lp.response", "server_to_client", "ItemCountNotify", "push", true, "responses", 3},
	}
	for i, fc := range frames {
		body := envelopePayload(fc.cmd, fc.seq, fc.errCode)
		if fc.errMsg != "" {
			body = envelopePayloadWithError(fc.cmd, fc.seq, fc.errCode, fc.errMsg)
		}
		f, _, ok := parseFrame(rawLP(body, fc.width, fc.little))
		if !ok {
			t.Fatalf("parse frame %d: not ok", i)
		}
		if err := d.emit(stream, fmt.Sprintf("in-%d", i), flowID, f); err != nil {
			t.Fatalf("emit frame %d: %v", i, err)
		}
	}

	if len(stream.responses) != len(frames) {
		t.Fatalf("responses = %d, want %d", len(stream.responses), len(frames))
	}
	for i, r := range stream.responses {
		fc := frames[i]
		if r.Done {
			t.Fatalf("unexpected done response for %s", r.EventType)
		}
		if r.EventType != fc.wantType {
			t.Errorf("event %d type = %s, want %s", i, r.EventType, fc.wantType)
		}
		checkEmittedEvent(t, pc, m, r)

		payload := unmarshalToMap(t, r.PayloadMsgpack)
		if _, has := payload["flow_id"]; has {
			t.Errorf("%s: flow_id belongs in meta, not payload", r.EventType)
		}
		if _, has := payload["frame"]; has {
			t.Errorf("%s: frame header facts belong in meta, not payload", r.EventType)
		}
		if _, has := payload["_state_changes"]; has {
			t.Errorf("%s: payload must not carry _state_changes", r.EventType)
		}
		if payload["seq"] != fc.seq {
			t.Errorf("%s: payload.seq = %v, want %d", r.EventType, payload["seq"], fc.seq)
		}
		if payload["error_msg"] != fc.errMsg {
			t.Errorf("%s: payload.error_msg = %v, want %q", r.EventType, payload["error_msg"], fc.errMsg)
		}
		if payload[fc.countKey] != fc.wantCount {
			t.Errorf("%s: payload.%s = %v, want %d", r.EventType, fc.countKey, payload[fc.countKey], fc.wantCount)
		}

		meta := unmarshalToMap(t, r.MetaMsgpack)
		if meta["flow_id"] != flowID {
			t.Errorf("%s: meta.flow_id = %v, want %s", r.EventType, meta["flow_id"], flowID)
		}
		if meta["direction"] != fc.wantDir {
			t.Errorf("%s: meta.direction = %v, want %s", r.EventType, meta["direction"], fc.wantDir)
		}
		if meta["msg_name"] != fc.wantName {
			t.Errorf("%s: meta.msg_name = %v, want %s", r.EventType, meta["msg_name"], fc.wantName)
		}
		if meta["role"] != fc.wantRole {
			t.Errorf("%s: meta.role = %v, want %s", r.EventType, meta["role"], fc.wantRole)
		}
		if meta["is_push"] != fc.wantPush {
			t.Errorf("%s: meta.is_push = %v, want %v", r.EventType, meta["is_push"], fc.wantPush)
		}
		wantEndian := "big"
		if fc.little {
			wantEndian = "little"
		}
		frameMeta, ok := meta["frame"].(map[string]any)
		if !ok {
			t.Fatalf("%s: meta.frame missing or wrong type", r.EventType)
		}
		if frameMeta["len_width"] != int64(fc.width) || frameMeta["endian"] != wantEndian {
			t.Errorf("%s: meta.frame = %v, want len_width=%d endian=%s",
				r.EventType, frameMeta, fc.width, wantEndian)
		}

		analysis := unmarshalToMap(t, r.AnalysisMsgpack)
		if _, has := analysis["_state_changes"]; !has {
			t.Errorf("%s: analysis must carry _state_changes", r.EventType)
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
