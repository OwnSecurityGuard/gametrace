package sdk

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestDecoder_DecodeV2_Success(t *testing.T) {
	d := &Decoder{
		decodeFuncV2: func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
			return stream.Send(&pb.DecodeResponseV2{
				InputId:   req.GetInputId(),
				EventType: "test.event",
				Done:      true,
			})
		},
	}

	reqCh := make(chan *pb.DecodeRequest, 2)
	respCh := make(chan *pb.DecodeResponseV2, 2)

	mockStream := &mockDecodeV2Stream{
		recvFunc: func() (*pb.DecodeRequest, error) {
			req, ok := <-reqCh
			if !ok {
				return nil, io.EOF
			}
			return req, nil
		},
		sendFunc: func(resp *pb.DecodeResponseV2) error {
			respCh <- resp
			return nil
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.DecodeV2(mockStream)
	}()

	reqCh <- &pb.DecodeRequest{
		SessionId:    "sess-1",
		ProtocolHint: "http",
		Payload:      []byte("GET / HTTP/1.1\r\n"),
	}
	close(reqCh)

	select {
	case resp := <-respCh:
		if resp.EventType != "test.event" {
			t.Errorf("unexpected event_type: %s", resp.EventType)
		}
		if !resp.Done {
			t.Errorf("expected Done=true")
		}
	case err := <-errCh:
		if err != nil {
			t.Errorf("DecodeV2 error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for DecodeV2 to complete")
	}
}

func TestDecoder_DecodeV2_PanicRecovery(t *testing.T) {
	decoded := make(chan struct{}, 1)
	callCount := 0
	d := &Decoder{
		decodeFuncV2: func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
			callCount++
			select {
			case decoded <- struct{}{}:
			default:
			}
			panic("intentional panic for test")
		},
	}

	receivedRespCh := make(chan *pb.DecodeResponseV2, 2)
	mockStream := &mockDecodeV2Stream{
		recvFunc: func() (*pb.DecodeRequest, error) {
			if callCount == 0 {
				return &pb.DecodeRequest{SessionId: "sess-1"}, nil
			}
			return nil, io.EOF
		},
		sendFunc: func(resp *pb.DecodeResponseV2) error {
			receivedRespCh <- resp
			return nil
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.DecodeV2(mockStream)
	}()

	// Wait for decodeFuncV2 to be called (it panics inside)
	select {
	case <-decoded:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for decodeFuncV2 to be called")
	}

	// Wait for the error response (panic was recovered)
	select {
	case resp := <-receivedRespCh:
		if resp.Error == "" {
			t.Fatal("expected error response for panic, got empty error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for error response")
	}

	// Wait for DecodeV2 to finish (EOF received after panic recovery)
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("DecodeV2 returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for DecodeV2 to finish")
	}
}

func TestDecoder_DecodeV2_DecodeError(t *testing.T) {
	d := &Decoder{
		decodeFuncV2: func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
			return fmt.Errorf("decode error")
		},
	}

	callCount := 0
	receivedRespCh := make(chan *pb.DecodeResponseV2, 2)
	mockStream := &mockDecodeV2Stream{
		recvFunc: func() (*pb.DecodeRequest, error) {
			callCount++
			if callCount == 1 {
				return &pb.DecodeRequest{SessionId: "sess-1"}, nil
			}
			return nil, io.EOF
		},
		sendFunc: func(resp *pb.DecodeResponseV2) error {
			receivedRespCh <- resp
			return fmt.Errorf("send failed")
		},
	}

	err := d.DecodeV2(mockStream)
	// V2: decodeFuncV2 returns an error; DecodeV2 sends error response (ignoring send errors)
	// and continues loop; EOF terminates the stream cleanly.
	if err != nil {
		t.Fatalf("expected nil on EOF, got: %v", err)
	}

	select {
	case resp := <-receivedRespCh:
		if resp.Error != "decode error" {
			t.Errorf("unexpected error in response: %s", resp.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for error response")
	}
}

type mockDecodeV2Stream struct {
	recvFunc func() (*pb.DecodeRequest, error)
	sendFunc func(*pb.DecodeResponseV2) error
}

func (m *mockDecodeV2Stream) Context() context.Context     { return context.Background() }
func (m *mockDecodeV2Stream) SendMsg(interface{}) error    { return nil }
func (m *mockDecodeV2Stream) RecvMsg(interface{}) error    { return nil }
func (m *mockDecodeV2Stream) SendHeader(metadata.MD) error { return nil }
func (m *mockDecodeV2Stream) SetHeader(metadata.MD) error  { return nil }
func (m *mockDecodeV2Stream) SetTrailer(metadata.MD)       {}
func (m *mockDecodeV2Stream) Send(resp *pb.DecodeResponseV2) error {
	if m.sendFunc != nil {
		return m.sendFunc(resp)
	}
	return nil
}
func (m *mockDecodeV2Stream) Recv() (*pb.DecodeRequest, error) {
	if m.recvFunc != nil {
		return m.recvFunc()
	}
	return nil, nil
}

func TestAdvertiseTCP(t *testing.T) {
	// GT_DECODER_PUBLIC_ADDR 显式指定时原样返回（最高优先级）。
	if got := advertiseTCP("10.0.0.5:19001", "0.0.0.0:19001"); got != "10.0.0.5:19001" {
		t.Errorf("explicit pub must win, got %q", got)
	}

	// 绑定到具体 IP 时原样上报，尊重用户意图。
	if got := advertiseTCP("", "192.168.1.10:19001"); got != "192.168.1.10:19001" {
		t.Errorf("specific bind address must be kept, got %q", got)
	}
	if got := advertiseTCP("", "127.0.0.1:19001"); got != "127.0.0.1:19001" {
		t.Errorf("loopback bind address must be kept, got %q", got)
	}

	// 通配符监听地址应替换为本机非回环 IPv4（若本机存在），
	// 使 Docker 容器内的平台侧可以拨号回连，而不是解析到容器自身。
	ip := hostIPv4()
	if ip == "" {
		t.Skip("no non-loopback IPv4 on this machine")
	}
	want := net.JoinHostPort(ip, "19001")
	for _, actual := range []string{"0.0.0.0:19001", "[::]:19001", ":19001"} {
		if got := advertiseTCP("", actual); got != want {
			t.Errorf("advertiseTCP(%q) = %q, want %q", actual, got, want)
		}
	}
}

func TestHostIPv4(t *testing.T) {
	ip := hostIPv4()
	if ip == "" {
		t.Skip("no non-loopback IPv4 on this machine")
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil || parsed.IsLoopback() {
		t.Errorf("hostIPv4() = %q, want a non-loopback IPv4 address", ip)
	}
}

// mockRegistryClient 是 PluginRegistryClient 的最小实现，
// 只用于在单测中驱动 heartbeatLoop。
type mockRegistryClient struct {
	heartbeatErr   error
	heartbeatCalls atomic.Int32
}

func (m *mockRegistryClient) Register(context.Context, *pb.RegisterRequest, ...grpc.CallOption) (*pb.RegisterResponse, error) {
	return &pb.RegisterResponse{InstanceId: "inst-1", HeartbeatIntervalSec: 1}, nil
}
func (m *mockRegistryClient) Heartbeat(_ context.Context, _ *pb.HeartbeatRequest, _ ...grpc.CallOption) (*pb.HeartbeatResponse, error) {
	m.heartbeatCalls.Add(1)
	if m.heartbeatErr != nil {
		return nil, m.heartbeatErr
	}
	return &pb.HeartbeatResponse{}, nil
}
func (m *mockRegistryClient) Deregister(context.Context, *pb.DeregisterRequest, ...grpc.CallOption) (*pb.DeregisterResponse, error) {
	return &pb.DeregisterResponse{}, nil
}
func (m *mockRegistryClient) Connect(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[pb.TunnelFrame, pb.TunnelFrame], error) {
	return nil, fmt.Errorf("not implemented")
}

// TestHeartbeatLoop_FailureSignalsLost：心跳失败（平台侧重启/失联）时，
// lost channel 必须被关闭，让主循环能感知失联并触发重连。
func TestHeartbeatLoop_FailureSignalsLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lost := make(chan struct{})
	client := &mockRegistryClient{heartbeatErr: fmt.Errorf("registry unreachable")}
	go heartbeatLoop(ctx, client, "inst-1", 10*time.Millisecond, lost)

	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat loss was not signaled on the lost channel")
	}
	if client.heartbeatCalls.Load() == 0 {
		t.Fatal("expected at least one heartbeat attempt")
	}
}

// TestHeartbeatLoop_SuccessKeepsAlive：心跳连续成功时 lost 不应被关闭；
// context 取消后 goroutine 安静退出。
func TestHeartbeatLoop_SuccessKeepsAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lost := make(chan struct{})
	client := &mockRegistryClient{}
	go heartbeatLoop(ctx, client, "inst-1", 10*time.Millisecond, lost)

	deadline := time.Now().Add(2 * time.Second)
	for client.heartbeatCalls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if client.heartbeatCalls.Load() < 3 {
		t.Fatalf("expected >=3 successful heartbeats, got %d", client.heartbeatCalls.Load())
	}
	select {
	case <-lost:
		t.Fatal("lost channel closed while heartbeats succeed")
	default:
	}

	// context 取消后心跳循环应退出（不泄漏 goroutine）。
	cancel()
}
