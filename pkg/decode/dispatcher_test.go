package decode

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"gametrace/pkg/event"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// fakeDecoderV2 返回正常的 MsgPack 编码结果。
type fakeDecoderV2 struct {
	pb.UnimplementedDecoderServer
}

func (f *fakeDecoderV2) DecodeV2(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	payload := event.ValueObject(map[string]event.Value{
		"data": event.ValueObject(map[string]event.Value{
			"ok": event.ValueBool(true),
		}),
	})
	msgpackData, err := payload.MarshalMsgpack()
	if err != nil {
		return err
	}
	_ = stream.Send(&pb.DecodeResponseV2{
		InputId:        req.InputId,
		EventType:      "test.event",
		PayloadMsgpack: msgpackData,
	})
	_ = stream.Send(&pb.DecodeResponseV2{
		InputId: req.InputId,
		Done:    true,
	})
	return nil
}

// errorDecoderV2 在结果中返回错误（V2 中错误为 per-result 而非 per-call）。
type errorDecoderV2 struct {
	pb.UnimplementedDecoderServer
}

func (f *errorDecoderV2) DecodeV2(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	_ = stream.Send(&pb.DecodeResponseV2{
		InputId: req.InputId,
		Error:   "bad payload",
	})
	_ = stream.Send(&pb.DecodeResponseV2{
		InputId: req.InputId,
		Done:    true,
	})
	return nil
}

func TestDispatcherDecodeV2Error(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "test.sock")
	lis, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterDecoderServer(srv, &errorDecoderV2{})
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient("unix:"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	col := NewErrorCollector()
	d, err := NewDispatcher(pb.NewDecoderClient(conn), "test-session", nil, WithErrorCollector(col))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	pkt := event.Packet{
		ID:        "raw-42",
		Timestamp: time.Now(),
		Protocol:  "tcp",
		Raw:       []byte("x"),
		Src:       netip.MustParseAddrPort("10.0.0.1:1"),
		Dst:       netip.MustParseAddrPort("10.0.0.2:2"),
		Metadata:  make(map[string]any),
	}
	evs, err := d.DecodeV2(context.Background(), pkt)
	if err != nil {
		t.Fatal(err)
	}
	// V2 中错误结果被跳过，应返回空列表
	if len(evs) != 0 {
		t.Fatalf("expected 0 events from error decoder, got %d", len(evs))
	}

	// 但失败原因必须被采集下来：过去只打日志就 continue，前端因此只能看到计数。
	groups := col.Groups()
	if len(groups) != 1 {
		t.Fatalf("失败分组数 = %d, want 1（同一条错误文案）", len(groups))
	}
	g := groups[0]
	if g.Kind != ErrKindPlugin {
		t.Errorf("Kind = %q, want %q", g.Kind, ErrKindPlugin)
	}
	if g.Template != "bad payload" {
		t.Errorf("Template = %q, want %q", g.Template, "bad payload")
	}
	if g.Sample != "bad payload" || g.SampleRawID != "raw-42" {
		t.Errorf("样本 = (%q, %q), want (bad payload, raw-42)：没有代表包就无法下钻", g.Sample, g.SampleRawID)
	}
	if g.SampleSrc == "" || g.SampleDst == "" {
		t.Error("样本应带地址对")
	}
}

// brokenStreamDecoderV2 收下请求后直接断开流，用于验证传输层失败的归因。
type brokenStreamDecoderV2 struct {
	pb.UnimplementedDecoderServer
}

func (f *brokenStreamDecoderV2) DecodeV2(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

// TestDispatcherTransportErrorKeepsPacketContext 覆盖：流断开时 Future 仍带包上下文，
// 调用方才能把传输层失败也归到具体包上（否则又是「只有一个数字」）。
func TestDispatcherTransportErrorKeepsPacketContext(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "test.sock")
	lis, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterDecoderServer(srv, &brokenStreamDecoderV2{})
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient("unix:"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	col := NewErrorCollector()
	d, err := NewDispatcher(pb.NewDecoderClient(conn), "test-session", nil, WithErrorCollector(col))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	pkt := event.Packet{
		ID:        "raw-7",
		Timestamp: time.Now(),
		Protocol:  "tcp",
		Raw:       []byte("x"),
		Src:       netip.MustParseAddrPort("10.0.0.1:1"),
		Dst:       netip.MustParseAddrPort("10.0.0.2:2"),
		Metadata:  make(map[string]any),
	}
	f, err := d.Submit(pkt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, werr := f.Wait(ctx); werr == nil {
		t.Fatal("期望传输层错误，实际成功")
	} else {
		// capture_task 的 decode worker 就是这样归因的。
		col.Add(ErrKindTransport, f.PacketID(), f.Src(), f.Dst(), werr.Error())
	}

	g := col.Groups()[0]
	if g.Kind != ErrKindTransport {
		t.Errorf("Kind = %q, want %q", g.Kind, ErrKindTransport)
	}
	if g.SampleRawID != "raw-7" {
		t.Errorf("SampleRawID = %q, want raw-7：传输层失败也要能定位到包", g.SampleRawID)
	}
}

func TestDispatcherDecodeV2Success(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "test.sock")
	_ = os.Remove(socket)
	lis, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterDecoderServer(srv, &fakeDecoderV2{})
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient("unix:"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	d, err := NewDispatcher(pb.NewDecoderClient(conn), "test-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	pkt := event.Packet{
		Timestamp: time.Now(),
		Protocol:  "tcp",
		Raw:       []byte("x"),
		Src:       netip.MustParseAddrPort("10.0.0.1:1"),
		Dst:       netip.MustParseAddrPort("10.0.0.2:2"),
		Metadata:  make(map[string]any),
	}
	evs, err := d.DecodeV2(context.Background(), pkt)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Identity.Type != "test.event" {
		t.Fatalf("unexpected event type: %s", ev.Identity.Type)
	}
	// 验证 payload 内容
	obj, ok := ev.Payload.Value.AsObject()
	if !ok {
		t.Fatal("payload is not object")
	}
	data, ok := obj["data"]
	if !ok {
		t.Fatal("payload missing 'data' key")
	}
	dataObj, ok := data.AsObject()
	if !ok {
		t.Fatal("data is not object")
	}
	if v, ok := dataObj["ok"]; !ok || !v.Bool {
		t.Fatalf("expected data.ok=true, got %v", v)
	}
}

// closeAfterRecvDecoder 接收一个请求后关闭流，模拟断流。
type closeAfterRecvDecoder struct {
	pb.UnimplementedDecoderServer
}

func (f *closeAfterRecvDecoder) DecodeV2(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]) error {
	_, err := stream.Recv()
	if err != nil {
		return err
	}
	// 直接关闭流（不发送响应），模拟对端断流。
	return nil
}

func TestDispatcherIsHealthy(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "test.sock")
	_ = os.Remove(socket)
	lis, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterDecoderServer(srv, &closeAfterRecvDecoder{})
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient("unix:"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	d, err := NewDispatcher(pb.NewDecoderClient(conn), "test-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// 新建时流是健康的。
	if !d.IsHealthy() {
		t.Fatal("expected dispatcher to be healthy after creation")
	}

	// 提交一个包触发对端关闭流。
	pkt := event.Packet{
		Timestamp: time.Now(),
		Protocol:  "tcp",
		Raw:       []byte("x"),
		Src:       netip.MustParseAddrPort("10.0.0.1:1"),
		Dst:       netip.MustParseAddrPort("10.0.0.2:2"),
		Metadata:  make(map[string]any),
	}
	_, _ = d.Submit(pkt)

	// 等待 recvLoop 退出。
	requireEventuallyHealthy(t, d, false, 2*time.Second)
}

// requireEventuallyHealthy 轮询直到 d.IsHealthy() == want 或超时。
func requireEventuallyHealthy(t *testing.T, d *Dispatcher, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.IsHealthy() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for dispatcher healthy=%v (got %v)", want, d.IsHealthy())
}

func TestInferDirection(t *testing.T) {
	tests := []struct {
		name       string
		serverPort int
		src        uint16
		dst        uint16
		want       string
	}{
		{"well-known server port", 0, 12345, 80, "client_to_server"},
		{"well-known server response", 0, 80, 12345, "server_to_client"},
		{"game server port client to server", 8989, 12345, 8989, "client_to_server"},
		{"game server port server to client", 8989, 8989, 12345, "server_to_client"},
		{"game server port both equal", 8989, 8989, 8989, "unknown"},
		{"game server port with well-known fallback", 8989, 12345, 80, "client_to_server"},
		{"unknown ports", 0, 12345, 8989, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferDirection(tt.src, tt.dst, tt.serverPort)
			if got != tt.want {
				t.Errorf("inferDirection(%d, %d, %d) = %q, want %q", tt.src, tt.dst, tt.serverPort, got, tt.want)
			}
		})
	}
}

// TestPeerKeyFromPacket 覆盖稳定对端标识的推导规则。
//
// 它存在的理由：FlowID 含客户端临时端口，重连一次就换值，不能当实体身份用；
// 对端标识只保留客户端 IP 与服务端地址，方向判不出来时返回空串（调用方退回会话维度），
// 宁可少延续也不能把两个不同客户端并成一个实体。
func TestPeerKeyFromPacket(t *testing.T) {
	pkt := func(src, dst string) event.Packet {
		return event.Packet{
			Src: netip.MustParseAddrPort(src),
			Dst: netip.MustParseAddrPort(dst),
		}
	}
	tests := []struct {
		name       string
		src        string
		dst        string
		serverPort int
		want       string
	}{
		{
			name: "客户端端口不入身份：同一客户端重连得到同一标识",
			// 两次连接只有客户端临时端口不同，对端标识必须相同。
			src: "10.0.0.1:1111", dst: "10.0.0.2:9250", serverPort: 9250,
			want: "10.0.0.1|10.0.0.2:9250",
		},
		{
			name: "服务端发起的包方向相反也得到同一标识",
			src: "10.0.0.2:9250", dst: "10.0.0.1:2222", serverPort: 9250,
			want: "10.0.0.1|10.0.0.2:9250",
		},
		{
			name: "另一客户端必须是另一个标识",
			src: "10.0.0.9:3333", dst: "10.0.0.2:9250", serverPort: 9250,
			want: "10.0.0.9|10.0.0.2:9250",
		},
		{
			name: "未知端口且无服务端提示：判不出方向，返回空串",
			src: "10.0.0.1:1111", dst: "10.0.0.2:9250", serverPort: 0,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := peerKeyFromPacket(pkt(tt.src, tt.dst), tt.serverPort); got != tt.want {
				t.Errorf("peerKeyFromPacket(%s -> %s, serverPort=%d) = %q, want %q",
					tt.src, tt.dst, tt.serverPort, got, tt.want)
			}
		})
	}

	// 同一客户端换临时端口：FlowID 会变，PeerKey 不能变。
	a := peerKeyFromPacket(pkt("10.0.0.1:1111", "10.0.0.2:9250"), 9250)
	b := peerKeyFromPacket(pkt("10.0.0.1:55555", "10.0.0.2:9250"), 9250)
	if a != b {
		t.Errorf("重连换了临时端口后 PeerKey 变了：%q -> %q（基线会因此断掉）", a, b)
	}
	if FlowIDFromEndpoints("10.0.0.1:1111", "10.0.0.2:9250", "tcp") ==
		FlowIDFromEndpoints("10.0.0.1:55555", "10.0.0.2:9250", "tcp") {
		t.Error("前提不成立：FlowID 本应随临时端口变化")
	}
}
