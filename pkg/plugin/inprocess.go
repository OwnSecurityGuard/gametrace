package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"

	"google.golang.org/grpc/metadata"
)

// RegisterInProcessTunnel 把一个同进程解码器注册成隧道插件。
//
// 平台只支持隧道注册：真实插件自己拨 registry 并在同一连接上打开 Connect 流。
// 但模拟器与集成测试运行在与 registry 相同的进程里，没有可用网络端点，因此这里
// 用一条**内存 Connect 双向流**复刻同样的握手：先 Register 拿 instance_id，再把
// 它塞进流 metadata 完成精确绑定，最后两侧分别交给 TunnelHub 与 sdk.ServeTunnel。
// 宿主看到的是标准隧道，与外置插件走的是同一条代码路径。
//
// ctx 需携带与 Register 相同的调用者身份（auth principal），owner 作用域才一致。
// 返回的 stop 关闭隧道流，语义等同插件进程退出（实例随即判离线）。
func (s *RegistryServer) RegisterInProcessTunnel(ctx context.Context, manifest []byte, decoder pb.DecoderServer) (string, func(), error) {
	resp, err := s.Register(ctx, &pb.RegisterRequest{Manifest: manifest})
	if err != nil {
		return "", nil, err
	}
	instanceID := resp.GetInstanceId()

	// 隧道通过 metadata 上报 instance_id（宿主据此精确绑定）；内存流没有真实
	// 传输层，直接把它放进 incoming ctx（与 gRPC 服务端看到的位置一致）。
	p := &inProcessPipe{
		ctx:    metadata.NewIncomingContext(ctx, metadata.Pairs(sdk.TunnelInstanceIDKey, instanceID)),
		hub2p:  make(chan *pb.TunnelFrame, 16),
		p2hub:  make(chan *pb.TunnelFrame, 16),
		closed: make(chan struct{}),
	}

	go func() { _ = s.tunnelHub.Connect(inProcessHubEnd{p}) }()
	go func() { _ = sdk.ServeTunnel(p.ctx, inProcessPluginEnd{p}, decoder) }()

	// 绑定由上面的 goroutine 异步完成，这里同步等到 Client 挂上：调用方拿到
	// instance_id 时插件即可被 Find 到，测试与模拟器不必各自轮询。
	deadline := time.Now().Add(inProcessBindWait)
	for {
		s.mu.RLock()
		rp := s.plugins[instanceID]
		bound := rp != nil && rp.Client != nil
		s.mu.RUnlock()
		if bound {
			break
		}
		if time.Now().After(deadline) {
			p.close()
			return "", nil, fmt.Errorf("in-process tunnel not bound to %q within %s", instanceID, inProcessBindWait)
		}
		time.Sleep(time.Millisecond)
	}

	return instanceID, p.close, nil
}

// inProcessBindWait 是进程内隧道等待绑定的上限（正常是微秒级）。
const inProcessBindWait = 5 * time.Second

// inProcessPipe 是一条内存 Connect 流的两端共享状态。
type inProcessPipe struct {
	ctx    context.Context
	hub2p  chan *pb.TunnelFrame // hub(宿主) → 插件
	p2hub  chan *pb.TunnelFrame // 插件 → hub(宿主)
	closed chan struct{}
	once   sync.Once
}

func (p *inProcessPipe) close() { p.once.Do(func() { close(p.closed) }) }

// inProcessHubEnd 实现 pb.PluginRegistry_ConnectServer：交给 TunnelHub.Connect。
type inProcessHubEnd struct{ p *inProcessPipe }

func (e inProcessHubEnd) Recv() (*pb.TunnelFrame, error) {
	select {
	case f := <-e.p.p2hub:
		return f, nil
	case <-e.p.closed:
		return nil, io.EOF
	case <-e.p.ctx.Done():
		return nil, e.p.ctx.Err()
	}
}

func (e inProcessHubEnd) Send(f *pb.TunnelFrame) error {
	select {
	case e.p.hub2p <- f:
		return nil
	case <-e.p.closed:
		return errors.New("in-process tunnel closed")
	case <-e.p.ctx.Done():
		return e.p.ctx.Err()
	}
}

func (e inProcessHubEnd) Context() context.Context     { return e.p.ctx }
func (e inProcessHubEnd) SendMsg(interface{}) error    { return errors.New("not supported") }
func (e inProcessHubEnd) RecvMsg(interface{}) error    { return errors.New("not supported") }
func (e inProcessHubEnd) SendHeader(metadata.MD) error { return nil }
func (e inProcessHubEnd) SetHeader(metadata.MD) error  { return nil }
func (e inProcessHubEnd) SetTrailer(metadata.MD)       {}

// inProcessPluginEnd 实现 pb.PluginRegistry_ConnectClient：交给 sdk.ServeTunnel。
type inProcessPluginEnd struct{ p *inProcessPipe }

func (e inProcessPluginEnd) Recv() (*pb.TunnelFrame, error) {
	select {
	case f := <-e.p.hub2p:
		return f, nil
	case <-e.p.closed:
		return nil, io.EOF
	case <-e.p.ctx.Done():
		return nil, e.p.ctx.Err()
	}
}

func (e inProcessPluginEnd) Send(f *pb.TunnelFrame) error {
	select {
	case e.p.p2hub <- f:
		return nil
	case <-e.p.closed:
		return errors.New("in-process tunnel closed")
	case <-e.p.ctx.Done():
		return e.p.ctx.Err()
	}
}

func (e inProcessPluginEnd) Context() context.Context          { return e.p.ctx }
func (e inProcessPluginEnd) CloseSend() error                  { return nil }
func (e inProcessPluginEnd) Header() (metadata.MD, error)      { return nil, nil }
func (e inProcessPluginEnd) Trailer() metadata.MD              { return nil }
func (e inProcessPluginEnd) SendMsg(interface{}) error         { return errors.New("not supported") }
func (e inProcessPluginEnd) RecvMsg(interface{}) error         { return errors.New("not supported") }

var (
	_ pb.PluginRegistry_ConnectServer = inProcessHubEnd{}
	_ pb.PluginRegistry_ConnectClient = inProcessPluginEnd{}
)
