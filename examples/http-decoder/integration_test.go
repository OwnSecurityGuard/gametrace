// Package main 的集成测试：不经任何 mock，把 fake packet 从真实的 gRPC
// DecodeV2 双向流灌进插件（bufconn 上挂 sdk.NewDecoder），断言事件输出并过
// 宿主同款契约校验（PluginChecker.CheckEvent）。
//
// 与单测的分工：main_test.go 覆盖 parseMessage/emit 的函数级行为；
// 本文件覆盖「传输 → SDK 服务壳（panic 恢复/done 语义）→ 解码 → 事件」的
// 线上同款调用路径——外部贡献者改插件 SDK 后最容易在这里露馅。
// 平台侧「registry 注册 → pipeline 落库」链路另由根模块 TestSimulate 覆盖。
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	sdkcontract "github.com/OwnSecurityGuard/gametrace/sdk/contract"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// buildEthernetFrame 手工拼一个 Ethernet/IPv4/TCP 完整帧（PSH|ACK，无校验和
// 要求——gopacket 解析路径不校验），payload 即 L7 字节。
func buildEthernetFrame(srcIP, dstIP net.IP, srcPort, dstPort uint16, seq uint32, payload []byte) []byte {
	var buf bytes.Buffer
	// Ethernet 头：dst/src MAC + EtherType 0x0800。
	buf.Write([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x08, 0x00})
	// IPv4 头（20 字节，总长 = 20 + 20 + len(payload)）。
	l3 := 20 + 20 + len(payload)
	ip := []byte{0x45, 0x00, byte(l3 >> 8), byte(l3), 0x00, 0x01, 0x40, 0x00, 64, 6 /* proto=TCP */}
	ip = append(ip, 0x00, 0x00) // checksum（解析不校验）
	ip = append(ip, srcIP.To4()...)
	ip = append(ip, dstIP.To4()...)
	buf.Write(ip)
	// TCP 头（20 字节，flags=PSH|ACK）。
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

// startDecoderServer 在 bufconn 上起真实 gRPC 解码服务，返回客户端流。
func startDecoderServer(t *testing.T) pb.Decoder_DecodeV2Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterDecoderServer(srv, sdk.NewDecoder(newDecoder().decode))
	go func() {
		_ = srv.Serve(lis)
	}()
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

// sendFrame 送一个完整帧并收取该 input 的全部响应（直到 done）。
func sendFrame(t *testing.T, stream pb.Decoder_DecodeV2Client, inputID string, frame []byte) []*pb.DecodeResponseV2 {
	t.Helper()
	if err := stream.Send(&pb.DecodeRequest{
		SessionId: "s-int",
		InputId:   inputID,
		PacketId:  inputID,
		Payload:   frame,
		LinkType:  1, // Ethernet
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

// buildHTTPRequest / buildHTTPResponse 拼完整 L7 报文，Content-Length 按
// body 实际长度生成。
func buildHTTPRequest(body string) []byte {
	return []byte("POST /echo HTTP/1.1\r\nHost: 127.0.0.1:8984\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func buildHTTPResponse(body string) []byte {
	return []byte("HTTP/1.1 200 OK\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

// TestDecodeV2OverRealGRPC 走线上同款路径：HTTP 请求/响应两个完整帧经真实
// gRPC 流解码成事件，逐条过宿主契约校验（CheckEvent 零违规）。
func TestDecodeV2OverRealGRPC(t *testing.T) {
	m := loadManifest(t)
	pc := sdkcontract.NewPluginChecker()
	stream := startDecoderServer(t)

	client := buildEthernetFrame(net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 1), 12345, 8984, 1000,
		buildHTTPRequest(`{"header":{"cmd":1001},"body":{"seq":1234}}`))
	server := buildEthernetFrame(net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 1), 8984, 12345, 5000,
		buildHTTPResponse(`{"header":{"cmd":1002},"body":{"seq":1234,"error_code":0}}`))

	evReq := sendFrame(t, stream, "in-1", client)
	if len(evReq) != 2 { // 1 事件 + 1 done
		t.Fatalf("request frames = %d, want 2", len(evReq))
	}
	evResp := sendFrame(t, stream, "in-2", server)
	if len(evResp) != 2 {
		t.Fatalf("response frames = %d, want 2", len(evResp))
	}
	for _, r := range []*pb.DecodeResponseV2{evReq[0], evResp[0]} {
		if r.GetDone() || r.GetEventType() == "" {
			t.Fatalf("unexpected event response: %+v", r)
		}
		checkEmittedEvent(t, pc, m, r)
	}
	// 事件可区分性：请求/响应必须落到不同 event_type（pair 规则的前提）。
	if evReq[0].GetEventType() == evResp[0].GetEventType() {
		t.Fatalf("request and response share event_type %q", evReq[0].GetEventType())
	}

	if err := stream.CloseSend(); err != nil && err != io.EOF {
		t.Fatalf("close send: %v", err)
	}
}

// TestDecodeV2CrossPacketReassembly 把一个 HTTP 消息拆成两个 TCP 段先后送入：
// 第一段只能 done（等待续传），第二段才产出事件——验证真实传输下 reassembler
// 跨调用状态保持。
func TestDecodeV2CrossPacketReassembly(t *testing.T) {
	stream := startDecoderServer(t)

	full := buildHTTPRequest(`{"header":{"cmd":1001},"body":{"seq":1234}}`)
	first, second := full[:30], full[30:]

	part1 := buildEthernetFrame(net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 1), 23456, 8984, 7000, first)
	out1 := sendFrame(t, stream, "in-1", part1)
	if len(out1) != 1 || !out1[0].GetDone() {
		t.Fatalf("partial segment must only return done, got %+v", out1)
	}

	part2 := buildEthernetFrame(net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 1), 23456, 8984, 7030, second)
	out2 := sendFrame(t, stream, "in-2", part2)
	if len(out2) != 2 || out2[0].GetEventType() == "" {
		t.Fatalf("completed message must emit an event, got %+v", out2)
	}
}
