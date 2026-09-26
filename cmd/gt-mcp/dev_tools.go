package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
	"gametrace/pkg/plugindev"
)

// connect 阶段链。顺序即「第一个没过的是哪一环」：auth → connection →
// manifest → ready。stage 只在失败时出现，用来指出断点；对用户的结论永远只有一个
// status。
//
// 与旧的 activate 相比，这里没有 artifact / launch：插件源码、构建与进程都在用户
// 自己的机器上，平台不编译、不拉起、也不持有插件目录 —— 平台只等插件自己注册上来。
const (
	connectStageAuth       = "auth"       // 准入：注册令牌可用
	connectStageConnection = "connection" // 连接：注册请求被 registry 接受且有心跳
	connectStageManifest   = "manifest"   // 声明：manifest 可取
	connectStageReady      = "ready"      // 注册 + 在线 + manifest 全绿
)

// connectEnvelope 收敛 connect 的对外结论：一个 status（ready / failed）+ 断点
// stage + 人话 reason + 可执行的 next 步骤。
//
// 用户问的是「我的插件到底有没有接上」，答案是 ready 或 failed 二选一；registry /
// heartbeat / manifest 这些内部概念只该出现在 stage 里，供人排查时定位，而不是作为
// 结论本身。机器字段（registered / online / manifest_present）由调用方另行补充，
// Agent 仍可按它们做细粒度决策。
func connectEnvelope(name, stage, reason, code string, next []string) map[string]any {
	ready := stage == connectStageReady
	if next == nil {
		next = []string{}
	}
	out := map[string]any{
		"name":   name,
		"ok":     ready,
		"status": "failed",
		"stage":  stage,
		"reason": reason,
		"next":   next,
	}
	if ready {
		out["status"] = "ready"
	} else if code != "" {
		out["failure"] = map[string]any{"code": code, "message": reason}
	}
	return out
}

// connectNextTool 把断点 stage 映射成推荐的下一步工具（机器可执行建议）。
func connectNextTool(stage string) string {
	switch stage {
	case connectStageAuth:
		return "get_plugin_env"
	case connectStageManifest:
		return "get_plugin_manifest"
	default:
		return "status_plugin"
	}
}

// registerFailureStage 归类 registry 记下的注册失败：鉴权被拒是令牌问题（auth），
// 其余（连不上 registry / 隧道模式不回拨 / 旧 SDK 被拒流）都是连接问题。
func registerFailureStage(errMsg string) (stage, code string, next []string) {
	lower := strings.ToLower(errMsg)
	authish := strings.Contains(lower, "permissiondenied") || strings.Contains(lower, "unauthenticated") ||
		strings.Contains(lower, "unauthorized") || strings.Contains(lower, "token") || strings.Contains(lower, "auth")
	if authish {
		return connectStageAuth, "registry_rejected_token", []string{
			"刷新调用方的注册令牌（get_plugin_env 可确认 token 来源）",
			"确认该 owner 的令牌已配置在平台的 GT_AUTH_TOKENS 里",
			"用正确的 GT_AUTH_TOKEN 重启插件进程（平台不会替插件注入令牌）",
			"插件重新注册后再 connect_plugin 复核",
		}
	}
	return connectStageConnection, "registry_unreachable_or_rejected", []string{
		"确认插件进程能访问 registry 地址（网络 / 防火墙）",
		"看插件日志里注册请求是否发出（隧道模式宿主不回拨，地址写错最典型）",
		"插件重启注册后再 connect_plugin 复核",
	}
}

// handleConnectPlugin 告诉平台「我的插件已经在本机启动了，请等它注册上来」。
//
// 它**不**编译、**不**拉起任何进程：插件源码与二进制永远在用户自己的机器上，
// 平台只持有 registry 侧的运行期管理（注册 / 心跳 / 解码）。因此这里做的事只有
// 两件：把注册所需的对外地址与令牌交出去（供用户启动插件时使用），再轮询确认插件
// 真的接上了。
//
// 接入是否完成不以「进程存在」为准：必须联合校验 registered（registry 里有它）、
// online（有心跳）、manifest_present（能取到 plugin.yaml）三项，全部满足才算接入
// 完成。对外只给一个结论：status=ready|failed，失败时用 stage 指出断点、next 给出
// 可执行的修复步骤。
func (m *mcpCapture) handleConnectPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}
	if !pluginNameRe.MatchString(name) {
		return errorResult(fmt.Errorf("name must be kebab-case (lowercase letters, digits, hyphens; must start with a letter), got %q", name)), nil
	}

	// 注册地址解析顺序：显式 arg → 平台对外通告地址（advertisedAddrs，docker/NAT
	// 下唯一可达的那个）→ GT_REGISTRY_ADDR 环境变量。
	registryAddr := req.GetString("registry_addr", "")
	addrSource := "arg"
	if registryAddr == "" {
		registryAddr, addrSource = strings.TrimSpace(os.Getenv("GT_REGISTRY_ADDR")), "env"
	}
	if registryAddr == "" && m.pipelineClient != nil {
		if resp, err := m.pipelineClient.GetRegistryAddr(ctx, &pb.GetRegistryAddrRequest{}); err == nil && resp.GetRegistryAddr() != "" {
			if registry, _, _ := m.advertisedAddrs(ctx, req.GetString("host", "")); registry != "" {
				registryAddr, addrSource = registry, "advertised"
			} else {
				// listen_addr 是容器内 bind 地址，docker/NAT 下外部插件连不到；
				// 仅在拿不到对外通告时兜底，并在 reason 里说明风险。
				registryAddr, addrSource = resp.GetRegistryAddr(), "listen"
			}
		}
	}

	// 未接入完成时统一补上这组「还没到哪一步」的机器字段。
	incomplete := func() map[string]any {
		return map[string]any{
			"registered":       false,
			"online":           false,
			"manifest_present": false,
			"integrated":       false,
		}
	}

	if registryAddr == "" {
		out := connectEnvelope(name, connectStageConnection,
			"不知道该让插件连哪个 registry：既没传 registry_addr、没设 GT_REGISTRY_ADDR，也读不到 pipeline 的通告地址",
			"registry_addr_unknown", []string{
				"确认 gt-pipeline 在运行（registry 地址由它提供）",
				"或显式传入 registry_addr / 设置 GT_REGISTRY_ADDR 环境变量",
				"再 connect_plugin",
			})
		for k, v := range incomplete() {
			out[k] = v
		}
		out["next_action"] = map[string]any{"tool": "status_plugin", "why": "registry 地址未知", "requires": "registry_reachable"}
		return successResult(out), nil
	}

	// token 预检：平台以 token 模式运行（GT_AUTH_TOKENS 已配置）时，插件注册会被
	// 鉴权拦截。插件进程由用户自己启动，平台无法替它注入令牌，所以这里提前把结论
	// 和令牌一起给出去，避免「先看到 failed 才知道缺 token」。
	token, tokenSrc := m.callerToken(ctx)
	if len(m.tokensByOwner) > 0 && token == "" {
		out := connectEnvelope(name, connectStageAuth,
			"平台以令牌模式运行，但调用方没有可注册的令牌；插件手册启动时会因缺少 GT_AUTH_TOKEN 被注册拒绝",
			"missing_auth_token", []string{
				"先解析出调用方的注册令牌（get_plugin_env 可确认来源）",
				"把 GT_AUTH_TOKEN 写进插件进程的环境（.env 或命令行）后重启插件",
				"再 connect_plugin 复核",
			})
		out["registry_addr"] = registryAddr
		out["addr_source"] = addrSource
		for k, v := range incomplete() {
			out[k] = v
		}
		out["next_action"] = map[string]any{"tool": "get_plugin_env", "why": "缺少可注册的令牌", "requires": "token_resolved"}
		return successResult(out), nil
	}

	// 轮询等插件自己注册上来：平台不拉起、也不回拨，只能等。
	registered, online, manifestPresent, detail := m.verifyPluginIntegration(ctx, name)

	stage, reason, code, next := connectStageReady, detail, "", []string(nil)
	switch {
	case m.pipelineClient == nil:
		stage, code = connectStageConnection, "pipeline_unreachable"
		reason = "gt-mcp 连不上 Runtime Plane，无法确认插件是否注册成功"
		next = []string{
			"确认 gt-pipeline 在运行，且 gt-mcp 能连上它",
			"连通后重新 connect_plugin，或直接调 status_plugin 复核",
		}
	case !registered:
		if f := m.latestRegisterFailure(ctx, name); f != nil {
			msg, _ := f["error"].(string)
			stage, code, next = registerFailureStage(msg)
			reason = "registry 拒绝了注册：" + msg
		} else {
			stage, code = connectStageConnection, "not_registered"
			reason = "registry 里还没有这个插件：插件进程没启动，或注册请求还没到"
			next = []string{
				"在插件所在机器上启动插件进程（源码与二进制都在本机，平台不持有）",
				"确认进程的 GT_REGISTRY_ADDR 指向下面返回的 registry_addr",
				"看插件日志里注册请求是否发出、是否被拒",
				"启动后再调 connect_plugin 复核",
			}
		}
	case !online:
		stage, code = connectStageConnection, "not_online"
		next = []string{
			"看插件日志是否在正常发心跳，并用 status_plugin 看 last_heartbeat",
			"确认插件主循环没有被阻塞或提前退出",
			"插件恢复心跳后再 connect_plugin 复核",
		}
	case !manifestPresent:
		stage, code = connectStageManifest, "missing_manifest"
		next = []string{
			"确认插件目录下有 plugin.yaml，且与二进制一起随包提供",
			"修好后重启插件（manifest 随注册一起上报）",
			"再 connect_plugin 复核",
		}
	}

	out := connectEnvelope(name, stage, reason, code, next)
	out["registry_addr"] = registryAddr
	out["addr_source"] = addrSource
	out["auth_token"] = token
	out["auth_token_source"] = tokenSrc
	out["registered"] = registered
	out["online"] = online
	out["manifest_present"] = manifestPresent
	out["integrated"] = registered && online && manifestPresent
	out["verification_detail"] = detail
	if stage != connectStageReady {
		out["next_action"] = map[string]any{"tool": connectNextTool(stage), "why": reason, "requires": code}
	}
	return successResult(out), nil
}

// verifyPluginIntegration 轮询确认插件真正接入运行时：同时检查 registry 里有它
// （registered）、有心跳（online）、manifest 可取（manifest_present）。三者皆满足
// 才视为集成完成。
func (m *mcpCapture) verifyPluginIntegration(ctx context.Context, name string) (registered, online, manifestPresent bool, detail string) {
	if m.pipelineClient == nil {
		return false, false, false, "gt-mcp 连不上 Runtime Plane：无法确认 registered / online / manifest"
	}
	deadline := time.Now().Add(15 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		registered, online, manifestPresent = false, false, false

		listResp, err := m.pipelineClient.ListPlugins(ctx, &pb.ListPluginsRequest{})
		if err == nil {
			for _, p := range listResp.GetPlugins() {
				if p.GetName() == name {
					registered = true
					online = p.GetOnline()
					break
				}
			}
		}
		manResp, merr := m.pipelineClient.GetPluginManifest(ctx, &pb.GetPluginManifestRequest{Name: name})
		manifestPresent = merr == nil && len(manResp.GetManifest()) > 0

		if registered && online && manifestPresent {
			return true, true, true, "registered + online + manifest present"
		}
		if time.Now().After(deadline) {
			var parts []string
			if !registered {
				parts = append(parts, "not in the registry")
			} else if !online {
				parts = append(parts, "registered but not online (no heartbeat yet?)")
			}
			if !manifestPresent {
				parts = append(parts, "get_plugin_manifest returned empty/nil")
			}
			return registered, online, manifestPresent, strings.Join(parts, "; ")
		}
		select {
		case <-ctx.Done():
			return registered, online, manifestPresent, "context cancelled before integration confirmed"
		case <-ticker.C:
		}
	}
}

// handleGetRegistryAddr 返回插件注册所需的 registry 地址（写入 GT_REGISTRY_ADDR）。
//
// 两个地址必须分开给：
//   - listen_addr：pipeline 进程自己 bind 的地址，由 GetRegistryAddr RPC 原样拿到
//     （容器内视角，形如 :9091）。docker / NAT 部署下这个端口没有对外发布，
//     外面连不到，所以只能当诊断信息，不能给插件用。
//   - registry_addr：调用方真正该连的地址，走 advertisedAddrs（GT_PUBLIC_HOST /
//     GT_PUBLIC_REGISTRY_PORT 显式通告优先，否则按请求 Host 回推）。
//
// 插件在用户自己的机器上运行，所以这里给的一定是「对外可达」的那个地址。
func (m *mcpCapture) handleGetRegistryAddr(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if m.pipelineClient == nil {
		return errorResult(fmt.Errorf("pipeline client not available")), nil
	}
	resp, err := m.pipelineClient.GetRegistryAddr(ctx, &pb.GetRegistryAddrRequest{})
	if err != nil {
		return errorResult(fmt.Errorf("get registry addr: %w", err)), nil
	}
	listenAddr := resp.GetRegistryAddr()
	out := map[string]any{"listen_addr": listenAddr}
	if listenAddr == "" {
		out["registry_addr"] = ""
		out["message"] = "pipeline returned an empty registry address (registry not configured via -registry-addr)"
		return successResult(out), nil
	}
	registry, _, src := m.advertisedAddrs(ctx, req.GetString("host", ""))
	out["registry_addr"] = registry
	out["addr_source"] = string(src)
	out["message"] = "set GT_REGISTRY_ADDR=" + registry + " in the plugin process environment" +
		" (the plugin runs on YOUR machine and registers out to the platform; listen_addr is the in-container bind address and is usually unreachable from outside under docker/NAT)"
	return successResult(out), nil
}

// handleGetPluginEnv 返回解码插件 .env 的全部配置与可直接写入的 env_file。
//
// 插件由用户自己启动，所以平台不注入任何环境变量，GT_AUTH_TOKEN 必须由用户写进插件
// 进程环境（.env 或命令行）—— 这一项是手工启动与平台托管的关键差别。
// 地址走 advertisedAddrs（与 get_registry_addr 同源），token 反查调用者自己的
// （与 OAuth 兑换同一信任级别）。
func (m *mcpCapture) handleGetPluginEnv(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if m.pipelineClient == nil {
		return errorResult(fmt.Errorf("pipeline client not available")), nil
	}
	resp, err := m.pipelineClient.GetRegistryAddr(ctx, &pb.GetRegistryAddrRequest{})
	if err != nil {
		return errorResult(fmt.Errorf("get registry addr: %w", err)), nil
	}
	if resp.GetRegistryAddr() == "" {
		return errorResult(fmt.Errorf("pipeline registry not configured (empty registry addr)")), nil
	}
	registry, _, _ := m.advertisedAddrs(ctx, req.GetString("host", ""))

	token, src := m.callerToken(ctx)

	notes := []string{
		"GT_REGISTRY_ADDR: 插件注册端点（平台对外通告地址），原样使用 —— 插件跑在你自己机器上，连的是这个地址",
		"GT_TUNNEL: 平台统一以隧道模式运行，插件注册后主动拨出 Connect 双向流；插件不起本地端口、宿主不回拨，注册/心跳/解码帧共用 GT_REGISTRY_ADDR 这一条连接",
		"把 env_file 原样写入插件工作目录的 .env；源码与 .env 都在你的机器上，平台不保存插件源码，不要把 .env 提交到 git",
	}
	if token == "" {
		notes = append(notes, "GT_AUTH_TOKEN: 当前未解析到令牌（平台可能运行在匿名模式）；token 模式下插件注册会被拒绝，需先在平台配置该 owner 的令牌")
	} else {
		notes = append(notes, "GT_AUTH_TOKEN: 插件注册鉴权凭证；插件由你自己启动，平台不会代注入，必须写进插件进程的环境（.env 或命令行），否则 token 模式下注册会被拒")
	}

	return successResult(map[string]any{
		"registry_addr": registry,
		"auth_token":    token,
		"token_source":  src,
		"env_file":      buildPluginEnvFile(registry, token),
		"notes":         notes,
	}), nil
}

// callerToken 反查调用者自己的注册 token：env（tokensByOwner）优先、users 表兜底；
// 匿名模式（无 Principal / probe 身份）返回空——注册无需鉴权。
// 与 OAuth 兑换的 ownerToken 同源（同一信任级别：调用方已认证为该 owner）。
func (m *mcpCapture) callerToken(ctx context.Context) (string, string) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || p == nil || p.ProbeID != "" {
		return "", "anonymous"
	}
	if t, ok := m.tokensByOwner[p.Owner]; ok && t != "" {
		return t, "env"
	}
	if m.users != nil {
		if t, err := m.users.TokenByOwner(ctx, p.Owner); err == nil && t != "" {
			return t, "users"
		}
	}
	return "", "anonymous"
}

// serviceToken 返回后台系统通道使用的确定性服务凭证。
//
// 使用方有两类：WatchPlugins 这类没有调用方 ctx 的订阅流；以及鉴权豁免端点
// （/singbox/profile）内部反查 pipeline 资源——它们都没有调用方凭证可用。
//
// 优先返回 admin 身份的 token：这类通道需要跨 owner 可见。非 admin 凭证会被
// pipeline 的 owner 作用域过滤（cmd/gt-pipeline/proxy_lease.go ListProxyLeases），
// 结果是服务通道只能看到自己名下的插件/租约——表现就是别人的二维码 profile 反查
// 恒 404、别人的插件事件不广播。没有 admin token 时退回 env 里按 owner 字典序
// 的首个非空 token，再退 users 表首条。匿名模式返回空（服务端无拦截器，无需凭证）。
func (m *mcpCapture) serviceToken() string {
	owners := make([]string, 0, len(m.tokensByOwner))
	for o := range m.tokensByOwner {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	fallback := ""
	for _, o := range owners {
		t := m.tokensByOwner[o]
		if t == "" {
			continue
		}
		if fallback == "" {
			fallback = t
		}
		// envResolver 可能尚未注入（装配早期启动的 WatchPlugins）或为 nil；
		// StaticResolver.Resolve 的 nil/匿名分支安全，此时 IsAdmin 恒 false，
		// 行为与「退回首个 token」一致。
		if p, ok := m.envResolver.Resolve(t); ok && p.IsAdmin {
			return t
		}
	}
	if fallback != "" {
		return fallback
	}
	if m.users != nil {
		if t, err := m.users.AnyToken(context.Background()); err == nil && t != "" {
			return t
		}
	}
	return ""
}

// buildPluginEnvFile 生成可直接写入 .env 的文本（含注释）。
func buildPluginEnvFile(registryAddr, token string) string {
	var b strings.Builder
	b.WriteString("# 由 gametrace get_plugin_env 生成 —— 复制为插件工作目录下的 .env，无需手填\n")
	b.WriteString("# 插件在你的机器上运行；源码与 .env 都不由平台保存，不要把 .env 提交到 git\n")
	fmt.Fprintf(&b, "GT_REGISTRY_ADDR=%s\n", registryAddr)
	fmt.Fprintf(&b, "GT_AUTH_TOKEN=%s\n", token)
	return b.String()
}

// handleStatusPlugin 返回插件实例的单一状态视图：运行期状态（registry：是否注册 /
// 是否在线 / 最近心跳）＋ 验证证明（控制库 plugin_validations）＋ 最近的注册失败，
// 并给出一条 next_action。
//
// 这里没有 artifact / dev_process 了：平台不持有插件源码、编译产物与进程，所以
// 「制品状态」这个概念在平台侧已不存在；插件实例的身份只由 registry 与验证证明
// 两处共同定义。
func (m *mcpCapture) handleStatusPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}

	// 验证证明：由 gt-pipeline 的 verify 写入控制库，按 (owner, name) 唯一。
	// 平台没有插件目录，所以证明只能挂在这里，而不是磁盘文件。
	validation := map[string]any{"validated": false}
	validated := false
	if owner := auth.OwnerFrom(ctx); owner != "" && m.controlStore != nil {
		v, verr := m.controlStore.GetPluginValidation(ctx, owner, name)
		switch {
		case verr != nil:
			validation["error"] = verr.Error()
		case v != nil:
			validated = v.Verdict == plugindev.VerdictPass
			validation = map[string]any{
				"validated":     validated,
				"verdict":       v.Verdict,
				"verify_run_id": v.VerifyRunID,
				"session_id":    v.SessionID,
				"at_unix":       v.At.Unix(),
			}
		}
	}

	runtimeState, runtime := m.runtimeState(ctx, name)
	next := nextAction(runtimeState, validated)

	out := map[string]any{
		"name":        name,
		"runtime":     runtime,
		"validation":  validation,
		"next_action": next,
	}
	// 注册失败（Runtime Plane ring buffer）：命中同名失败时并入。
	// 平台只支持隧道注册，宿主不回拨；失败原因是注册/建流本身
	// （token、registry 地址、缺 instance_id 被拒流等），与 Decode 地址无关。
	if f := m.latestRegisterFailure(ctx, name); f != nil {
		out["register_failure"] = f
		if runtimeState == "offline" {
			out["next_action"] = "注册失败（隧道模式，宿主不回拨）：核对插件进程环境里的 GT_REGISTRY_ADDR 与 GT_AUTH_TOKEN；若插件用旧 SDK 编译（Connect 未带 instance_id）会被拒流，用当前 SDK 重新编译后重启插件"
		}
	}
	return successResult(out), nil
}

// latestRegisterFailure 查询 Runtime Plane 注册失败 ring buffer 中同名插件的最近记录。
func (m *mcpCapture) latestRegisterFailure(ctx context.Context, name string) map[string]any {
	if m.pipelineClient == nil {
		return nil
	}
	resp, err := m.pipelineClient.ListPlugins(ctx, &pb.ListPluginsRequest{})
	if err != nil {
		return nil
	}
	for _, f := range resp.GetRecentFailures() {
		if f.GetName() == name {
			return map[string]any{
				"name":           f.GetName(),
				"error":          f.GetError(),
				"owner":          f.GetOwner(),
				"timestamp_unix": f.GetTimestampUnix(),
			}
		}
	}
	return nil
}

// runtimeState 查 registry 得到插件的运行期状态：offline（registry 里没有） /
// registered（已注册但未在线） / active（在线）。
//
// 没有 dev 侧进程视角可退化了：插件进程在用户机器上，平台看不到它，所以 注册状态
// 只能以 registry 为准。
func (m *mcpCapture) runtimeState(ctx context.Context, name string) (string, map[string]any) {
	runtime := map[string]any{
		"state":          "offline",
		"instance_id":    "",
		"online":         false,
		"last_heartbeat": int64(0),
		"bound_sessions": []any{},
	}
	if m.pipelineClient == nil {
		return "offline", runtime
	}
	resp, err := m.pipelineClient.ListPlugins(ctx, &pb.ListPluginsRequest{})
	if err != nil {
		return "offline", runtime
	}
	for _, p := range resp.GetPlugins() {
		if p.GetName() != name {
			continue
		}
		online := p.GetOnline()
		state := "registered"
		if online {
			state = "active"
		}
		runtime = map[string]any{
			"state":          state,
			"instance_id":    p.GetInstanceId(),
			"online":         online,
			"last_heartbeat": p.GetLastHeartbeatUnix(),
			"bound_sessions": []any{},
		}
		return state, runtime
	}
	return "offline", runtime
}

// nextAction 按运行期状态 + 验证证明推荐下一步工具。
func nextAction(runtimeState string, validated bool) map[string]any {
	switch {
	case runtimeState == "offline":
		return map[string]any{"tool": "connect_plugin", "why": "插件尚未注册到 runtime：在本机启动插件进程（源码与二进制都在你的机器上，平台不持有），再 connect_plugin 确认接入"}
	case validated:
		return nil
	case runtimeState == "registered":
		return map[string]any{"tool": "status_plugin", "why": "已注册但未在线（心跳未到）：确认插件主循环在发心跳"}
	default:
		return map[string]any{"tool": "verify_plugin", "why": "已在线但未通过 verify：跑一次 verify_plugin 立证"}
	}
}

// handleExplainPlugin attributes the most recent verify result of a plugin.
// 归因逻辑在进程内的 pkg/plugindev（纯函数，无 gRPC、无磁盘），gt-mcp 只做参数
// 编解码与结论透传。
//
// For decode-class attribution the caller may pass a `verify` object (the
// verify_plugin result: {violations, quality, checks, verdict}); when omitted the
// most recent result recorded via plugindev.RecordVerify is attributed.
func (m *mcpCapture) handleExplainPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}

	ereq := &plugindev.ExplainRequest{Name: name}
	if args, ok := req.Params.Arguments.(map[string]any); ok {
		if raw, ok := args["verify"]; ok && raw != nil {
			ereq.Verify = verifyResultFromArg(raw)
		}
	}

	res, err := plugindev.Explain(ctx, ereq)
	if err != nil {
		return errorResult(err), nil
	}
	out := map[string]any{
		"ref":         res.Ref,
		"name":        res.Name,
		"action":      res.Action,
		"at_unix":     res.At.Unix(),
		"summary":     res.Summary,
		"next_action": res.NextAction,
	}
	findings := make([]map[string]any, 0, len(res.Findings))
	for _, f := range res.Findings {
		findings = append(findings, map[string]any{
			"category": f.Category,
			"rule_id":  f.RuleID,
			"why":      f.Why,
			"fix":      f.Fix,
		})
	}
	out["findings"] = findings
	return successResult(out), nil
}

// verifyResultFromArg maps the `verify` tool argument (the verify_plugin JSON:
// {violations, quality, checks, verdict}) onto plugindev.VerifyResult. It is
// strict only about shape, never about attribution — all interpretation stays in
// pkg/plugindev. An unparseable payload yields a nil result, which makes Explain
// attribute the last recorded verify instead.
//
// quality 的 input/decode 分组形状与 verify_plugin 的输出一致；verdict=
// not_applicable 时 quality 为 null，这里就映射成 nil Quality —— 对非本协议
// 流量算出的统计不该拿来归因插件。
func verifyResultFromArg(raw any) *plugindev.VerifyResult {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var v struct {
		Violations []struct {
			RuleID    string `json:"rule_id"`
			Topic     string `json:"topic"`
			Severity  string `json:"severity"`
			Statement string `json:"statement"`
			DocRef    string `json:"doc_ref"`
			Count     int    `json:"count"`
			Sample    string `json:"sample"`
			Layer     string `json:"layer"`
		} `json:"violations"`
		Quality *struct {
			Input struct {
				Raw       int `json:"raw"`
				Candidate int `json:"candidate"`
			} `json:"input"`
			Decode struct {
				Success      int     `json:"success"`
				Unknown      int     `json:"unknown"`
				UnknownRatio float64 `json:"unknown_ratio"`
				Errors       int     `json:"errors"`
			} `json:"decode"`
			CorrelatedInputs int     `json:"correlated_inputs"`
			LongPacketErrors int     `json:"long_packet_errors"`
			EntropyEstimate  float64 `json:"entropy_estimate"`
		} `json:"quality"`
		Checks *struct {
			Decode   string `json:"decode"`
			Semantic string `json:"semantic"`
		} `json:"checks"`
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	out := &plugindev.VerifyResult{Verdict: v.Verdict}
	for _, vv := range v.Violations {
		out.Violations = append(out.Violations, &plugindev.Violation{
			RuleID:    vv.RuleID,
			Topic:     vv.Topic,
			Severity:  vv.Severity,
			Statement: vv.Statement,
			DocRef:    vv.DocRef,
			Count:     vv.Count,
			Sample:    vv.Sample,
			Layer:     vv.Layer,
		})
	}
	if q := v.Quality; q != nil {
		out.Quality = &plugindev.QualityStats{
			InputRaw:           q.Input.Raw,
			InputCandidate:     q.Input.Candidate,
			DecodeSuccess:      q.Decode.Success,
			DecodeUnknown:      q.Decode.Unknown,
			DecodeUnknownRatio: q.Decode.UnknownRatio,
			CorrelatedInputs:   q.CorrelatedInputs,
			LongPacketErrors:   q.LongPacketErrors,
			EntropyEstimate:    q.EntropyEstimate,
			DecodeErrors:       q.Decode.Errors,
		}
	}
	if c := v.Checks; c != nil {
		out.Checks = &plugindev.VerifyChecks{Decode: c.Decode, Semantic: c.Semantic}
	}
	return out
}