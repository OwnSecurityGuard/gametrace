package sdk

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// registry 连接的 gRPC keepalive 参数（②）。TCP 半开（对端消失但无 RST/FIN）
// 时连接不会报错，周期性 PING 是传输层唯一的死连接发现手段。
// PermitWithoutStream=true 是关键：隧道空闲（无解码请求）时也必须探测。
// Time 必须 >= 宿主 EnforcementPolicy.MinTime，否则宿主会回 RST 断流。
const (
	registryKeepaliveTime    = 30 * time.Second
	registryKeepaliveTimeout = 10 * time.Second
)

// TunnelInstanceIDKey 是 Connect 流用来携带 instance_id 的 gRPC metadata 键。
//
// Register 与 Connect 是两个独立 RPC，宿主无法从连接本身判断某条隧道属于
// 哪次注册。插件在 Connect 时把 Register 返回的 instance_id 放进该 header，
// 宿主据此精确绑定；缺失该 header 时宿主拒绝建流（SDK 会退避重连），
// 不再按到达顺序猜测配对。
const TunnelInstanceIDKey = "x-gt-instance-id"

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

// RunRegisterLoopWithOptions 是插件 main 的标准入口。
//
// 插件一律经反向隧道接入：不起本地监听端点，宿主也不回拨插件，注册 / 心跳 /
// 解码帧全部复用同一条到 registry 的连接——因此插件在 NAT、容器、手机后面都能
// 直接接入，不需要任何入站端口。
//
// 工作流程：
//  1. 确定 registry 端点（GT_REGISTRY_ADDR 环境变量 > --registry flag > 默认 :9091）
//  2. 读取插件目录下的 plugin.yaml → manifest bytes
//  3. 调 Register RPC 拿到 instance_id 与心跳间隔
//  4. 在同一条连接上打开 Connect 双向流：instance_id 经 metadata
//     （TunnelInstanceIDKey）上报，宿主据此精确绑定本次注册
//  5. 心跳与隧道服务并发运行：任一失败（心跳失联 / 隧道断开）都退避重连并
//     重新 Register（新的 instance_id），进程无需重启
//
// 心跳不能用隧道流代替：TCP 半开时 Connect 流的 Recv 不会报错，只有带超时的
// 应用层心跳能发现宿主已经消失。
// opts.AuthToken 非空时，所有 RPC 附带 `authorization: Bearer <token>` metadata。
func RunRegisterLoopWithOptions(decodeFuncV2 DecodeFuncV2, opts RegisterOptions) {
	decoder := &Decoder{decodeFuncV2: decodeFuncV2}

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
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:    registryKeepaliveTime,
				Timeout: registryKeepaliveTimeout,
				// 隧道空闲（无解码请求）时也要探测，否则死连接只能等到
				// 下一次有流量才被发现——正是半开连接最危险的场景。
				PermitWithoutStream: true,
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
		regResp, err := client.Register(callCtx, &pb.RegisterRequest{Manifest: manifestBytes})
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

		slog.Info("registered successfully", "instance_id", instanceID, "heartbeat_sec", heartbeatSec)
		backoff = time.Second

		// 隧道：在同一条连接上打开 Connect 双向流并服务解码请求。
		// instance_id 通过 metadata 上报，宿主据此精确绑定本次注册。
		connectStream, err := client.Connect(
			metadata.AppendToOutgoingContext(callCtx, TunnelInstanceIDKey, instanceID))
		if err != nil {
			slog.Warn("tunnel: open connect stream failed, retrying", "error", err, "backoff", backoff)
			_ = conn.Close()
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		// 隧道期间心跳照发：TCP 半开（对端消失但无 RST/FIN）时 Connect 流的
		// Recv 不会报错，只有带超时的应用层心跳能发现对端已消失。
		// 隧道断 / 心跳失联任一发生都走同一条退避重连路径。
		hbCtx, hbCancel := context.WithCancel(callCtx)
		heartbeatLost := make(chan struct{})
		go heartbeatLoop(hbCtx, client, instanceID, time.Duration(heartbeatSec)*time.Second, heartbeatLost)

		tunnelDone := make(chan error, 1)
		go func() { tunnelDone <- ServeTunnel(hbCtx, connectStream, decoder) }()

		var tunnelErr error
		tunnelReturned := false
		select {
		case tunnelErr = <-tunnelDone:
			tunnelReturned = true
			slog.Warn("tunnel closed, reconnecting", "instance_id", instanceID, "error", tunnelErr)
		case <-heartbeatLost:
			slog.Warn("tunnel heartbeat lost, reconnecting", "instance_id", instanceID)
		}
		// 收尾：取消心跳并关闭连接。心跳失联时流的 Recv 不会自己报错，
		// 必须关连接才能让 runTunnel 返回（否则 goroutine 泄漏）。
		hbCancel()
		_ = conn.Close()
		if !tunnelReturned {
			<-tunnelDone
		}
		<-heartbeatLost // 已关闭的 channel：立即返回，确认心跳 goroutine 已退出
		time.Sleep(backoff)
		backoff = min(backoff*2, maxBackoff)
	}
}

// heartbeatTimeout 是单次心跳 RPC 的超时。registry 进程被强杀或网络静默丢包时
// 底层 TCP 连接不会立刻报错，若不带超时，心跳会无限阻塞，主循环永远收不到
// 「失联」通知而无法重连。取一个明显小于常见心跳间隔的保守值即可。
const heartbeatTimeout = 10 * time.Second

// heartbeatLoop 定期向 registry 发送心跳。心跳失败（平台侧重启/失联）时
// close(lost) 通知主循环触发重连，避免插件进程被平台侧重启拖死、必须人工重启。
// ctx 应携带认证 metadata（如有），context 取消时安静退出。
//
// lost 在**所有**退出路径上都被关闭（含 ctx 取消），是单一出口约定：
// 隧道分支在收尾时同步等待 <-heartbeatLost 以确认 goroutine 退出，
// 若取消路径不关闭该 channel 会永久阻塞。
func heartbeatLoop(ctx context.Context, client pb.PluginRegistryClient, instanceID string, interval time.Duration, lost chan<- struct{}) {
	defer close(lost)
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

