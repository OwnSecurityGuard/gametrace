package sdk

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// dialRegistry 将 registry 地址解析为一条 net.Conn。
// 支持 gametrace-pipeline 在两种平台下的端点形式：
//   - Unix socket:   "unix:/abs/workdir/run/registry.sock"
//   - Windows pipe:  "npipe:\\.\pipe\gametrace-registry" 或裸 "\\.\pipe\gametrace-registry"
//
// 注意：gRPC 原生不识别 npipe scheme，必须通过 WithContextDialer 显式拨号
// （与 internalipc.DialGRPC 的做法一致），否则插件在 Windows 上无法注册。
func dialRegistry(ctx context.Context, addr string) (net.Conn, error) {
	switch {
	case strings.HasPrefix(addr, "unix:"):
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(addr, "unix:"))
	case strings.HasPrefix(addr, "npipe:"):
		return dialNpipe(ctx, strings.TrimPrefix(addr, "npipe:"))
	case strings.HasPrefix(addr, `\\.\pipe\`):
		return dialNpipe(ctx, addr)
	case strings.ContainsRune(addr, '/'):
		// 裸路径视为 Unix socket（兼容 "./work/gametrace-registry.sock" 这类写法）
		return (&net.Dialer{}).DialContext(ctx, "unix", addr)
	default:
		// host:port 走 TCP（跨机器部署）
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
}

// RunRegisterLoopWithOptions 是插件 main 的标准入口，支持隧道等可选行为。
//
// 工作流程（默认模式，opts 全零值时与旧版行为完全一致）：
//  1. 创建 Decoder gRPC server，监听 DecodeV2 端点
//     - 默认：本地 Unix socket（同机部署，路径含 PID 避免冲突）
//     - 跨机器：设置 GT_DECODER_ADDR=host:port 监听 TCP；
//     并用 GT_DECODER_PUBLIC_ADDR（缺省回退为该 listen 地址）作为注册时上报的拨号地址
//  2. 确定 registry 端点地址（GT_REGISTRY_ADDR 环境变量 > --registry flag > 默认 :9091）
//     - 未显式设置时回退到 SDK 默认 :9091（见 ResolveRegistryAddr）；显式设置可避免连到非预期地址
//  3. 拨号 registry 并调用 Register RPC（传入上报地址和 plugin.yaml manifest bytes）
//  4. 按返回的 instance_id 和 heartbeat_interval_sec 启动心跳 goroutine
//  5. 心跳断开或 Register 失败时，指数退避重试（首次 1s，上限 30s）
//
// 隧道模式（opts.Tunnel=true）：跳过第 1 步（不监听本地端点，宿主不回拨），
// Register(tunnel=true) 成功后打开 Connect 双向流，DecodeV2 经隧道帧完成。
// opts.AuthToken 非空时，所有 RPC 附带 `authorization: Bearer <token>` metadata。
func RunRegisterLoopWithOptions(decodeFuncV2 DecodeFuncV2, opts RegisterOptions) {
	decoder := &Decoder{decodeFuncV2: decodeFuncV2}

	// 1. 启动 Decoder server，并决定注册时上报给 pipeline 的拨号地址。
	//    隧道模式跳过回拨，无需本地监听。
	var listener net.Listener
	var advertise string
	if !opts.Tunnel {
		var err error
		listener, advertise, err = listenDecoder()
		if err != nil {
			slog.Error("failed to listen on decoder endpoint", "error", err)
			os.Exit(1)
		}
		defer listener.Close()

		grpcServer := grpc.NewServer()
		pb.RegisterDecoderServer(grpcServer, decoder)
		go func() {
			if err := grpcServer.Serve(listener); err != nil && err != io.EOF {
				slog.Error("decoder server error", "error", err)
			}
		}()
		slog.Info("decoder endpoint listening", "advertise", advertise, "local", listener.Addr().String())
	}

	registryAddr := ResolveRegistryAddr()
	if registryAddr == "" {
		slog.Error("GT_REGISTRY_ADDR is required: set it to the registry endpoint printed by gametrace-pipeline (e.g. npipe:\\\\.\\pipe\\gametrace-registry or host:port when -registry-addr is used)")
		os.Exit(1)
	}

	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		conn, err := grpc.NewClient(
			"passthrough:///"+registryAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return dialRegistry(ctx, registryAddr)
			}),
		)
		if err != nil {
			slog.Warn("register: dial registry failed, retrying", "addr", registryAddr, "error", err, "backoff", backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		manifestBytes, err := ReadManifest()
		if err != nil {
			slog.Warn("register: read manifest failed, retrying", "error", err, "backoff", backoff)
			_ = conn.Close()
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		callCtx := context.Background()
		if opts.AuthToken != "" {
			callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+opts.AuthToken)
		}

		client := pb.NewPluginRegistryClient(conn)
		regReq := &pb.RegisterRequest{
			SocketPath: advertise,
			Manifest:   manifestBytes,
			Tunnel:     opts.Tunnel,
		}
		regResp, err := client.Register(callCtx, regReq)
		if err != nil {
			slog.Warn("register: Register RPC failed, retrying", "error", err, "backoff", backoff)
			_ = conn.Close()
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		instanceID := regResp.GetInstanceId()
		heartbeatSec := regResp.GetHeartbeatIntervalSec()
		if heartbeatSec <= 0 {
			heartbeatSec = 10
		}

		slog.Info("registered successfully", "instance_id", instanceID, "heartbeat_sec", heartbeatSec, "tunnel", opts.Tunnel)
		backoff = time.Second

		if opts.Tunnel {
			// 隧道模式：在同一条连接上打开 Connect 双向流并服务解码请求，
			// 流断开即视为失联，走与心跳断开一致的退避重连路径。
			connectStream, err := client.Connect(callCtx)
			if err != nil {
				slog.Warn("tunnel: open connect stream failed, retrying", "error", err, "backoff", backoff)
				_ = conn.Close()
				time.Sleep(backoff)
				backoff = min(backoff*2, maxBackoff)
				continue
			}
			tunnelErr := runTunnel(callCtx, connectStream, decoder)
			_ = conn.Close()
			slog.Warn("tunnel closed, reconnecting", "instance_id", instanceID, "error", tunnelErr)
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		heartbeatLost := make(chan struct{})
		go heartbeatLoop(callCtx, client, instanceID, time.Duration(heartbeatSec)*time.Second, heartbeatLost)

		// 保持注册直到心跳失联（平台侧重启/升级/网络中断）。心跳失败即回到
		// 循环顶部重新注册，获得新的 instance_id，插件进程无需重启；
		// 与隧道模式「流断开即重连」的语义保持一致。
		<-heartbeatLost
		_ = conn.Close()
		slog.Warn("registry heartbeat lost, reconnecting", "instance_id", instanceID)
	}
}

// heartbeatTimeout 是单次心跳 RPC 的超时。registry 进程被强杀或网络静默丢包时
// 底层 TCP 连接不会立刻报错，若不带超时，心跳会无限阻塞，主循环永远收不到
// 「失联」通知而无法重连。取一个明显小于常见心跳间隔的保守值即可。
const heartbeatTimeout = 10 * time.Second

// heartbeatLoop 定期向 registry 发送心跳。心跳失败（平台侧重启/失联）时
// close(lost) 通知主循环触发重连，避免插件进程被平台侧重启拖死、必须人工重启。
// ctx 应携带认证 metadata（如有），context 取消时安静退出。
func heartbeatLoop(ctx context.Context, client pb.PluginRegistryClient, instanceID string, interval time.Duration, lost chan<- struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hbCtx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
			_, err := client.Heartbeat(hbCtx, &pb.HeartbeatRequest{InstanceId: instanceID})
			cancel()
			if err != nil {
				slog.Warn("heartbeat failed", "instance_id", instanceID, "error", err)
				close(lost)
				return
			}
		}
	}
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// listenDecoder 启动插件的 Decoder gRPC server，并返回 (listener, advertiseAddr, error)。
//
// 监听模式：
//   - 设置了 GT_DECODER_ADDR：在 host:port（或 unix:path）上监听 TCP/Unix。
//   - 未设置：默认监听 TCP :0（随机端口），兼容 Windows / 跨机器。
//
// 上报地址（注册时回填给 pipeline 的拨号地址）优先级：
//  1. GT_DECODER_PUBLIC_ADDR：显式指定，原样上报。平台侧部署在 Docker、插件运行在
//     宿主机的场景下，把这里填成平台侧（容器）能拨号到的地址即可，例如宿主局域网 IP、
//     host.docker.internal 或 Docker 网桥网关。
//  2. listener 实际地址，但 host 部分是通配符（0.0.0.0 / :: / 空）时自动替换为本机
//     首个非回环 IPv4 —— 通配符地址对平台侧（尤其是 Docker 容器内）不可拨号，
//     默认 `:0` 与 `0.0.0.0:port` 两种常见用法因此都能上报可拨号地址。
//  3. 显式绑定到具体 IP（如 192.168.1.10:port）时原样上报，尊重用户意图。
func listenDecoder() (net.Listener, string, error) {
	bind := os.Getenv("GT_DECODER_ADDR")
	pub := os.Getenv("GT_DECODER_PUBLIC_ADDR")

	if bind == "" {
		lis, err := net.Listen("tcp", ":0")
		if err != nil {
			return nil, "", err
		}
		return lis, advertiseTCP(pub, lis.Addr().String()), nil
	}

	if strings.HasPrefix(bind, "unix:") {
		path := strings.TrimPrefix(bind, "unix:")
		_ = os.RemoveAll(path)
		lis, err := net.Listen("unix", path)
		if err != nil {
			return nil, "", err
		}
		adv := pub
		if adv == "" {
			adv = "unix:" + path
		}
		return lis, adv, nil
	}

	lis, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, "", err
	}
	return lis, advertiseTCP(pub, lis.Addr().String()), nil
}

// advertiseTCP 计算注册时上报给 pipeline 的 TCP 拨号地址。
// pub 非空时原样返回（最高优先级，跨机器 / Docker 部署时手动指定）。
// 否则取 actual（listener 实际地址），host 为通配符（空 / 0.0.0.0 / ::）时
// 替换为本机首个非回环 IPv4；仍拿不到具体 IP 时保留原地址并告警。
func advertiseTCP(pub, actual string) string {
	if pub != "" {
		return pub
	}
	host, port, err := net.SplitHostPort(actual)
	if err != nil {
		return actual
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		if ip := hostIPv4(); ip != "" {
			host = ip
		} else {
			slog.Warn("no non-loopback IPv4 found; advertised address keeps the wildcard host and may not be dialable by gametrace-pipeline (e.g. when it runs inside Docker); set GT_DECODER_PUBLIC_ADDR to the reachable host:port", "addr", actual)
		}
	}
	return net.JoinHostPort(host, port)
}

// hostIPv4 返回本机首个非回环 IPv4 地址，用于把通配符监听地址转换为平台侧
// （可能部署在 Docker 中）可以拨号回连的地址；找不到时返回空串。
// 多网卡 / VPN 环境下首个地址未必是目标网段，此时请显式设置 GT_DECODER_PUBLIC_ADDR。
func hostIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || ip.IsLoopback() {
			continue
		}
		return ip.String()
	}
	return ""
}
