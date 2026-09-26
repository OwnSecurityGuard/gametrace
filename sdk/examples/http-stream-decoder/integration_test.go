// Package main 的集成测试：SDK 官方模板（http-stream-decoder）必须自带一条
// 「真实 gRPC DecodeV2 流 → fake packet → 事件 → 契约校验」的完整链路示例，
// 外部插件作者照抄即可验证自己的插件；同时它在 CI 中兜底 SDK 服务壳
// （sdk.NewDecoder）的行为不回退。
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	sdkcontract "github.com/OwnSecurityGuard/gametrace/sdk/contract"
	sdkevent "github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// buildEthernetFrame 手工拼一个 Ethernet/IPv4/TCP 完整帧（PSH|ACK）。
func buildEthernetFrame(srcIP, dstIP net.IP, srcPort, dstPort uint16, seq uint32, payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x08, 0x00})
	l3 := 20 + 20 + len(payload)
	ip := []byte{0x45, 0x00, byte(l3 >> 8), byte(l3), 0x00, 0x01, 0x40, 0x00, 64, 6}
	ip = append(ip, 0x00, 0x00) // checksum（解析路径不校验）
	ip = append(ip, srcIP.To4()...)
	ip = append(ip, dstIP.To4()...)
	buf.Write(ip)
	var tcp [20]byte
	binary.BigEndian.PutUint16(tcp[0:2], srcPort)
	binary.BigEndian.PutUint16(tcp[2:4], dstPort)
	binary.BigEndian.PutUint32(tcp[4:8], seq)
	binary.BigEndian.PutUint32(tcp[8:12], 1)
	tcp[12] = 5 << 4
	tcp[13] = 0x18
	binary.BigEndian.PutUint16(tcp[14:16], 65535)
	buf.Write(tcp[:])
	buf.Write(payload)
	return buf.Bytes()
}

func buildHTTPRequest(body string) []byte {
	return []byte("POST /echo HTTP/1.1\r\nHost: 127.0.0.1:8984\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func buildHTTPResponse(body string) []byte {
	return []byte("HTTP/1.1 200 OK\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

// serveViaGRPC 在 bufconn 上起真实 gRPC 解码服务并返回 DecodeV2 客户端流。
// 这就是外部插件集成测试的可复制模板。
func serveViaGRPC(t *testing.T) pb.Decoder_DecodeV2Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterDecoderServer(srv, sdk.NewDecoder(newDecoder().decode))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	stream, err := pb.NewDecoderClient(conn).DecodeV2(ctx)
	if err != nil {
		t.Fatalf("open DecodeV2 stream: %v", err)
	}
	return stream
}

func sendFrame(t *testing.T, stream pb.Decoder_DecodeV2Client, inputID string, frame []byte) []*pb.DecodeResponseV2 {
	t.Helper()
	if err := stream.Send(&pb.DecodeRequest{
		SessionId: "s-int", InputId: inputID, PacketId: inputID,
		Payload: frame, LinkType: 1,
	}); err != nil {
		t.Fatalf("send %s: %v", inputID, err)
	}
	var out []*pb.DecodeResponseV2
	for {
		r, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv for %s: %v", inputID, err)
		}
		if r.GetInputId() != inputID {
			t.Fatalf("input_id = %q, want %q", r.GetInputId(), inputID)
		}
		if r.GetError() != "" {
			t.Fatalf("%s: decoder error: %s", inputID, r.GetError())
		}
		out = append(out, r)
		if r.GetDone() {
			return out
		}
	}
}

// TestDecodeV2OverRealGRPC：请求/响应两个完整帧经真实 gRPC 流解出
// http.request / http.response 事件，且逐条通过宿主同款契约校验。
func TestDecodeV2OverRealGRPC(t *testing.T) {
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
	pc := sdkcontract.NewPluginChecker()
	stream := serveViaGRPC(t)

	reqFrame := buildEthernetFrame(net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 1), 12345, 8984, 1000,
		buildHTTPRequest(`{"header":{"cmd":1001},"body":{"seq":1234}}`))
	respFrame := buildEthernetFrame(net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 1), 8984, 12345, 5000,
		buildHTTPResponse(`{"header":{"cmd":1002},"body":{"seq":1234,"error_code":0}}`))

	got := map[string]*pb.DecodeResponseV2{}
	for _, tc := range []struct {
		id, want string
		frame    []byte
	}{
		{"in-1", "http.request", reqFrame},
		{"in-2", "http.response", respFrame},
	} {
		resp := sendFrame(t, stream, tc.id, tc.frame)
		if len(resp) != 2 {
			t.Fatalf("%s: responses = %d, want event+done", tc.id, len(resp))
		}
		if resp[0].GetEventType() != tc.want {
			t.Fatalf("%s: event_type = %q, want %q", tc.id, resp[0].GetEventType(), tc.want)
		}
		got[tc.want] = resp[0]
	}
	_ = stream.CloseSend()

	for etype, r := range got {
		v, err := sdkevent.UnmarshalValueMsgpack(r.GetPayloadMsgpack())
		if err != nil {
			t.Fatalf("%s: unmarshal payload: %v", etype, err)
		}
		rep := pc.CheckEvent(m, &sdkevent.Draft{Type: sdkevent.EventType(etype), Value: v})
		for _, viol := range rep.Violations {
			t.Errorf("%s: contract %s: %s", etype, viol.RuleID, viol.Message)
		}
	}
}
