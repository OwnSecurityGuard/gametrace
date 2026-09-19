package mobile

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gametrace/pkg/capture"
	"gametrace/pkg/capture/mobile/proto"
	"gametrace/pkg/event"
)

// startSource 启动一个 mobile source，返回 Source 与 gRPC 监听地址。
func startSource(t *testing.T) (capture.Source, string) {
	t.Helper()
	cfg := MobileConfig{ListenAddr: "127.0.0.1:0"}
	src, err := capture.Open(context.Background(), "mobile", cfg)
	if err != nil {
		t.Fatalf("open mobile source: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	addr := src.(*mobileSource).Addr().String()
	if addr == "" {
		t.Fatalf("source addr is empty")
	}
	return src, addr
}

// openPush 建立到 source 的 Push 客户端流。
func openPush(t *testing.T, addr string) proto.MobileCapture_PushClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	stream, err := proto.NewMobileCaptureClient(conn).Push(context.Background())
	if err != nil {
		t.Fatalf("open push stream: %v", err)
	}
	return stream
}

func dataEvent(connID, direction string, payload []byte) *proto.AgentEvent {
	return &proto.AgentEvent{
		ConnId: connID,
		Event:  &proto.AgentEvent_Data{Data: &proto.ConnData{Direction: direction, Payload: payload}},
	}
}

// readN 从包通道读取 n 个包，超时则测试失败。
func readN(t *testing.T, ch <-chan event.Packet, n int) []event.Packet {
	t.Helper()
	out := make([]event.Packet, 0, n)
	deadline := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case pkt, ok := <-ch:
			if !ok {
				t.Fatalf("packet channel closed early: got %d want %d", len(out), n)
			}
			out = append(out, pkt)
		case <-deadline:
			t.Fatalf("timeout waiting for %d packets, got %d", n, len(out))
		}
	}
	return out
}

// TestMobileSourceEndToEnd 覆盖 open → data（直通分块/多方向）→ close。
//
// 分帧职责已下放插件：本 Source 不做应用层重组，每个数据块原样成为
// 一个 packet（含被 TCP 拆分的半块），协议帧边界的判定由解码插件完成。
// 这里验证 packet 的 LinkType/Src/Dst/Protocol/Metadata 与直通结果。
func TestMobileSourceEndToEnd(t *testing.T) {
	src, addr := startSource(t)
	stream := openPush(t, addr)

	mustSend := func(evt *proto.AgentEvent) {
		t.Helper()
		if err := stream.Send(evt); err != nil {
			t.Fatalf("stream.Send: %v", err)
		}
	}

	mustSend(&proto.AgentEvent{
		ConnId: "c1",
		Event: &proto.AgentEvent_Open{Open: &proto.ConnOpen{
			ClientAddr: "10.0.0.5:50000",
			ServerAddr: "1.2.3.4:443",
			Network:    "tcp",
			App:        "com.game.demo",
			Device:     "pixel-8",
		}},
	})
	// 两个完整块 + 一个被 TCP 拆分的块（分两次推送，原样透传）
	mustSend(dataEvent("c1", "request", []byte("login")))
	mustSend(dataEvent("c1", "request", []byte("move x=1 ")))
	mustSend(dataEvent("c1", "request", []byte("y=2")))
	// 服务端响应
	mustSend(dataEvent("c1", "response", []byte("login_ok")))
	// 关闭连接
	mustSend(&proto.AgentEvent{ConnId: "c1", Event: &proto.AgentEvent_Close{Close: &proto.ConnClose{Reason: "fin"}}})

	if _, err := stream.CloseAndRecv(); err != nil {
		t.Fatalf("close and recv: %v", err)
	}

	// 期望 4 个直通 packet：login / move x=1 （半块）/ y=2（半块）/ login_ok
	pkts := readN(t, src.Packets(), 4)

	req := []event.Packet{pkts[0], pkts[1], pkts[2]}
	resp := pkts[3]
	for i, p := range req {
		if p.Protocol != "tcp" {
			t.Errorf("req[%d] protocol = %q, want tcp", i, p.Protocol)
		}
		if p.LinkType != event.LinkTypeProxyPayload {
			t.Errorf("req[%d] link_type = %d, want ProxyPayload", i, p.LinkType)
		}
		if p.Src.String() != "10.0.0.5:50000" || p.Dst.String() != "1.2.3.4:443" {
			t.Errorf("req[%d] src/dst = %s/%s, want 10.0.0.5:50000/1.2.3.4:443", i, p.Src, p.Dst)
		}
		if p.Metadata["direction"] != "request" {
			t.Errorf("req[%d] direction metadata = %v, want request", i, p.Metadata["direction"])
		}
		if p.Metadata[capture.MetaAppPackage] != "com.game.demo" {
			t.Errorf("req[%d] app = %v", i, p.Metadata[capture.MetaAppPackage])
		}
	}
	if string(req[0].Raw) != "login" ||
		string(req[1].Raw) != "move x=1 " || string(req[2].Raw) != "y=2" {
		t.Errorf("request payloads mismatch: %q / %q / %q", req[0].Raw, req[1].Raw, req[2].Raw)
	}

	if resp.Src.String() != "1.2.3.4:443" || resp.Dst.String() != "10.0.0.5:50000" {
		t.Errorf("resp src/dst = %s/%s, want reversed", resp.Src, resp.Dst)
	}
	if resp.Metadata["direction"] != "response" {
		t.Errorf("resp direction = %v, want response", resp.Metadata["direction"])
	}
	if string(resp.Raw) != "login_ok" {
		t.Errorf("resp payload = %q, want login_ok", resp.Raw)
	}
}

// TestMobileSourceDataBeforeOpen 验证 open 未到时的数据被丢弃（agent 违约）并计入错误。
func TestMobileSourceDataBeforeOpen(t *testing.T) {
	src, addr := startSource(t)
	stream := openPush(t, addr)

	if err := stream.Send(dataEvent("ghost", "request", []byte("orphan"))); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		t.Fatal(err)
	}
	if got := src.Stats().Errors; got != 1 {
		t.Fatalf("want 1 error, got %d", got)
	}
}

// TestMobileSourceCloseSide 验证 ConnClose.close_side 会在 TCP 连接关闭时发射
// 关闭标记帧（metadata["tcp_close"]="client"/"server"），供下游推导关闭方；
// 未知侧别与 UDP 不发射。
func TestMobileSourceCloseSide(t *testing.T) {
	src, addr := startSource(t)
	stream := openPush(t, addr)

	mustSend := func(evt *proto.AgentEvent) {
		t.Helper()
		if err := stream.Send(evt); err != nil {
			t.Fatalf("stream.Send: %v", err)
		}
	}

	// TCP：server 侧关闭 → 发射 tcp_close="server" 标记。
	mustSend(&proto.AgentEvent{ConnId: "c1", Event: &proto.AgentEvent_Open{Open: &proto.ConnOpen{
		ClientAddr: "10.0.0.5:50000", ServerAddr: "1.2.3.4:443", Network: "tcp",
	}}})
	mustSend(dataEvent("c1", "request", []byte("hi")))
	mustSend(&proto.AgentEvent{ConnId: "c1", Event: &proto.AgentEvent_Close{Close: &proto.ConnClose{CloseSide: 2}}})

	// UDP：无连接关闭语义，即使上报 close_side 也不发射标记。
	mustSend(&proto.AgentEvent{ConnId: "u1", Event: &proto.AgentEvent_Open{Open: &proto.ConnOpen{
		ClientAddr: "10.0.0.5:50001", ServerAddr: "1.2.3.4:5300", Network: "udp",
	}}})
	mustSend(dataEvent("u1", "request", []byte("quic")))
	mustSend(&proto.AgentEvent{ConnId: "u1", Event: &proto.AgentEvent_Close{Close: &proto.ConnClose{CloseSide: 1}}})

	if _, err := stream.CloseAndRecv(); err != nil {
		t.Fatalf("close and recv: %v", err)
	}

	// 期望 3 个 packet：c1 数据 + c1 关闭标记 + u1 数据（u1 关闭不发射）。
	pkts := readN(t, src.Packets(), 3)
	if string(pkts[0].Raw) != "hi" {
		t.Fatalf("c1 data payload = %q", pkts[0].Raw)
	}
	marker := pkts[1]
	if marker.LinkType != event.LinkTypeProxyPayload {
		t.Fatalf("marker link_type = %d, want ProxyPayload", marker.LinkType)
	}
	if marker.Metadata[tcpCloseKey] != "server" {
		t.Fatalf("marker tcp_close = %v, want server", marker.Metadata[tcpCloseKey])
	}
	if marker.Src.String() != "10.0.0.5:50000" || marker.Dst.String() != "1.2.3.4:443" {
		t.Fatalf("marker src/dst = %s/%s", marker.Src, marker.Dst)
	}
	if string(pkts[2].Raw) != "quic" {
		t.Fatalf("u1 data payload = %q", pkts[2].Raw)
	}
	// u1 之后不应再有标记帧：通道应立即关闭（CloseAndRecv 已收流，source.run 结束）。
	select {
	case pkt, ok := <-src.Packets():
		t.Fatalf("unexpected extra packet after u1 close: ok=%v pkt=%+v", ok, pkt)
	case <-time.After(200 * time.Millisecond):
	}
}
