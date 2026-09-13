package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// ---------- 进程内隧道帧管道 ----------

// tunnelPipe 提供一对互联的 BidiStreamingClient/Server 假实现，
// 用于不依赖真实 gRPC 连接地驱动 runTunnel。
type tunnelPipe struct {
	ctx       context.Context
	c2s       chan *pb.TunnelFrame // client(mux) → server(test)
	s2c       chan *pb.TunnelFrame // server(test) → client(mux)
	closeOnce sync.Once
	closed    chan struct{}
}

func newTunnelPipe(ctx context.Context) *tunnelPipe {
	return &tunnelPipe{
		ctx:    ctx,
		c2s:    make(chan *pb.TunnelFrame, 16),
		s2c:    make(chan *pb.TunnelFrame, 16),
		closed: make(chan struct{}),
	}
}

func (p *tunnelPipe) Close() { p.closeOnce.Do(func() { close(p.closed) }) }

// clientEnd 是传给 runTunnel 的一端（只用到 Send/Recv/Context）。
type clientEnd struct{ p *tunnelPipe }

func (c clientEnd) Recv() (*pb.TunnelFrame, error) {
	select {
	case f, ok := <-c.p.s2c:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	case <-c.p.closed:
		return nil, io.EOF
	case <-c.p.ctx.Done():
		return nil, c.p.ctx.Err()
	}
}
func (c clientEnd) Send(f *pb.TunnelFrame) error {
	select {
	case c.p.c2s <- f:
		return nil
	case <-c.p.closed:
		return errors.New("pipe closed")
	case <-c.p.ctx.Done():
		return c.p.ctx.Err()
	}
}
func (c clientEnd) Context() context.Context     { return c.p.ctx }
func (c clientEnd) SendMsg(interface{}) error    { return nil }
func (c clientEnd) RecvMsg(interface{}) error    { return nil }
func (c clientEnd) Header() (metadata.MD, error) { return nil, nil }
func (c clientEnd) Trailer() metadata.MD         { return nil }
func (c clientEnd) CloseSend() error             { return nil }

// serverEnd 是测试扮演宿主的一端（Send 向插件方向写，Recv 读插件应答）。
type serverEnd struct{ p *tunnelPipe }

func (s serverEnd) Recv() (*pb.TunnelFrame, error) {
	select {
	case f, ok := <-s.p.c2s:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	case <-s.p.closed:
		return nil, io.EOF
	case <-s.p.ctx.Done():
		return nil, s.p.ctx.Err()
	}
}
func (s serverEnd) Send(f *pb.TunnelFrame) error {
	select {
	case s.p.s2c <- f:
		return nil
	case <-s.p.closed:
		return errors.New("pipe closed")
	case <-s.p.ctx.Done():
		return s.p.ctx.Err()
	}
}
func (s serverEnd) Context() context.Context     { return s.p.ctx }
func (s serverEnd) SendMsg(interface{}) error    { return nil }
func (s serverEnd) RecvMsg(interface{}) error    { return nil }
func (s serverEnd) SendHeader(metadata.MD) error { return nil }
func (s serverEnd) SetHeader(metadata.MD) error  { return nil }
func (s serverEnd) SetTrailer(metadata.MD)       {}

// recvFrame 带 超时读取一帧并断言 stream_id。
func recvFrame(t *testing.T, se serverEnd, wantStreamID uint32) *pb.TunnelFrame {
	t.Helper()
	select {
	case f := <-se.p.c2s:
		if f.GetStreamId() != wantStreamID {
			t.Fatalf("expected stream_id %d, got %d", wantStreamID, f.GetStreamId())
		}
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tunnel frame")
		return nil
	}
}

// ---------- 测试 ----------

func TestRunTunnel_DecodeRoundTrip(t *testing.T) {
	echoDecoder := &Decoder{decodeFuncV2: func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
		return stream.Send(&pb.DecodeResponseV2{
			InputId:   req.GetInputId(),
			EventType: "echo.event",
			Done:      true,
		})
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe := newTunnelPipe(ctx)
	defer pipe.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- runTunnel(ctx, clientEnd{pipe}, echoDecoder) }()

	se := serverEnd{pipe}

	// 首个 request 帧隐式开流（stream_id=1）
	reqData, _ := proto.Marshal(&pb.DecodeRequest{InputId: "in-1", SessionId: "s1"})
	if err := se.Send(&pb.TunnelFrame{StreamId: 1, Payload: &pb.TunnelFrame_Request{Request: reqData}}); err != nil {
		t.Fatal(err)
	}

	// 应收到一个 response 帧（可反序列化为 DecodeResponseV2）
	respFrame := recvFrame(t, se, 1)
	respData, ok := respFrame.Payload.(*pb.TunnelFrame_Response)
	if !ok {
		t.Fatalf("expected response frame, got %T", respFrame.Payload)
	}
	var resp pb.DecodeResponseV2
	if err := proto.Unmarshal(respData.Response, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.GetInputId() != "in-1" || resp.GetEventType() != "echo.event" || !resp.GetDone() {
		t.Fatalf("unexpected response: input_id=%s event_type=%s done=%v", resp.GetInputId(), resp.GetEventType(), resp.GetDone())
	}

	// half_close → DecodeV2 正常返回 → end 帧（error 为空）
	if err := se.Send(&pb.TunnelFrame{StreamId: 1, Payload: &pb.TunnelFrame_HalfClose{HalfClose: true}}); err != nil {
		t.Fatal(err)
	}
	endFrame := recvFrame(t, se, 1)
	end, ok := endFrame.Payload.(*pb.TunnelFrame_End)
	if !ok {
		t.Fatalf("expected end frame, got %T", endFrame.Payload)
	}
	if end.End.GetError() != "" {
		t.Fatalf("expected clean end, got error: %s", end.End.GetError())
	}

	// 新 stream_id（2）：先 request 再 reset → end 帧带错误
	reqData2, _ := proto.Marshal(&pb.DecodeRequest{InputId: "in-2"})
	if err := se.Send(&pb.TunnelFrame{StreamId: 2, Payload: &pb.TunnelFrame_Request{Request: reqData2}}); err != nil {
		t.Fatal(err)
	}
	_ = recvFrame(t, se, 2) // response 帧
	if err := se.Send(&pb.TunnelFrame{StreamId: 2, Payload: &pb.TunnelFrame_Reset_{Reset_: &pb.StreamReset{Reason: "cancel"}}}); err != nil {
		t.Fatal(err)
	}
	endFrame2 := recvFrame(t, se, 2)
	end2, ok := endFrame2.Payload.(*pb.TunnelFrame_End)
	if !ok {
		t.Fatalf("expected end frame for stream 2, got %T", endFrame2.Payload)
	}
	if !strings.Contains(end2.End.GetError(), "reset") {
		t.Fatalf("expected reset error in end frame, got: %q", end2.End.GetError())
	}
}

// fakeTunnelRegistry 是 bufconn e2e 测试用的宿主侧 PluginRegistry 实现。
type fakeTunnelRegistry struct {
	pb.UnimplementedPluginRegistryServer
	t *testing.T

	mu           sync.Mutex
	registered   bool
	verifyOnce   sync.Once
	sawToken     string
	sawTunnel    bool
	registerHits atomic.Int32
	verifiedDone chan struct{} // 由测试注入，Register 校验通过后关闭
	tunnelDone   chan struct{} // 由测试注入，隧道解码 round trip 完成后关闭
}

func (r *fakeTunnelRegistry) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	r.registerHits.Add(1)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			r.sawToken = vals[0]
		}
	}
	r.mu.Lock()
	already := r.registered
	r.registered = true
	r.sawTunnel = req.GetTunnel()
	r.mu.Unlock()

	if already {
		// 重连后挂起，避免测试期间反复 Register
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if req.GetTunnel() != true {
		r.t.Error("expected RegisterRequest.tunnel=true")
	}
	if r.sawToken != "Bearer test-token" {
		r.t.Errorf("expected authorization Bearer token, got %q", r.sawToken)
	}
	r.verifyOnce.Do(func() { close(r.verifiedDone) })
	return &pb.RegisterResponse{InstanceId: "inst-1", HeartbeatIntervalSec: 10}, nil
}

func (r *fakeTunnelRegistry) Connect(stream pb.PluginRegistry_ConnectServer) error {
	// 扮演宿主 TunnelHub：发一个请求帧，收 response + end
	reqData, _ := proto.Marshal(&pb.DecodeRequest{InputId: "e2e-in-1"})
	if err := stream.Send(&pb.TunnelFrame{StreamId: 7, Payload: &pb.TunnelFrame_Request{Request: reqData}}); err != nil {
		return err
	}
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		switch p := frame.Payload.(type) {
		case *pb.TunnelFrame_Response:
			var resp pb.DecodeResponseV2
			if err := proto.Unmarshal(p.Response, &resp); err != nil {
				r.t.Error(err)
			}
			if resp.GetInputId() != "e2e-in-1" || resp.GetEventType() != "e2e.event" {
				r.t.Errorf("unexpected response over tunnel: input_id=%s event_type=%s", resp.GetInputId(), resp.GetEventType())
			}
			if err := stream.Send(&pb.TunnelFrame{StreamId: 7, Payload: &pb.TunnelFrame_HalfClose{HalfClose: true}}); err != nil {
				return err
			}
		case *pb.TunnelFrame_End:
			if p.End.GetError() != "" {
				r.t.Errorf("expected clean end, got: %s", p.End.GetError())
			}
			close(r.tunnelDone)
			return nil
		default:
			r.t.Errorf("unexpected frame from plugin: %T", frame.Payload)
		}
	}
}

func TestRunRegisterLoopWithOptions_TunnelEndToEnd(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	reg := &fakeTunnelRegistry{
		t: t,

		verifiedDone: make(chan struct{}),
		tunnelDone:   make(chan struct{}),
	}

	grpcServer := grpc.NewServer()
	pb.RegisterPluginRegistryServer(grpcServer, reg)
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	t.Setenv("GT_REGISTRY_ADDR", lis.Addr().String())

	// ReadManifest 读取当前目录的 plugin.yaml；临时 chdir 到参考示例目录
	// 以复用其合法 manifest。
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	if err := os.Chdir(filepath.Join(cwd, "examples", "http-stream-decoder")); err != nil {
		t.Fatal(err)
	}

	decodeFunc := func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
		return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), EventType: "e2e.event", Done: true})
	}

	go func() {
		RunRegisterLoopWithOptions(decodeFunc, RegisterOptions{Tunnel: true, AuthToken: "test-token"})
	}()

	select {
	case <-reg.verifiedDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for verified registration")
	}
	select {
	case <-reg.tunnelDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tunnel decode round trip")
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.sawToken != "Bearer test-token" {
		t.Errorf("token metadata not propagated: %q", reg.sawToken)
	}
	if !reg.sawTunnel {
		t.Error("tunnel flag not set on RegisterRequest")
	}

	// Connect 断开后（宿主侧 handler 返回），SDK 应按退避策略重新 Register + Connect
	deadline := time.After(10 * time.Second)
	for reg.registerHits.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for re-Register after tunnel drop")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ---------- 补充测试：并发逻辑流 / reset 后请求 / Connect 断开 ----------

func TestRunTunnel_ConcurrentStreams(t *testing.T) {
	echoDecoder := &Decoder{decodeFuncV2: func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
		return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), EventType: "echo.event", Done: true})
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe := newTunnelPipe(ctx)
	defer pipe.Close()

	go func() { _ = runTunnel(ctx, clientEnd{pipe}, echoDecoder) }()
	se := serverEnd{pipe}

	const n = 4
	// 交错发送 n 个逻辑流的请求帧
	for i := 0; i < n; i++ {
		id := uint32(10 + i)
		data, _ := proto.Marshal(&pb.DecodeRequest{InputId: fmt.Sprintf("in-%d", i)})
		if err := se.Send(&pb.TunnelFrame{StreamId: id, Payload: &pb.TunnelFrame_Request{Request: data}}); err != nil {
			t.Fatal(err)
		}
	}

	// 每个流各自收到 response，然后发 half_close 并收到干净 end
	seen := make(map[uint32]bool)
	for i := 0; i < n; i++ {
		select {
		case f := <-pipe.c2s:
			resp, ok := f.Payload.(*pb.TunnelFrame_Response)
			if !ok {
				t.Fatalf("expected response frame, got %T", f.Payload)
			}
			if seen[f.GetStreamId()] {
				t.Fatalf("duplicate response for stream %d", f.GetStreamId())
			}
			seen[f.GetStreamId()] = true
			var r pb.DecodeResponseV2
			if err := proto.Unmarshal(resp.Response, &r); err != nil {
				t.Fatal(err)
			}
			if r.GetEventType() != "echo.event" || !r.GetDone() {
				t.Fatalf("unexpected response: input_id=%s done=%v", r.GetInputId(), r.GetDone())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent responses")
		}
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct streams, got %d", n, len(seen))
	}
	for i := 0; i < n; i++ {
		id := uint32(10 + i)
		if err := se.Send(&pb.TunnelFrame{StreamId: id, Payload: &pb.TunnelFrame_HalfClose{HalfClose: true}}); err != nil {
			t.Fatal(err)
		}
		ef := recvFrame(t, se, id)
		if _, ok := ef.Payload.(*pb.TunnelFrame_End); !ok {
			t.Fatalf("expected end frame for stream %d, got %T", id, ef.Payload)
		}
	}
}

// TestRunTunnel_RequestAfterReset 覆盖评审指出的核心竞态：
// request 帧与 reset/half_close/teardown 并发、以及 reset 后同 id 的新请求，
// 均不得 panic（曾因 send on closed channel 崩溃）。
func TestRunTunnel_RequestAfterReset(t *testing.T) {
	slowDecoder := &Decoder{decodeFuncV2: func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
		// 慢解码，制造请求帧在 reset 到达时仍在入队的窗口
		time.Sleep(100 * time.Millisecond)
		return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true})
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe := newTunnelPipe(ctx)
	defer pipe.Close()

	done := make(chan error, 1)
	go func() { done <- runTunnel(ctx, clientEnd{pipe}, slowDecoder) }()
	se := serverEnd{pipe}

	// 同一 stream_id 连发多个 request（部分会与 reset 竞争），随后 reset
	for i := 0; i < 5; i++ {
		data, _ := proto.Marshal(&pb.DecodeRequest{InputId: "racy"})
		_ = se.Send(&pb.TunnelFrame{StreamId: 9, Payload: &pb.TunnelFrame_Request{Request: data}})
	}
	_ = se.Send(&pb.TunnelFrame{StreamId: 9, Payload: &pb.TunnelFrame_Reset_{Reset_: &pb.StreamReset{Reason: "cancel"}}})

	// 收 racy 流的 end 帧（应带 reset 错误）
	deadline := time.After(5 * time.Second)
	ends := 0
	firstEnd := ""
	for ends < 1 {
		select {
		case f := <-pipe.c2s:
			if e, ok := f.Payload.(*pb.TunnelFrame_End); ok {
				ends++
				firstEnd = e.End.GetError()
			}
		case <-deadline:
			t.Fatalf("timeout waiting for end frames, got %d", ends)
		}
	}
	if !strings.Contains(firstEnd, "reset") {
		t.Fatalf("expected reset error in end frame, got %q", firstEnd)
	}

	// 等 racy 流的 serveStream 结束并从 mux 移除（解码函数 100ms），
	// 之后同 id 的新请求应开启全新逻辑流，且不得 panic。
	time.Sleep(300 * time.Millisecond)
	data2, _ := proto.Marshal(&pb.DecodeRequest{InputId: "after-reset"})
	_ = se.Send(&pb.TunnelFrame{StreamId: 9, Payload: &pb.TunnelFrame_Request{Request: data2}})
	_ = se.Send(&pb.TunnelFrame{StreamId: 9, Payload: &pb.TunnelFrame_HalfClose{HalfClose: true}})

	for ends < 2 {
		select {
		case f := <-pipe.c2s:
			if e, ok := f.Payload.(*pb.TunnelFrame_End); ok {
				ends++
				if e.End.GetError() != "" {
					t.Fatalf("expected clean end for after-reset stream, got %q", e.End.GetError())
				}
			}
		case <-deadline:
			t.Fatalf("timeout waiting for end frames, got %d", ends)
		}
	}
}

// TestRunTunnel_ConnectDrop 验证 Connect 流断开后 runTunnel 返回错误，
// 由 RunRegisterLoop 沿用退避重连策略重新 Register/Connect。
func TestRunTunnel_ConnectDrop(t *testing.T) {
	echoDecoder := &Decoder{decodeFuncV2: func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
		return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true})
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipe := newTunnelPipe(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- runTunnel(ctx, clientEnd{pipe}, echoDecoder) }()

	pipe.Close() // 模拟宿主侧 Connect 流断开

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error when connect stream drops, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for runTunnel to return after connect drop")
	}
}
