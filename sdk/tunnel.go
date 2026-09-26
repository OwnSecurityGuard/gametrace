package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// tunnelReqQueueSize 是每个逻辑 DecodeV2 流的请求队列容量。
// 只用于吸收正常抖动：队列满即 reset 该流，绝不为慢消费者等待
// （等待会阻塞 runTunnel 的收包主循环，见 handleRequest）。
const tunnelReqQueueSize = 64

// RegisterOptions 是注册行为的可选项；零值表示匿名注册（平台未开启鉴权时的用法）。
type RegisterOptions struct {
	// AuthToken 非空时，注册/心跳/隧道请求附带
	// `authorization: Bearer <token>` gRPC metadata。
	AuthToken string
}

// RunRegisterLoop 是插件 main 的标准入口（匿名注册）。
func RunRegisterLoop(decodeFuncV2 DecodeFuncV2) {
	RunRegisterLoopWithOptions(decodeFuncV2, RegisterOptions{})
}

// tunnelStream 是隧道模式下传给 Decoder.DecodeV2 的「假 server stream」：
// Recv 从该逻辑流的请求队列读帧还原出的 DecodeRequest，
// Send 把 DecodeResponseV2 编回隧道帧写给宿主。
//
// reqCh 永不关闭（避免 enqueue 与 close 竞争导致 send on closed channel
// panic）；逻辑流关闭以 done 通道 + closeErr 表示。
type tunnelStream struct {
	ctx      context.Context
	cancel   context.CancelFunc
	reqCh    chan *pb.DecodeRequest
	done     chan struct{} // close() 时关闭
	closeErr error         // 关闭原因（reset/帧损坏时非空）
	streamID uint32
	sendFn   func(*pb.DecodeResponseV2) error

	closeOnce sync.Once
	closeMu   sync.Mutex // 保护 closeErr 的读取
}

func (s *tunnelStream) close(err error) {
	s.closeOnce.Do(func() {
		s.closeMu.Lock()
		if err != nil {
			s.closeErr = err
		}
		s.closeMu.Unlock()
		close(s.done)
		s.cancel()
	})
}

func (s *tunnelStream) recvErr() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

func (s *tunnelStream) Recv() (*pb.DecodeRequest, error) {
	// reset/帧损坏：立即报错，不排空残余请求
	select {
	case <-s.done:
		if err := s.recvErr(); err != nil {
			return nil, err
		}
	default:
	}

	select {
	case req := <-s.reqCh:
		return req, nil
	case <-s.done:
		// reset/帧损坏：优先报错；half_close：先排空在其之前入队的请求
		//（对齐 gRPC 缓冲语义），再 EOF
		if err := s.recvErr(); err != nil {
			return nil, err
		}
		select {
		case req := <-s.reqCh:
			return req, nil
		default:
			return nil, io.EOF
		}
	case <-s.ctx.Done():
		if err := s.recvErr(); err != nil {
			return nil, err
		}
		return nil, s.ctx.Err()
	}
}

func (s *tunnelStream) Send(resp *pb.DecodeResponseV2) error {
	if resp == nil {
		return errors.New("tunnel: Send(nil) is not allowed")
	}
	return s.sendFn(resp)
}

func (s *tunnelStream) Context() context.Context { return s.ctx }
func (s *tunnelStream) SendMsg(m interface{}) error {
	if resp, ok := m.(*pb.DecodeResponseV2); ok {
		return s.Send(resp)
	}
	return fmt.Errorf("tunnel: SendMsg unsupported type %T", m)
}
func (s *tunnelStream) RecvMsg(interface{}) error {
	return errors.New("tunnel: RecvMsg not supported, use Recv")
}
func (s *tunnelStream) SendHeader(metadata.MD) error { return nil }
func (s *tunnelStream) SetHeader(metadata.MD) error  { return nil }
func (s *tunnelStream) SetTrailer(metadata.MD)       {}

// tunnelMux 把一条 Connect 双向流多路分解为若干逻辑 DecodeV2 流。
type tunnelMux struct {
	decoder  pb.DecoderServer
	stream   pb.PluginRegistry_ConnectClient
	sendMu   sync.Mutex // 保护 stream.Send 的并发访问
	mu       sync.Mutex
	streams  map[uint32]*tunnelStream
	wg       sync.WaitGroup
	closed   bool
	closeAll context.CancelFunc
}

// ServeTunnel 在一条 Connect 流上服务解码请求，直到流断开或 ctx 取消。
//
// 通常由 RunRegisterLoopWithOptions 调用（流来自真实 registry 连接）。导出版本
// 供宿主侧的同进程用途使用：模拟器 / 集成测试可把任意 pb.DecoderServer 挂到
// 一条内存 Connect 流上，无需真实网络端点。
func ServeTunnel(ctx context.Context, stream pb.PluginRegistry_ConnectClient, decoder pb.DecoderServer) error {
	ctx, cancel := context.WithCancel(ctx)
	m := &tunnelMux{
		decoder:  decoder,
		stream:   stream,
		streams:  make(map[uint32]*tunnelStream),
		closeAll: cancel,
	}
	defer func() {
		m.mu.Lock()
		m.closed = true
		for _, ts := range m.streams {
			ts.close(nil)
		}
		m.mu.Unlock()
		cancel()
		m.wg.Wait()
	}()

	for {
		frame, err := stream.Recv()
		if err != nil {
			slog.Debug("tunnel: connect stream closed", "error", err)
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch p := frame.Payload.(type) {
		case *pb.TunnelFrame_Request:
			m.handleRequest(ctx, frame.GetStreamId(), p.Request)
		case *tunnelHalfClose:
			m.closeRecv(frame.GetStreamId(), nil)
		case *pb.TunnelFrame_Reset_:
			reason := p.Reset_.GetReason()
			m.closeRecv(frame.GetStreamId(), &tunnelResetError{reason: reason})
		default:
			slog.Warn("tunnel: ignoring frame without payload", "stream_id", frame.GetStreamId())
		}
	}
}

// tunnelHalfClose 仅用于 switch 匹配（HalfClose payload 的字段类型是 bool）。
type tunnelHalfClose = pb.TunnelFrame_HalfClose

type tunnelResetError struct{ reason string }

func (e *tunnelResetError) Error() string { return "stream reset: " + e.reason }

// handleRequest 处理 request 帧：首个帧隐式开流并启动 DecodeV2 服务协程，
// 后续帧入队。
func (m *tunnelMux) handleRequest(ctx context.Context, streamID uint32, data []byte) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	ts, ok := m.streams[streamID]
	if !ok {
		tctx, tcancel := context.WithCancel(ctx)
		ts = &tunnelStream{
			ctx:      tctx,
			cancel:   tcancel,
			reqCh:    make(chan *pb.DecodeRequest, tunnelReqQueueSize),
			done:     make(chan struct{}),
			streamID: streamID,
			sendFn:   func(resp *pb.DecodeResponseV2) error { return m.sendResponse(streamID, resp) },
		}
		m.streams[streamID] = ts
		m.wg.Add(1)
		go m.serveStream(ts)
	}
	m.mu.Unlock()

	req := &pb.DecodeRequest{}
	if err := proto.Unmarshal(data, req); err != nil {
		// 帧损坏：按 reset 处理该逻辑流
		slog.Warn("tunnel: bad request frame", "stream_id", streamID, "error", err)
		m.closeRecv(streamID, fmt.Errorf("bad request frame: %w", err))
		return
	}
	// ③ 单逻辑流隔离：入队**绝不能阻塞**——本函数在 runTunnel 唯一的收包
	// 循环里执行，为一个消费慢的流等待会让同隧道所有其他流的帧一起卡住
	// （队头阻塞）。入不进去就只 reset 这一个逻辑流，代价由它自己承担。
	select {
	case ts.reqCh <- req:
		return
	case <-ts.done:
		return
	default:
	}
	slog.Warn("tunnel: request queue overflow, resetting stream", "stream_id", streamID, "queue", cap(ts.reqCh))
	m.closeRecv(streamID, fmt.Errorf("tunnel: request queue overflow"))
}

// serveStream 驱动一个逻辑流：直接复用 Decoder.DecodeV2（用户代码零改动），
// 结束后发送 end 帧给宿主，并把该逻辑流从 mux 中移除（允许 stream_id 复用）。
func (m *tunnelMux) serveStream(ts *tunnelStream) {
	defer m.wg.Done()
	err := m.decoder.DecodeV2(ts)
	m.sendEnd(ts.streamID, err)

	m.mu.Lock()
	if cur, ok := m.streams[ts.streamID]; ok && cur == ts {
		delete(m.streams, ts.streamID)
	}
	m.mu.Unlock()
}

// closeRecv 关闭一个逻辑流的请求侧（half_close / reset / 帧损坏共用）。
func (m *tunnelMux) closeRecv(streamID uint32, err error) {
	m.mu.Lock()
	ts, ok := m.streams[streamID]
	m.mu.Unlock()
	if ok {
		ts.close(err)
	}
}

func (m *tunnelMux) sendFrame(frame *pb.TunnelFrame) error {
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	if m.stream.Context().Err() != nil {
		return m.stream.Context().Err()
	}
	return m.stream.Send(frame)
}

func (m *tunnelMux) sendResponse(streamID uint32, resp *pb.DecodeResponseV2) error {
	data, err := proto.Marshal(resp)
	if err != nil {
		return err
	}
	return m.sendFrame(&pb.TunnelFrame{
		StreamId: streamID,
		Payload:  &pb.TunnelFrame_Response{Response: data},
	})
}

func (m *tunnelMux) sendEnd(streamID uint32, decodeErr error) {
	end := &pb.StreamEnd{}
	if decodeErr != nil && !errors.Is(decodeErr, io.EOF) && !errors.Is(decodeErr, context.Canceled) {
		end.Error = decodeErr.Error()
	}
	_ = m.sendFrame(&pb.TunnelFrame{
		StreamId: streamID,
		Payload:  &pb.TunnelFrame_End{End: end},
	})
}
