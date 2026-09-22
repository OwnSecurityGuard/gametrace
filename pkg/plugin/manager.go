package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gametrace/pkg/auth"
	sdkcontract "github.com/OwnSecurityGuard/gametrace/sdk/contract"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"gopkg.in/yaml.v3"
)

// RegisteredPlugin 表示一个已注册的插件实例。
type RegisteredPlugin struct {
	InstanceID    string
	SocketPath    string
	Manifest      *Manifest
	Client        pb.DecoderClient
	Conn          *grpc.ClientConn
	LastHeartbeat time.Time
	Online        atomic.Bool // true 表示最近有心跳，false 表示超时未心跳
	// Owner 是注册方的属主标识（来自 gRPC auth 上下文，auth.OwnerFrom）。
	// 空串表示无主/匿名（本地单机用法），注册键退化为裸 manifest name，
	// 与改造前的行为完全一致。
	Owner string
	// Tunnel 表示该插件经 Connect 反向隧道接入：没有可回拨的 socket，
	// 存活与否跟随 Connect 流（不参与 CheckOffline 心跳超时判定）。
	Tunnel bool
}

// PluginSummary 是插件注册信息的摘要，用于对外暴露。
type PluginSummary struct {
	InstanceID string
	Name       string
	Protocol   string
	Type       string
	// Transports 是插件声明的 L4 传输层能力（tcp|udp）；空表示未声明。
	Transports    []string
	APIVersion    string
	SocketPath    string
	Online        bool
	LastHeartbeat time.Time
	// Owner 是注册方属主（空串 = 匿名/本地）。T13 的 MCP listing 需要。
	Owner string
	// Tunnel 表示该插件经反向隧道接入（存活跟随 Connect 流）。
	Tunnel bool
}

// PluginEventType 表示插件注册表状态变化的类型。
type PluginEventType string

const (
	// PluginEventRegister 新插件注册（进程启动并成功注册）。
	PluginEventRegister PluginEventType = "register"
	// PluginEventDeregister 插件主动注销（进程退出时调用 Deregister）。
	PluginEventDeregister PluginEventType = "deregister"
	// PluginEventOnline 插件由离线恢复在线（心跳恢复，离线→在线翻转）。
	PluginEventOnline PluginEventType = "online"
	// PluginEventOffline 插件心跳超时被判离线（在线→离线翻转）。
	PluginEventOffline PluginEventType = "offline"
	// PluginEventRegisterFailed 注册失败（平台拨号插件 Decode 地址不通等）。
	// 失败的注册不进注册表；事件携带 SocketPath/Error/Owner 诊断字段。
	PluginEventRegisterFailed PluginEventType = "register_failed"
)

// PluginEvent 是插件注册表状态变化的通知，用于即时推送（避免轮询）。
type PluginEvent struct {
	Type       PluginEventType
	InstanceID string
	Name       string
	Online     bool
	Timestamp  time.Time
	// 以下为 register_failed 专用字段（其余事件为零值）：
	// SocketPath 注册时上报的插件地址；Error 拨号错误与诊断建议；
	// Owner 注册方属主（SSE 订阅侧按此过滤，匿名为空串）。
	SocketPath string
	Error      string
	Owner      string
}

// RegisterFailure 记录一次注册失败的诊断信息（dial 插件地址失败）。
// ring buffer 容量 20、条目 TTL 15 分钟；name+socket_path+error 同 key
// 5 分钟内去重（SDK 以 1→30s 退避无限重试，不去重会刷屏）。
type RegisterFailure struct {
	Name       string
	SocketPath string
	Error      string
	Owner      string
	Timestamp  time.Time
}

// maxRecentFailures 是注册失败 ring buffer 的容量。
const maxRecentFailures = 20

// 测试可覆盖的时间参数。
var (
	failureTTL         = 15 * time.Minute
	failureDedupWindow = 5 * time.Minute
	// registerProbeTimeout 是 Register 阶段对插件 Decode 地址做可达性探测的
	// 超时（错误远端地址/被防火墙丢弃的 SYN 不应长时间挂住注册 RPC）。
	registerProbeTimeout = 5 * time.Second
)

// RegistryServer 实现 PluginRegistry gRPC 服务，被动接受插件注册。
// 插件进程由外部编排（systemd/脚本）独立启动，RegistryServer 不再 spawn 子进程，
// 也不再维护 watcher/restart 逻辑。
type RegistryServer struct {
	pb.UnimplementedPluginRegistryServer
	mu           sync.RWMutex
	closed       bool                         // Close() 置位；置位后 Register/隧道钩子拒绝新工作
	plugins      map[string]*RegisteredPlugin // key: instance_id
	byName       map[string]string            // key: pluginKey(owner, name) → instance_id
	nextID       atomic.Int64
	heartbeatSec int32

	// tunnelHub 处理 PluginRegistry.Connect 反向隧道流（见 tunnel.go）。
	// NewRegistryServer 时随注册表一起创建，Connect RPC 委托给它；
	// 隧道建流/断开钩子回调 bindTunnelClient / tunnelDisconnected 完成绑定与下线。
	// 绑定按 Connect metadata 里的 instance_id 精确匹配，注册表不再保存
	// 任何「等待配对」的 FIFO 状态。
	tunnelHub *TunnelHub

	// 注册失败 ring buffer（register_failed 事件源数据）：容量 20、TTL 15min、
	// name+socket_path+error 同 key 5min 去重。
	failMu       sync.Mutex
	failures     []RegisterFailure
	failLastSeen map[string]time.Time

	// 事件总线：插件注册/注销/上下线时向订阅者推送 PluginEvent。
	// 订阅者通道带缓冲，emit 非阻塞（订阅者处理不过来则丢弃，避免阻塞注册表主路径）。
	listenerMu  sync.Mutex
	listenerSeq int64
	listeners   map[int64]chan PluginEvent
}

// NewRegistryServer 创建注册服务器。heartbeatSec 是要求插件的心跳间隔，
// 非正数时默认 10 秒。
func NewRegistryServer(heartbeatSec int32) *RegistryServer {
	if heartbeatSec <= 0 {
		heartbeatSec = 10
	}
	s := &RegistryServer{
		plugins:      map[string]*RegisteredPlugin{},
		byName:       map[string]string{},
		heartbeatSec: heartbeatSec,
		listeners:    map[int64]chan PluginEvent{},
		failLastSeen: map[string]time.Time{},
	}
	// Connect 反向隧道：owner 从流上下文解析（gRPC auth 拦截器注入 Principal；
	// 未接入认证时 OwnerFrom 返回 ""，即匿名/本地语义）。
	s.tunnelHub = NewTunnelHub(
		WithTunnelOwnerResolver(auth.OwnerFrom),
		WithTunnelConnectHook(s.bindTunnelClient),
		WithTunnelDisconnectHook(s.tunnelDisconnected),
	)
	return s
}

// pluginKey 生成注册表 byName 的内部键。
// owner 为空（匿名/本地单机）时键就是裸 manifest name，行为与改造前一致；
// 非空 owner 的键为 "owner/name"，使不同属主的同名插件互不覆盖、各自路由。
// 注意：owner 内含 "/" 会使键产生歧义——owner 由认证体系（token→Principal）
// 生成、不来自用户自由输入，约定 owner 不得包含 "/"；manifest name 同理。
func pluginKey(owner, name string) string {
	if owner == "" {
		return name
	}
	return owner + "/" + name
}

// pluginKeyCandidates 返回按优先级排列的候选 byName 键（纯函数，不触碰共享状态）。
// name 已含 "/" 时唯一候选为完整键；否则先 owner 作用域键、再退化裸 name
// （使有主调用方仍能看到匿名注册的本地/系统插件）。
// 单 owner（或匿名）语境下候选序列与改造前的 FindByName 语义一致。
// 候选键的最终校验（存在性 + 属主隔离）在持锁的查找路径内完成。
func pluginKeyCandidates(owner, name string) []string {
	if strings.Contains(name, "/") || owner == "" {
		return []string{name}
	}
	return []string{pluginKey(owner, name), name}
}

// Subscribe 订阅插件注册表状态变化事件。
// 返回只读事件通道与退订函数；退订后通道会被关闭。
func (s *RegistryServer) Subscribe() (<-chan PluginEvent, func()) {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	id := s.listenerSeq
	s.listenerSeq++
	ch := make(chan PluginEvent, 16)
	s.listeners[id] = ch
	unsub := func() {
		s.listenerMu.Lock()
		defer s.listenerMu.Unlock()
		if c, ok := s.listeners[id]; ok {
			delete(s.listeners, id)
			close(c)
		}
	}
	return ch, unsub
}

// emit 向所有订阅者非阻塞推送事件。
func (s *RegistryServer) emit(event PluginEvent) {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	for _, ch := range s.listeners {
		select {
		case ch <- event:
		default:
			// 订阅者消费不及时则丢弃，避免阻塞注册表主路径
		}
	}
}

// Register 处理插件注册请求。
// 流程：解析 manifest → 校验 manifest → 版本协商 → （非隧道时）拨号插件 Decode
// socket 验证可达 → 分配 instance_id。
//
// 属主：owner 取自 gRPC auth 上下文（auth.OwnerFrom），注册键为 pluginKey(owner, name)；
// 同一 owner 的同名重复注册替换旧实例（崩溃重启场景），不同 owner 的同名插件共存。
//
// 隧道分支：req.Tunnel == true 时插件没有可回拨的 Decode socket，跳过拨号验证；
// 解码用 pb.DecoderClient 由 Connect 反向隧道提供（见 tunnel.go）。绑定方式（④）：
// 本 RPC 返回 instance_id，插件随后带着它打开 Connect（metadata
// sdk.TunnelInstanceIDKey），宿主按 id 精确绑定——不做任何到达顺序推断。
// 绑定前实例 Online=false、不参与 Find/FindByName（Client 为 nil 不可用）。
//
// 存活：隧道实例同样受心跳超时约束（CheckOffline）。断开的隧道由断开钩子
// 立即下线；心跳只是兜底——半开连接下 Connect 流不会报错，只有心跳超时能
// 发现插件已消失（①）。
func (s *RegistryServer) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	// 1. 解析 manifest
	m, err := ParseManifest(req.Manifest)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	// 2. 校验 manifest
	if err := ValidateManifest(m); err != nil {
		return nil, fmt.Errorf("validate manifest: %w", err)
	}
	// 3. 版本协商
	if err := CheckManifestVersion(m); err != nil {
		return nil, fmt.Errorf("version check: %w", err)
	}
	// 3.5 语义契约声明期校验（Semantic Contract v1 两层：schema/state）。
	//     error 级违规拒绝注册；warn 级放行但记日志，让插件作者能在 plugin.verify 看到全量报告。
	if report := sdkcontract.NewPluginChecker().Check(m); report != nil {
		if report.HasErrors() {
			return nil, fmt.Errorf("semantic contract check failed: %s", formatReport(report))
		}
		for _, v := range report.Violations {
			slog.Warn("plugin manifest semantic warn", "name", m.Name, "rule", v.RuleID, "detail", v.Message)
		}
	}

	owner := auth.OwnerFrom(ctx)
	tunnel := req.GetTunnel()

	// 4. 拨号到插件的 Decode socket 验证可达（隧道模式跳过：存活跟随 Connect 流）
	//    SocketPath 可能是 host:port（跨机器部署）、unix:/path 或 npipe:\\.\pipe\name。
	var conn *grpc.ClientConn
	var client pb.DecoderClient
	if !tunnel {
		// 真实可达性探测：grpc.NewClient 是懒连接，创建时不会报错——不主动
		// 探测的话，错误的 GT_DECODER_PUBLIC_ADDR 要到首次解码 RPC 才暴露。
		// SDK 保证 Decode server 先于 Register 启动（sdk/registry.go），
		// 正确配置的插件不受影响；探测失败则拒绝注册（SDK 退避重试自愈）。
		pctx, pcancel := context.WithTimeout(ctx, registerProbeTimeout)
		probe, perr := dialTarget(pctx, req.SocketPath)
		pcancel()
		if perr != nil {
			// 诊断建议随错误返回给 SDK（插件控制台可见），同时入账 ring buffer
			// 并发 register_failed 事件（前端 toast / 插件面板 / status_plugin）。
			hint := dialFailureHint(ctx, req.SocketPath, perr)
			s.recordFailure(m, req.SocketPath, hint, owner)
			return nil, fmt.Errorf("dial plugin socket: %w (%s)", perr, hint)
		}
		_ = probe.Close()
		conn, err = dialDecoder(ctx, req.SocketPath)
		if err != nil {
			return nil, fmt.Errorf("dial plugin socket: %w", err)
		}
		client = pb.NewDecoderClient(conn)
	}

	// 5. 分配 instance_id
	instanceID := fmt.Sprintf("%s-%d", m.Name, s.nextID.Add(1))

	rp := &RegisteredPlugin{
		InstanceID:    instanceID,
		SocketPath:    req.SocketPath,
		Manifest:      m,
		Client:        client,
		Conn:          conn,
		LastHeartbeat: time.Now(),
		Owner:         owner,
		Tunnel:        tunnel,
	}
	rp.Online.Store(!tunnel) // 隧道实例等 Connect 绑定后才算在线

	nameKey := pluginKey(owner, m.Name)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("registry is closed")
	}
	// 若同名插件已注册（同 owner 作用域内），删除旧实例（崩溃后重启场景）。
	// 旧实例的隧道若仍存活，其断开钩子因 plugins 里已无该 instance_id 而为
	// no-op —— 不会误伤接替它的新实例（④ 精确绑定的直接收益）。
	if oldID, ok := s.byName[nameKey]; ok {
		if old, ok := s.plugins[oldID]; ok {
			if old.Conn != nil {
				_ = old.Conn.Close()
			}
			delete(s.plugins, oldID)
		}
	}
	s.plugins[instanceID] = rp
	s.byName[nameKey] = instanceID
	// 隧道实例此刻尚未绑定：等插件用本返回的 instance_id 打开 Connect（见
	// tunnel.go 的 bindTunnelClient），由它推 online 事件。
	s.mu.Unlock()

	slog.Info("plugin registered", "name", m.Name, "instance_id", instanceID, "protocol", m.Protocol,
		"owner", owner, "tunnel", tunnel)

	s.emit(PluginEvent{
		Type:       PluginEventRegister,
		InstanceID: instanceID,
		Name:       m.Name,
		Online:     rp.Online.Load(),
		Timestamp:  time.Now(),
	})

	return &pb.RegisterResponse{
		InstanceId:           instanceID,
		HeartbeatIntervalSec: s.heartbeatSec,
	}, nil
}

// dialFailureHint 生成拨号失败的诊断建议：底层错误 + 注册连接来源 IP 建议值 +
// 回环地址场景的 Docker 提示。仅作建议文本，不做自动纠正（证据驱动原则）。
func dialFailureHint(ctx context.Context, socketPath string, dialErr error) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%v", dialErr)
	if src := peerSourceIP(ctx); src != "" {
		if port := socketPort(socketPath); port != "" {
			fmt.Fprintf(&sb, "; 注册连接来源 IP %s，可尝试 GT_DECODER_PUBLIC_ADDR=%s:%s", src, src, port)
		} else {
			fmt.Fprintf(&sb, "; 注册连接来源 IP %s", src)
		}
	}
	if host, _, err := net.SplitHostPort(socketPath); err == nil &&
		(host == "127.0.0.1" || host == "localhost" || host == "::1") {
		sb.WriteString("; GT_DECODER_PUBLIC_ADDR/GT_DECODER_ADDR 指向回环地址：平台（尤其 Docker 部署）回拨的是平台自身，请改填平台可回拨的宿主机地址（如 GT_PUBLIC_HOST 或宿主机局域网 IP）")
	}
	return sb.String()
}

// peerSourceIP 返回 RPC 调用方的来源 IP（地址的 host 部分）。
func peerSourceIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return p.Addr.String()
	}
	return host
}

// socketPort 返回 host:port 形态地址的端口；unix:/npipe: 等非 TCP 形态返回空。
func socketPort(socketPath string) string {
	_, port, err := net.SplitHostPort(socketPath)
	if err != nil {
		return ""
	}
	return port
}

// recordFailure 记录一次注册失败并返回是否入账：name+socket_path+error 同 key
// 在去重窗口内返回 false（不记录、不发事件）。入账时向事件总线发 register_failed。
func (s *RegistryServer) recordFailure(m *Manifest, socketPath, errMsg, owner string) bool {
	key := m.Name + "|" + socketPath + "|" + errMsg
	now := time.Now()
	s.failMu.Lock()
	if last, ok := s.failLastSeen[key]; ok && now.Sub(last) < failureDedupWindow {
		s.failMu.Unlock()
		return false
	}
	s.failLastSeen[key] = now
	kept := make([]RegisterFailure, 0, len(s.failures)+1)
	for _, f := range s.failures {
		if now.Sub(f.Timestamp) < failureTTL {
			kept = append(kept, f)
		} else {
			delete(s.failLastSeen, f.Name+"|"+f.SocketPath+"|"+f.Error)
		}
	}
	kept = append(kept, RegisterFailure{
		Name: m.Name, SocketPath: socketPath, Error: errMsg, Owner: owner, Timestamp: now,
	})
	if overflow := len(kept) - maxRecentFailures; overflow > 0 {
		for _, f := range kept[:overflow] {
			delete(s.failLastSeen, f.Name+"|"+f.SocketPath+"|"+f.Error)
		}
		kept = kept[overflow:]
	}
	s.failures = kept
	s.failMu.Unlock()

	s.emit(PluginEvent{
		Type:       PluginEventRegisterFailed,
		Name:       m.Name,
		SocketPath: socketPath,
		Error:      errMsg,
		Owner:      owner,
		Timestamp:  now,
	})
	return true
}

// ListRegisterFailures 返回未过期的注册失败快照（新的在前）。
func (s *RegistryServer) ListRegisterFailures() []RegisterFailure {
	now := time.Now()
	s.failMu.Lock()
	defer s.failMu.Unlock()
	out := make([]RegisterFailure, 0, len(s.failures))
	for i := len(s.failures) - 1; i >= 0; i-- {
		if now.Sub(s.failures[i].Timestamp) < failureTTL {
			out = append(out, s.failures[i])
		}
	}
	return out
}

// formatReport 把语义契约 Report 压缩为适合注册错误消息的单行摘要，
// 完整 JSON 形态由 plugin.verify 输出。
func formatReport(r *sdkcontract.Report) string {
	var sb strings.Builder
	for i, v := range r.Violations {
		if i >= 5 {
			fmt.Fprintf(&sb, " (+%d more)", len(r.Violations)-i)
			break
		}
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(v.Error())
	}
	return sb.String()
}

// Heartbeat 处理插件心跳，更新 LastHeartbeat 并标记为在线。
func (s *RegistryServer) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	s.mu.Lock()
	rp, ok := s.plugins[req.InstanceId]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("unknown instance_id: %s", req.InstanceId)
	}
	wasOnline := rp.Online.Load()
	name := rp.Manifest.Name
	instanceID := req.InstanceId
	if rp.Tunnel && rp.Client != nil && tunnelClientClosed(rp.Client) {
		// 隧道已断但插件还在发心跳：拒绝，而不是让该实例「复活」回在线。
		// SDK 收到错误即判定心跳失联 → 退避重连并重新 Register（新 instance_id）。
		s.mu.Unlock()
		return nil, fmt.Errorf("tunnel closed for instance %q: re-register required", instanceID)
	}
	// ① 隧道插件同样记录心跳：TCP 半开时 Connect 流的 Recv 不会报错，
	// 心跳超时是宿主侧唯一能发现「插件已死但连接还在」的信号。
	rp.LastHeartbeat = time.Now()
	rp.Online.Store(true)
	s.mu.Unlock()

	if !wasOnline {
		// 离线 → 在线翻转（心跳恢复），推送 online 事件。
		s.emit(PluginEvent{
			Type:       PluginEventOnline,
			InstanceID: instanceID,
			Name:       name,
			Online:     true,
			Timestamp:  time.Now(),
		})
	}
	return &pb.HeartbeatResponse{}, nil
}

// Deregister 处理插件主动下线，关闭连接并从注册表移除。
func (s *RegistryServer) Deregister(ctx context.Context, req *pb.DeregisterRequest) (*pb.DeregisterResponse, error) {
	s.mu.Lock()
	rp, ok := s.plugins[req.InstanceId]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("unknown instance_id: %s", req.InstanceId)
	}
	name := rp.Manifest.Name
	instanceID := req.InstanceId
	if rp.Conn != nil {
		_ = rp.Conn.Close()
	}
	delete(s.plugins, instanceID)
	delete(s.byName, pluginKey(rp.Owner, name))
	s.mu.Unlock()

	slog.Info("plugin deregistered", "instance_id", instanceID, "name", name)

	s.emit(PluginEvent{
		Type:       PluginEventDeregister,
		InstanceID: instanceID,
		Name:       name,
		Online:     false,
		Timestamp:  time.Now(),
	})

	return &pb.DeregisterResponse{}, nil
}

// CheckOffline 扫描注册表，将心跳超时的插件标记下线。
// 应由外部定时调用（如每秒）。
//
// 隧道插件同样参与心跳超时判定（①）：半开连接下 Connect 流不会报错，
// 只有心跳超时能发现插件已经消失。此外隧道还有两条自己的规则：
//   - 已绑定的隧道插件若其 Connect 会话已关闭（断开钩子丢失的兜底）→ 判离线；
//   - 一直未绑定隧道的注册（插件崩溃在 Connect 前/从未 Connect）超过
//     2×timeout 宽限 → 从注册表移除（SDK 重启后会重新 Register）。
func (s *RegistryServer) CheckOffline(timeout time.Duration) {
	s.mu.Lock()
	now := time.Now()
	var transitions []PluginEvent
	var reaped []PluginEvent
	for id, rp := range s.plugins {
		if rp.Tunnel {
			// 兜底：断开钩子丢失时，检测已死 Connect 会话并判离线
			if rp.Client != nil && rp.Online.Load() && tunnelClientClosed(rp.Client) {
				rp.Online.Store(false)
				transitions = append(transitions, PluginEvent{
					Type:       PluginEventOffline,
					InstanceID: id,
					Name:       rp.Manifest.Name,
					Online:     false,
					Timestamp:  now,
				})
				continue
			}
			// 从未绑定的注册超过宽限期则回收
			if rp.Client == nil && now.Sub(rp.LastHeartbeat) > 2*timeout {
				delete(s.plugins, id)
				delete(s.byName, pluginKey(rp.Owner, rp.Manifest.Name))
				reaped = append(reaped, PluginEvent{
					Type:       PluginEventDeregister,
					InstanceID: id,
					Name:       rp.Manifest.Name,
					Online:     false,
					Timestamp:  now,
				})
				continue
			}
		}
		if now.Sub(rp.LastHeartbeat) > timeout && rp.Online.Load() {
			rp.Online.Store(false)
			transitions = append(transitions, PluginEvent{
				Type:       PluginEventOffline,
				InstanceID: id,
				Name:       rp.Manifest.Name,
				Online:     false,
				Timestamp:  now,
			})
		}
	}
	s.mu.Unlock()

	for _, ev := range transitions {
		slog.Warn("plugin heartbeat timeout, marking offline", "instance_id", ev.InstanceID, "name", ev.Name)
		s.emit(ev)
	}
	for _, ev := range reaped {
		slog.Warn("unbound tunnel registration reaped", "instance_id", ev.InstanceID, "name", ev.Name)
		s.emit(ev)
	}
}

// Find 根据 protocol hint 查找第一个匹配的解码插件（匿名/本地语境）。
// 匹配规则：manifest 的 protocol 字段或 hints 列表包含 protocolHint。
// 返回 DecoderClient 和是否找到。等价于 FindFor("", protocolHint)。
func (s *RegistryServer) Find(protocolHint string) (pb.DecoderClient, bool) {
	return s.FindFor("", protocolHint)
}

// FindFor 是 owner 作用域版的 Find：owner != "" 时优先（且限定）该 owner
// 注册的插件，同时兼容匿名注册的本地/系统插件；其他 owner 的插件不可见，
// 使多成员同名插件各自路由互不干扰。owner == "" 时与改造前的 Find 一致
// （只见匿名插件）。
// 隧道插件在 Connect 绑定前（Client 为 nil）不可见。
func (s *RegistryServer) FindFor(owner, protocolHint string) (pb.DecoderClient, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rp := range s.plugins {
		// 只返回在线插件
		if !rp.Online.Load() || rp.Client == nil {
			continue
		}
		// owner 作用域：非匿名调用方看得到自己的插件 + 匿名（系统）插件；
		// 匿名调用方只看匿名插件。其他 owner 的插件不可见。
		if owner != "" {
			if rp.Owner != "" && rp.Owner != owner {
				continue
			}
		} else if rp.Owner != "" {
			continue
		}
		// 匹配 protocol / hints / transports：
		// transports 是插件声明的 L4 传输层能力（tcp|udp），供 dispatcher 按
		// 抓包协议路由——声明 transports:[udp] 的插件可被 FindFor(owner,"udp") 命中。
		if rp.Manifest.Protocol == protocolHint {
			return rp.Client, true
		}
		for _, h := range rp.Manifest.Hints {
			if h == protocolHint {
				return rp.Client, true
			}
		}
		for _, t := range rp.Manifest.Transports {
			if t == protocolHint {
				return rp.Client, true
			}
		}
	}
	return nil, false
}

// GetPlugin 按 name 查找已注册插件，返回 Manifest YAML bytes。
// FindByName 按插件名（manifest.name）精确查找已注册的解码插件。
// 用于一次抓包会话绑定特定插件（如 A 项目→插件 A、B 项目→插件 B），
// 使多项目并行抓包、各用各插件、均不重启主服务成为现实。
// 返回 DecoderClient 和是否找到。插件离线或不存在时返回 nil, false。
// 等价于 FindByNameFor("", name)：键为裸 name，仅匹配匿名注册——与改造前行为一致。
func (s *RegistryServer) FindByName(name string) (pb.DecoderClient, bool) {
	return s.findByKey("", []string{name})
}

// FindByNameFor 是 owner 作用域版的 FindByName：
//   - name 已含 "/"：按 "owner/name" 完整键精确查找；
//   - 否则先试 "<owner>/name"，查不到退化裸 "name"（兼容匿名注册的系统插件）；
//
// owner 为空时只按裸 name 查（匿名/本地语境，行为与改造前完全一致）。
// 解析顺序：先名精确、后退化——调用方（capture_task）的顺序由调用方保持。
func (s *RegistryServer) FindByNameFor(owner, name string) (pb.DecoderClient, bool) {
	return s.findByKey(owner, pluginKeyCandidates(owner, name))
}

// FindByNameAmong 是多 owner 候选版的 FindByNameFor：依序尝试 owners 中每个
// owner 的候选键（owner/name 优先、裸名退化），返回第一个在线命中。
// 用于项目成员共用项目插件：owners = 会话 owner + 所属项目插件条目的归属 owner。
// 隔离语义：只允许命中 owners 集合内的插件（匿名/系统插件恒可见，与单 owner 版一致）；
// 含 "/" 的完整键仍要求键内 owner 属于集合，不能越权寻址。
// owners 为空时行为等价 FindByNameFor("", name)。
func (s *RegistryServer) FindByNameAmong(owners []string, name string) (pb.DecoderClient, bool) {
	keys, allowed := amongCandidates(owners, name)
	if len(keys) == 0 {
		return s.FindByNameFor("", name)
	}
	return s.findByKeyAllowed(keys, allowed)
}

// amongCandidates 汇总多 owner 的查找键（去重保序）与 owner 白名单。
// 空串 owner 恒在白名单内（匿名/系统插件对所有人可见）。
func amongCandidates(owners []string, name string) ([]string, map[string]bool) {
	allowed := make(map[string]bool, len(owners)+1)
	allowed[""] = true
	var keys []string
	seen := map[string]bool{}
	for _, o := range owners {
		allowed[o] = true
		for _, k := range pluginKeyCandidates(o, name) {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys, allowed
}

// findByKeyAllowed 是 findByKey 的多 owner 白名单变体：命中键后校验插件 owner
// 必须在 allowed 集合内。与 findByKey 的差异：某个 owner 的同名实例离线时
// 继续尝试后续候选（多 owner 共用同名插件时，取第一个在线的）。全部检查锁内完成。
func (s *RegistryServer) findByKeyAllowed(keys []string, allowed map[string]bool) (pb.DecoderClient, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range keys {
		id, ok := s.byName[key]
		if !ok {
			continue
		}
		rp, ok := s.plugins[id]
		if !ok || !rp.Online.Load() || rp.Client == nil {
			continue
		}
		if !allowed[rp.Owner] {
			return nil, false
		}
		return rp.Client, true
	}
	return nil, false
}

// findByKey 依序尝试候选键查找在线插件（全部候选检查在 RLock 内完成，
// 避免与 Register/Deregister 的 map 写并发）；隧道插件绑定前（Client nil）
// 不可见。callerOwner 用于隔离校验：即使按完整键（owner/name）寻址，
// 也只能访问自己的或匿名（系统）的插件。
func (s *RegistryServer) findByKey(callerOwner string, keys []string) (pb.DecoderClient, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range keys {
		id, ok := s.byName[key]
		if !ok {
			continue
		}
		rp, ok := s.plugins[id]
		if !ok || !rp.Online.Load() || rp.Client == nil {
			return nil, false
		}
		if rp.Owner != "" && rp.Owner != callerOwner {
			return nil, false
		}
		return rp.Client, true
	}
	return nil, false
}

func (s *RegistryServer) GetPluginManifest(name string) ([]byte, error) {
	return s.GetPluginManifestFor("", name)
}

// GetPluginManifestFor 是 owner 作用域版的 GetPluginManifest，
// 候选键解析与隔离校验同 findByKey（锁内完成）。
func (s *RegistryServer) GetPluginManifestFor(owner, name string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range pluginKeyCandidates(owner, name) {
		ids, ok := s.byName[key]
		if !ok {
			continue
		}
		rp, ok := s.plugins[ids]
		if !ok {
			return nil, fmt.Errorf("plugin %q instance not found", name)
		}
		if rp.Owner != "" && rp.Owner != owner {
			return nil, fmt.Errorf("plugin %q not found", name)
		}
		return yaml.Marshal(rp.Manifest)
	}
	return nil, fmt.Errorf("plugin %q not found", name)
}

// GetPluginManifestAmong 是多 owner 候选版的 GetPluginManifestFor（隔离语义同
// FindByNameAmong：只允许命中 owners 白名单内的插件）。用于会话创建时的
// manifest 快照：会话 owner 自己的插件查不到时，按项目插件归属 owner 依次尝试。
func (s *RegistryServer) GetPluginManifestAmong(owners []string, name string) ([]byte, error) {
	keys, allowed := amongCandidates(owners, name)
	if len(keys) == 0 {
		return s.GetPluginManifestFor("", name)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range keys {
		id, ok := s.byName[key]
		if !ok {
			continue
		}
		rp, ok := s.plugins[id]
		if !ok {
			continue
		}
		if !allowed[rp.Owner] {
			return nil, fmt.Errorf("plugin %q not accessible", name)
		}
		return yaml.Marshal(rp.Manifest)
	}
	return nil, fmt.Errorf("plugin %q not found", name)
}

// NameByClient 返回给定 DecoderClient 对应的插件 manifest name。
// 用于 capture 侧在解码器挂载时反查插件身份（Find/FindByName 只返回 client），
// 进而取 manifest 做规则契约对齐。client 不在任何在线插件下时返回 false。
func (s *RegistryServer) NameByClient(c pb.DecoderClient) (string, bool) {
	if c == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rp := range s.plugins {
		if rp.Client == c {
			return rp.Manifest.Name, true
		}
	}
	return "", false
}

// List 返回当前已注册插件的快照（全量，含下线状态）。
// 返回 PluginSummary 以避免拷贝含 atomic.Bool 的 RegisteredPlugin。
func (s *RegistryServer) List() []PluginSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PluginSummary, 0, len(s.plugins))
	for _, rp := range s.plugins {
		out = append(out, PluginSummary{
			InstanceID:    rp.InstanceID,
			Name:          rp.Manifest.Name,
			Protocol:      rp.Manifest.Protocol,
			Transports:    rp.Manifest.Transports,
			Type:          rp.Manifest.Type,
			APIVersion:    rp.Manifest.APIVersion,
			SocketPath:    rp.SocketPath,
			Online:        rp.Online.Load(),
			LastHeartbeat: rp.LastHeartbeat,
			Owner:         rp.Owner,
			Tunnel:        rp.Tunnel,
		})
	}
	return out
}

// ListSummaries 返回当前已注册插件的摘要列表。
func (s *RegistryServer) ListSummaries() []PluginSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PluginSummary, 0, len(s.plugins))
	for _, rp := range s.plugins {
		out = append(out, PluginSummary{
			InstanceID:    rp.InstanceID,
			Name:          rp.Manifest.Name,
			Protocol:      rp.Manifest.Protocol,
			Transports:    rp.Manifest.Transports,
			Type:          rp.Manifest.Type,
			APIVersion:    rp.Manifest.APIVersion,
			SocketPath:    rp.SocketPath,
			Online:        rp.Online.Load(),
			LastHeartbeat: rp.LastHeartbeat,
			Owner:         rp.Owner,
			Tunnel:        rp.Tunnel,
		})
	}
	return out
}

// WatchOffline 启动心跳超时检测 goroutine。
// timeout 超过此时间的插件标记为下线。
// caller 调用返回的 cancel 函数停止检测。
func (s *RegistryServer) WatchOffline(timeout time.Duration) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.CheckOffline(timeout)
			}
		}
	}()
	return cancel
}

// Close 关闭所有插件连接。置位 closed 后，隧道钩子拒绝绑定/下线，
// Register 也拒绝新注册。
func (s *RegistryServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for id, rp := range s.plugins {
		if rp.Conn != nil {
			_ = rp.Conn.Close()
		}
		delete(s.plugins, id)
	}
	s.byName = map[string]string{}
	return nil
}

// Connect 实现 PluginRegistry 的 Connect 双向流：委托给随注册表创建的
// TunnelHub（见 tunnel.go）。隧道建流/断开钩子回调 RegistryServer 的
// bindTunnelClient / tunnelDisconnected 完成「隧道 ↔ tunnel 注册」的绑定与下线。
func (s *RegistryServer) Connect(stream pb.PluginRegistry_ConnectServer) error {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return fmt.Errorf("registry is closed")
	}
	return s.tunnelHub.Connect(stream)
}

// dialTarget 解析插件上报的 Decode 地址并建立 net.Conn。
// 支持 host:port（TCP，跨机器）、unix:/path、npipe:\\.\pipe\name、以及裸路径（视为 Unix socket）。
func dialTarget(ctx context.Context, target string) (net.Conn, error) {
	switch {
	case strings.HasPrefix(target, "unix:"):
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(target, "unix:"))
	case strings.HasPrefix(target, "npipe:"):
		return dialNamedPipe(strings.TrimPrefix(target, "npipe:"))
	case strings.HasPrefix(target, `\\.\pipe\`):
		return dialNamedPipe(target)
	case strings.ContainsRune(target, '/') || strings.ContainsRune(target, '\\'):
		// 裸路径视为 Unix socket（Windows 路径分隔符是反斜杠）
		return (&net.Dialer{}).DialContext(ctx, "unix", target)
	default:
		// host:port 走 TCP（跨机器部署）
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	}
}

// dialDecoder 建立到插件 Decode 服务的 gRPC 连接。
func dialDecoder(ctx context.Context, target string) (*grpc.ClientConn, error) {
	return grpc.NewClient(
		"passthrough:///"+target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return dialTarget(ctx, target)
		}),
	)
}

// Manager 管理插件注册表（被动接受注册，不再 spawn 子进程）。
type Manager struct {
	registry       *RegistryServer
	workDir        string
	mu             sync.RWMutex
	autoRestart    bool
	restartCount   map[string]int
	registryCancel context.CancelFunc
}

// NewManager 创建插件管理器。
// workDir 是插件注册表 socket 的存放目录（默认 work/gt-registry.sock）。
func NewManager(workDir string) *Manager {
	registry := NewRegistryServer(10)
	return &Manager{
		registry:     registry,
		workDir:      workDir,
		restartCount: map[string]int{},
	}
}

// Start 启动注册表监听（调用方应在服务启动时调用）。
// 返回 registry socket 地址，供插件进程通过环境变量知晓。
func (m *Manager) Start(ctx context.Context) (string, error) {
	sockPath := fmt.Sprintf("%s/gt-registry.sock", m.workDir)
	_ = os.Remove(sockPath)
	srv, lis, err := m.registry.StartListen(sockPath)
	if err != nil {
		return "", err
	}
	m.registryCancel = m.registry.WatchOffline(30 * time.Second)
	go func() {
		if err := srv.Serve(lis); err != nil {
			slog.Error("registry server serve", "error", err)
		}
	}()
	slog.Info("registry server started", "socket", sockPath)
	return sockPath, nil
}

// Find 按 protocol_hint 查找第一个在线插件。
func (m *Manager) Find(protocolHint string) (pb.DecoderClient, bool) {
	return m.registry.Find(protocolHint)
}

// FindByName 按插件名精确查找已注册的解码插件。
func (m *Manager) FindByName(name string) (pb.DecoderClient, bool) {
	return m.registry.FindByName(name)
}

// FindFor 是 owner 作用域版的 Find。
func (m *Manager) FindFor(owner, protocolHint string) (pb.DecoderClient, bool) {
	return m.registry.FindFor(owner, protocolHint)
}

// FindByNameFor 是 owner 作用域版的 FindByName。
func (m *Manager) FindByNameFor(owner, name string) (pb.DecoderClient, bool) {
	return m.registry.FindByNameFor(owner, name)
}

// GetPluginManifestFor 是 owner 作用域版的 GetPluginManifest。
func (m *Manager) GetPluginManifestFor(owner, name string) ([]byte, error) {
	return m.registry.GetPluginManifestFor(owner, name)
}

// Subscribe 订阅插件注册表状态变化事件（register/deregister/online/offline）。
// 返回只读事件通道与退订函数。
func (m *Manager) Subscribe() (<-chan PluginEvent, func()) {
	return m.registry.Subscribe()
}

// List 返回已注册插件摘要。
func (m *Manager) List() []PluginSummary {
	return m.registry.ListSummaries()
}

// Close 关闭注册表服务。
func (m *Manager) Close() error {
	if m.registryCancel != nil {
		m.registryCancel()
	}
	_ = m.registry.Close()
	slog.Info("manager closed")
	return nil
}

// SetAutoRestart 保留占位（Phase 3+ 再实现）。
func (m *Manager) SetAutoRestart(enable bool) {
	m.mu.Lock()
	m.autoRestart = enable
	m.mu.Unlock()
}

// RestartCount 保留占位（Phase 3+ 再实现）。
func (m *Manager) RestartCount(path string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.restartCount[path]
}

// Restart 保留占位（Phase 3+ 再实现）。
func (m *Manager) Restart(path string) error {
	return fmt.Errorf("manual restart not yet implemented in registration mode")
}
