package main

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"google.golang.org/grpc/metadata"

	sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/contract"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/event"
	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
)

// ---------------------------------------------------------------------------
// fake stream：grpc BidiStreamingServer 的测试替身。
// ---------------------------------------------------------------------------

type fakeStream struct {
	in   *pb.DecodeRequest
	sent []*pb.DecodeResponseV2
}

func (f *fakeStream) Send(r *pb.DecodeResponseV2) error { f.sent = append(f.sent, r); return nil }
func (f *fakeStream) Recv() (*pb.DecodeRequest, error)  { return f.in, nil }
func (f *fakeStream) SetHeader(metadata.MD) error       { return nil }
func (f *fakeStream) SendHeader(metadata.MD) error      { return nil }
func (f *fakeStream) SetTrailer(metadata.MD)            {}
func (f *fakeStream) Context() context.Context          { return context.Background() }
func (f *fakeStream) SendMsg(any) error                 { return nil }
func (f *fakeStream) RecvMsg(any) error                 { return nil }

// ---------------------------------------------------------------------------
// 构造输入
// ---------------------------------------------------------------------------

// wmlFrame 把一段 WML 文本封装成线上帧：[4B 大端长度 N][N 字节 gzip]。
func wmlFrame(t *testing.T, text string) []byte {
	t.Helper()
	b := gzipText(t, text)
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	return out
}

// tcpFrame 构造一个以太网 + IPv4 + TCP 帧，使 framing.ExtractL7 能读出端口，
// 从而验证方向判定。link_type 用 1（Ethernet）。
func tcpFrame(t *testing.T, srcPort, dstPort uint16, seq uint32, payload []byte) []byte {
	t.Helper()
	return tcpFrameFlags(t, srcPort, dstPort, seq, payload, false, false, false)
}

// tcpFrameFlags 是 tcpFrame 的完整版，可指定 SYN/RST/FIN 控制位，
// 用于构造连接建立（SYN）、异常终止（RST）与正常关闭（FIN）段。
func tcpFrameFlags(t *testing.T, srcPort, dstPort uint16, seq uint32, payload []byte, syn, rst, fin bool) []byte {
	t.Helper()
	eth := layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
		DstMAC:       net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := layers.IPv4{
		Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolTCP,
		SrcIP: net.IP{127, 0, 0, 1}, DstIP: net.IP{127, 0, 0, 1},
	}
	tcp := layers.TCP{
		SrcPort: layers.TCPPort(srcPort), DstPort: layers.TCPPort(dstPort),
		Seq: seq, PSH: true, ACK: true,
		SYN: syn, RST: rst, FIN: fin,
	}
	_ = tcp.SetNetworkLayerForChecksum(&ip)

	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		&eth, &ip, &tcp, gopacket.Payload(payload)); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.Bytes()
}

// newTestDecoder 返回独立 decoder（reasm 已重置，流状态不串味）。
func newTestDecoder() *decoder {
	d := newDecoder()
	d.reasm.Reset()
	return d
}

// run 跑一次 decodePacket，返回事件响应（不含 done 终止帧）。
func run(t *testing.T, d *decoder, req *pb.DecodeRequest) []*pb.DecodeResponseV2 {
	t.Helper()
	st := &fakeStream{in: req}
	if err := d.decodePacket(req, st); err != nil {
		t.Fatalf("decodePacket: %v", err)
	}
	var out []*pb.DecodeResponseV2
	var dones int
	for _, r := range st.sent {
		if r.GetDone() {
			dones++
			continue
		}
		out = append(out, r)
	}
	if dones != 1 {
		t.Fatalf("expected exactly one done=true terminator, got %d", dones)
	}
	return out
}

// payloadOf 把 msgpack 载荷解回 map。
func payloadOf(t *testing.T, r *pb.DecodeResponseV2) map[string]event.Value {
	t.Helper()
	v, err := event.UnmarshalValueMsgpack(r.GetPayloadMsgpack())
	if err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	m, ok := v.AsObject()
	if !ok {
		t.Fatalf("payload root is %s, want object", v.Kind)
	}
	return m
}

// ---------------------------------------------------------------------------
// 用例
// ---------------------------------------------------------------------------

// TestDecodeLogin 验证上行 login 消息：引号属性扁平到顶层、msg_type 判别、
// 方向为 client_to_server。
func TestDecodeLogin(t *testing.T) {
	d := newTestDecoder()
	body := wmlFrame(t, `[login]
username="player1"
password="s3cret"
[/login]`)
	got := run(t, d, &pb.DecodeRequest{
		InputId: "in-1", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 54321, serverPort, 1, body),
	})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].GetEventType() != "wesnoth.login" || got[0].GetSchemaId() != "wesnoth.message.v1" {
		t.Fatalf("unexpected type/schema: %s / %s", got[0].GetEventType(), got[0].GetSchemaId())
	}
	if got[0].GetInputId() != "in-1" {
		t.Fatalf("input_id not echoed: %q", got[0].GetInputId())
	}
	if got[0].GetCorrelationKey() == "" {
		t.Fatalf("correlation_key must be set")
	}
	p := payloadOf(t, got[0])
	if mt, _ := p["msg_type"].AsString(); mt != "login" {
		t.Errorf("msg_type = %q, want login", mt)
	}
	if u, _ := p["username"].AsString(); u != "player1" {
		t.Errorf("username = %q, want player1", u)
	}
	if pw, _ := p["password"].AsString(); pw != "s3cret" {
		t.Errorf("password = %q, want s3cret", pw)
	}
	if d, _ := metaStr(t, got[0], "direction"); d != dirC2S {
		t.Errorf("direction = %q, want %s", d, dirC2S)
	}
}

// TestPayloadPurity 验证硬约束：Payload 只含协议真实字段。
// WML 原文（辅助信息）必须进 Meta，不得进入 Payload；
// unknown 兜底事件 payload 仅保留 msg_type 判别字段。
func TestPayloadPurity(t *testing.T) {
	t.Run("normal_event_raw_in_meta", func(t *testing.T) {
		d := newTestDecoder()
		got := run(t, d, &pb.DecodeRequest{
			InputId: "in-1", LinkType: int32(event.LinkTypeProxyPayload),
			Payload: wmlFrame(t, `[login]
username="player1"
[/login]`),
		})
		if len(got) != 1 {
			t.Fatalf("want 1 event, got %d", len(got))
		}
		p := payloadOf(t, got[0])
		for _, banned := range []string{"_raw", "raw"} {
			if _, exists := p[banned]; exists {
				t.Errorf("payload must not contain %q (protocol-external field)", banned)
			}
		}
		raw, ok := metaStr(t, got[0], "raw")
		if !ok || !strings.Contains(raw, "[login]") {
			t.Errorf("meta.raw must carry WML text, ok=%v raw=%q", ok, raw)
		}
	})

	t.Run("unknown_event_raw_in_meta", func(t *testing.T) {
		d := newTestDecoder()
		got := run(t, d, &pb.DecodeRequest{
			InputId: "in-2", LinkType: int32(event.LinkTypeProxyPayload),
			Payload: wmlFrame(t, "not [valid wml"),
		})
		if len(got) != 1 || got[0].GetEventType() != "wesnoth.unknown" {
			t.Fatalf("want unknown event, got %+v", got)
		}
		p := payloadOf(t, got[0])
		if len(p) != 1 {
			t.Errorf("unknown payload must only contain msg_type, got %v", p)
		}
		if mt, _ := p["msg_type"].AsString(); mt != "unknown" {
			t.Errorf("msg_type = %q, want unknown", mt)
		}
		if reason, ok := metaStr(t, got[0], "reason"); !ok || reason == "" {
			t.Errorf("meta.reason must be set")
		}
		if raw, ok := metaStr(t, got[0], "raw"); !ok || raw == "" {
			t.Errorf("meta.raw must carry original bytes")
		}
	})
}

// TestDecodeHandshakeSkipped 验证每方向流开头的 4 字节握手被消费，不影响
// 后续帧解析（客户端发 0x00000000，服务端回 0xFFFFFFFF 两种分支）。
func TestDecodeHandshakeSkipped(t *testing.T) {
	login := wmlFrame(t, `[login]
username="player1"
[/login]`)

	t.Run("client_handshake_0", func(t *testing.T) {
		d := newTestDecoder()
		payload := make([]byte, 4)
		payload = append(payload, login...) // 握手 0x00000000 + [login] 帧
		got := run(t, d, &pb.DecodeRequest{
			InputId: "h-1", LinkType: int32(event.LinkTypeEthernet),
			Payload: tcpFrame(t, 54321, serverPort, 1, payload),
		})
		if len(got) != 1 || got[0].GetEventType() != "wesnoth.login" {
			t.Fatalf("handshake+frame must yield login event, got %+v", got)
		}
	})

	t.Run("server_handshake_rejected", func(t *testing.T) {
		d := newTestDecoder()
		payload := make([]byte, 4)
		binary.BigEndian.PutUint32(payload, 0xFFFFFFFF)
		payload = append(payload, login...)
		got := run(t, d, &pb.DecodeRequest{
			InputId: "h-2", LinkType: int32(event.LinkTypeEthernet),
			Payload: tcpFrame(t, serverPort, 54321, 1, payload),
		})
		if len(got) != 1 || got[0].GetEventType() != "wesnoth.login" {
			t.Fatalf("server handshake+frame must yield login event, got %+v", got)
		}
	})
}

// TestDecodeMultipleConnections 验证多连接场景：两条独立 TCP 连接各自
// 握手 + login，握手消费、方向判定、correlation_key 均按连接隔离互不干扰。
func TestDecodeMultipleConnections(t *testing.T) {
	d := newTestDecoder()
	login := wmlFrame(t, `[login]
username="player1"
[/login]`)

	// 连接 A：client:51001 → server:15000，握手(0) + login 帧
	payloadA := make([]byte, 4)
	payloadA = append(payloadA, login...)
	gotA := run(t, d, &pb.DecodeRequest{
		InputId: "a1", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 51001, serverPort, 100, payloadA),
	})
	if len(gotA) != 1 || gotA[0].GetEventType() != "wesnoth.login" {
		t.Fatalf("conn A must decode login, got %+v", gotA)
	}

	// 连接 B：client:52001 → server:15000（不同四元组），握手(0) + login 帧。
	// 连接 A 的 seen 标记绝不能泄漏到连接 B。
	payloadB := make([]byte, 4)
	payloadB = append(payloadB, login...)
	gotB := run(t, d, &pb.DecodeRequest{
		InputId: "b1", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 52001, serverPort, 200, payloadB),
	})
	if len(gotB) != 1 || gotB[0].GetEventType() != "wesnoth.login" {
		t.Fatalf("conn B must decode login, got %+v", gotB)
	}
	if gotA[0].GetCorrelationKey() == gotB[0].GetCorrelationKey() {
		t.Fatalf("correlation keys must differ per connection: %q", gotA[0].GetCorrelationKey())
	}
}

// TestDecodeFlowReuseHandshakeReset 验证 5-tuple 复用场景：客户端重连复用
// 同一四元组时，SYN 必须重置握手簿记，否则新连接的头 4 字节握手会被旧
// 连接的 seen 残留误当帧长度（n==0 → 失步，整个新连接解不出来）。
func TestDecodeFlowReuseHandshakeReset(t *testing.T) {
	d := newTestDecoder()
	login := wmlFrame(t, `[login]
username="player1"
[/login]`)

	// 连接 A：SYN 建连 → 握手(0) + login 帧
	run(t, d, &pb.DecodeRequest{
		InputId: "a1", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrameFlags(t, 51001, serverPort, 100, nil, true, false, false),
	})
	payloadA := make([]byte, 4)
	payloadA = append(payloadA, login...)
	gotA := run(t, d, &pb.DecodeRequest{
		InputId: "a2", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 51001, serverPort, 101, payloadA),
	})
	if len(gotA) != 1 || gotA[0].GetEventType() != "wesnoth.login" {
		t.Fatalf("conn A must decode login, got %+v", gotA)
	}

	// 连接 B：同一四元组新连接（重连复用端口）——SYN 重置握手簿记，
	// 第二个握手必须再次被消费，login 才能解出。
	run(t, d, &pb.DecodeRequest{
		InputId: "b1", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrameFlags(t, 51001, serverPort, 300, nil, true, false, false),
	})
	payloadB := make([]byte, 4)
	payloadB = append(payloadB, login...)
	gotB := run(t, d, &pb.DecodeRequest{
		InputId: "b2", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 51001, serverPort, 301, payloadB),
	})
	if len(gotB) != 1 || gotB[0].GetEventType() != "wesnoth.login" {
		t.Fatalf("conn B (5-tuple reuse) must decode login after SYN reset, got %+v", gotB)
	}
}

// TestDecodeGamelistChildren 验证下行 gamelist：嵌套 [game]/[user] 子节点
// 按 tag 名分组进 _children。
func TestDecodeGamelistChildren(t *testing.T) {
	d := newTestDecoder()
	body := wmlFrame(t, `[gamelist]
[game]
name="Test Game"
port="15000"
[era]
id="default"
[/era]
[/game]
[user]
name="alice"
location="0"
[/user]
[/gamelist]`)
	got := run(t, d, &pb.DecodeRequest{
		InputId: "in-2", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, serverPort, 54321, 1, body),
	})
	if len(got) != 1 || got[0].GetEventType() != "wesnoth.gamelist" {
		t.Fatalf("unexpected events: %+v", got)
	}
	if d, _ := metaStr(t, got[0], "direction"); d != dirS2C {
		t.Errorf("direction = %q, want %s", d, dirS2C)
	}
	p := payloadOf(t, got[0])
	kids, ok := p["_children"].AsObject()
	if !ok {
		t.Fatalf("_children missing: %v", p["_children"])
	}
	games, ok := kids["game"].AsArray()
	if !ok || len(games) != 1 {
		t.Fatalf("_children.game = %v, want 1 element", kids["game"])
	}
	gameObj, _ := games[0].AsObject()
	if name, _ := gameObj["name"].AsString(); name != "Test Game" {
		t.Errorf("game.name = %q, want Test Game", name)
	}
	users, _ := kids["user"].AsArray()
	if len(users) != 1 {
		t.Errorf("_children.user = %v, want 1 element", kids["user"])
	}
}

// TestDecodeBzip2Version 验证 bzip2 压缩的下行包（服务端 output_compressed 可选
// bzip2）能解出：固件为 bzip2('[version]...')。
func TestDecodeBzip2Version(t *testing.T) {
	d := newTestDecoder()
	const fixture = "425a68393141592653590d77057a0000095b8000101001e042008a8a659f002000545068d1a0c80d08d29b4d4da9ea3264c6a197555626676a1caf647742d652f88185cec2ec71e503ca10760e262cbe97e2ee48a70a1201aee0af40"
	raw := mustHex(t, fixture)
	body := make([]byte, 0, 4+len(raw))
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
	body = append(body, lenBuf[:]...)
	body = append(body, raw...)

	got := run(t, d, &pb.DecodeRequest{
		InputId: "in-3", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, serverPort, 54321, 1, body),
	})
	if len(got) != 1 || got[0].GetEventType() != "wesnoth.version" {
		t.Fatalf("unexpected events: %+v", got)
	}
	p := payloadOf(t, got[0])
	if v, _ := p["version"].AsString(); v != "1.18.0" {
		t.Errorf("version = %q, want 1.18.0", v)
	}
}

// TestDecodeSplitAcrossSegments 验证跨 TCP 段的一帧能被重组后解出（TCP 重组必需）。
func TestDecodeSplitAcrossSegments(t *testing.T) {
	d := newTestDecoder()
	full := wmlFrame(t, `[login]
username="player1"
[/login]`)

	st := &fakeStream{}
	half := len(full) / 2

	if err := d.decodePacket(&pb.DecodeRequest{
		InputId: "a", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 54321, serverPort, 1, full[:half]),
	}, st); err != nil {
		t.Fatalf("seg1: %v", err)
	}
	if events(st.sent) != 0 {
		t.Fatalf("segment 1 must not yield an event (frame incomplete), got %d", events(st.sent))
	}

	if err := d.decodePacket(&pb.DecodeRequest{
		InputId: "b", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 54321, serverPort, 1+uint32(half), full[half:]),
	}, st); err != nil {
		t.Fatalf("seg2: %v", err)
	}
	if events(st.sent) != 1 {
		t.Fatalf("segment 2 must complete the frame and yield 1 event, got %d", events(st.sent))
	}
}

// TestDecodeMultipleFramesPerPacket 验证一个 TCP 段里携带多帧。
func TestDecodeMultipleFramesPerPacket(t *testing.T) {
	d := newTestDecoder()
	var payload []byte
	payload = append(payload, wmlFrame(t, "[version]\nversion=\"1.18.0\"\n[/version]")...)
	payload = append(payload, wmlFrame(t, `[login]
username="player1"
[/login]`)...)

	got := run(t, d, &pb.DecodeRequest{
		InputId: "in-4", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 54321, serverPort, 1, payload),
	})
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d", len(got))
	}
	if got[0].GetEventType() != "wesnoth.version" || got[1].GetEventType() != "wesnoth.login" {
		t.Errorf("unexpected order/types: %s, %s", got[0].GetEventType(), got[1].GetEventType())
	}
}

// TestDecodeProxyPayload 验证代理类输入（已是纯 L7、无端口）仍能解码，
// 方向字段留空（由宿主按流补齐）。
func TestDecodeProxyPayload(t *testing.T) {
	d := newTestDecoder()
	got := run(t, d, &pb.DecodeRequest{
		InputId: "in-5", LinkType: int32(event.LinkTypeProxyPayload),
		Payload: wmlFrame(t, `[chat]
sender="alice"
message="hi"
[/chat]`),
	})
	if len(got) != 1 || got[0].GetEventType() != "wesnoth.chat" {
		t.Fatalf("want 1 chat event, got %+v", got)
	}
	if d, _ := metaStr(t, got[0], "direction"); d != "" {
		t.Errorf("direction = %q, want empty (no ports)", d)
	}
}

// TestDecodeMalformedIsSafe 验证畸形输入不 panic、不返回 error，只产出兜底事件。
func TestDecodeMalformedIsSafe(t *testing.T) {
	d := newTestDecoder()
	// 载荷不是合法 gzip（客户端/服务端从未出现过的垃圾字节）。
	got := run(t, d, &pb.DecodeRequest{
		InputId: "in-6", LinkType: int32(event.LinkTypeEthernet),
		Payload: tcpFrame(t, 54321, serverPort, 1, wmlFrameRaw(t, "not gzip at all")),
	})
	if len(got) != 1 || got[0].GetEventType() != "wesnoth.unknown" {
		t.Fatalf("want 1 unknown event, got %+v", got)
	}
	if err := got[0].GetError(); err != "" {
		t.Errorf("unexpected error field: %s", err)
	}

	// 空载荷 / 纯 ACK 不产出事件，但必须回 done。
	d = newTestDecoder()
	st := &fakeStream{in: &pb.DecodeRequest{InputId: "in-7", LinkType: int32(event.LinkTypeProxyPayload)}}
	if err := d.decodePacket(st.in, st); err != nil {
		t.Fatalf("empty payload: %v", err)
	}
	if len(st.sent) != 1 || !st.sent[0].GetDone() {
		t.Fatalf("empty payload must yield exactly one done frame, got %+v", st.sent)
	}
}

// wmlFrameRaw 构造 [4B 长度][原始字节] 帧（用于畸形 payload，不压缩）。
func wmlFrameRaw(t *testing.T, raw string) []byte {
	t.Helper()
	b := []byte(raw)
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	return out
}

// TestDecodedEventsConformToManifest 把真实报文解出的事件拿到契约校验器上跑一遍：
// schema 必须在 manifest 中声明，且 payload 必须符合声明（类型/必填/未知字段）。
func TestDecodedEventsConformToManifest(t *testing.T) {
	data, err := os.ReadFile("plugin.yaml")
	if err != nil {
		t.Fatalf("read plugin.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(data)
	if err != nil {
		t.Fatalf("parse plugin.yaml: %v", err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatalf("manifest violates contract: %v", err)
	}
	checker := contract.NewPluginChecker()
	if r := checker.Check(m); r.HasErrors() {
		t.Fatalf("manifest schema layer has errors: %v", r)
	}

	cases := []struct {
		name string
		wml  string
	}{
		{"login", "[login]\nusername=\"player1\"\npassword=\"s3cret\"\n[/login]"},
		{"version", "[version]\nversion=\"1.18.0\"\nclient_source=\"Wesnoth\"\n[/version]"},
		{"gamelist", "[gamelist]\n[game]\nname=\"Test Game\"\nport=\"15000\"\n[/game]\n[user]\nname=\"alice\"\nlocation=\"0\"\n[/user]\n[/gamelist]"},
		{"chat", "[chat]\nsender=\"alice\"\nmessage=\"hi there\"\n[/chat]"},
		{"unknown", "garbage bytes that are not wml at all"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDecoder()
			req := &pb.DecodeRequest{
				InputId: "c-" + tc.name, LinkType: int32(event.LinkTypeProxyPayload),
				Payload: wmlFrame(t, tc.wml),
			}
			got := run(t, d, req)
			if len(got) != 1 {
				t.Fatalf("want 1 event, got %d", len(got))
			}
			v, err := event.UnmarshalValueMsgpack(got[0].GetPayloadMsgpack())
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			draft := &event.Draft{
				Type:      event.EventType(got[0].GetEventType()),
				SchemaRef: got[0].GetSchemaId(),
				Value:     v,
			}
			if r := checker.CheckEvent(m, draft); r.HasErrors() {
				t.Fatalf("event does not conform to declared schema: %v", r)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// metaStr 从响应 v0.8.0 独立 MetaMsgpack 中读取元信息字段。
func metaStr(t *testing.T, r *pb.DecodeResponseV2, key string) (string, bool) {
	t.Helper()
	v, err := event.UnmarshalValueMsgpack(r.GetMetaMsgpack())
	if err != nil {
		return "", false
	}
	meta, ok := v.AsObject()
	if !ok {
		return "", false
	}
	val, ok := meta[key]
	if !ok {
		return "", false
	}
	return val.AsString()
}

func events(rs []*pb.DecodeResponseV2) int {
	n := 0
	for _, r := range rs {
		if !r.GetDone() {
			n++
		}
	}
	return n
}
