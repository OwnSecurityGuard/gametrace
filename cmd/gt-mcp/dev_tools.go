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
	plugindevpb "gametrace/pkg/plugindev/proto"
)

// handleBuildPlugin compiles a scaffolded plugin project via the Developer
// Plane and returns structured file:line:col diagnostics on failure. gt-mcp
// never runs the compiler itself — it forwards to pdClient.Build.
func (m *mcpCapture) handleBuildPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}
	timeoutSec := int(req.GetInt("timeout_sec", 0))
	if m.pdClient == nil {
		return errorResult(fmt.Errorf("plugin dev not available (Developer Plane not configured)")), nil
	}
	resp, err := m.pdClient.Build(ctx, name, timeoutSec)
	if err != nil {
		return errorResult(err), nil
	}
	out := map[string]any{
		"name":   name,
		"ok":     resp.GetOk(),
		"output": resp.GetOutput(),
	}
	var errs []map[string]any
	for _, e := range resp.GetErrors() {
		errs = append(errs, map[string]any{
			"file":    e.GetFile(),
			"line":    e.GetLine(),
			"col":     e.GetCol(),
			"message": e.GetMessage(),
		})
	}
	out["errors"] = errs
	return successResult(out), nil
}

// activate 阶段链。顺序即「第一个没过的是哪一环」：artifact → auth → launch →
// connection → manifest → ready。stage 只在失败时出现，用来指出断点；对用户的
// 结论永远只有一个 status。
const (
	activateStageArtifact   = "artifact"   // 制品：Dev Plane 拿得到可执行的插件二进制
	activateStageAuth       = "auth"       // 准入：注册令牌可用
	activateStageLaunch     = "launch"     // 拉起：插件进程起来并存活
	activateStageConnection = "connection" // 连接：注册请求被 registry 接受且有心跳
	activateStageManifest   = "manifest"   // 声明：manifest 可取
	activateStageReady      = "ready"      // 注册 + 在线 + manifest 全绿
)

// activateEnvelope 收敛 activate 的对外结论：一个 status（ready / failed）+ 断点
// stage + 人话 reason + 可执行的 next 步骤。
//
// 用户问的是「我的插件到底有没有成功」，答案是 ready 或 failed 二选一；process /
// registry / heartbeat / integration / artifact 这些内部概念只该出现在 stage 里，
// 供人排查时定位，而不是作为结论本身。机器字段（process_launched / registered /
// …）由调用方另行补充，Agent 仍可按它们做细粒度决策。
func activateEnvelope(name, stage, reason, code string, next []string) map[string]any {
	ready := stage == activateStageReady
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

// activateNextTool 把断点 stage 映射成推荐的下一步工具（机器可执行建议）。
func activateNextTool(stage string) string {
	switch stage {
	case activateStageArtifact:
		return "build_plugin"
	case activateStageAuth:
		return "get_plugin_env"
	case activateStageManifest:
		return "get_plugin_manifest"
	default:
		return "status_plugin"
	}
}

// launchFailureStage 按 Developer Plane 的启动错误文案判断断点落在「制品」还是
// 「拉起」：找不到二进制是制品问题（先去 build），其余（启动失败 / 秒退 / 已在
// 运行）都是拉起问题。
func launchFailureStage(msg string) (stage, code string, next []string) {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "binary not found"):
		return activateStageArtifact, "binary_missing", []string{
			"先 build_plugin 生成插件二进制",
			"确认二进制落在插件目录（plugins/<name>/<name>）",
			"再 activate_plugin",
		}
	case strings.Contains(lower, "already active"):
		return activateStageLaunch, "already_active", []string{
			"调 status_plugin 看它在运行时里的接入状态（不必重复拉起）",
			"确实要重启用 deactivate_plugin 再 activate_plugin",
		}
	default:
		return activateStageLaunch, "process_did_not_stay_up", []string{
			"看插件目录下的 <name>.dev.log（启动即退的报错原文在里面）",
			"确认 GT_REGISTRY_ADDR 指向的 registry 可达",
			"修好后重新 activate_plugin",
		}
	}
}

// registerFailureStage 归类 registry 记下的注册失败：鉴权被拒是令牌问题（auth），
// 其余（连不上 registry / 隧道模式不回拨 / 旧 SDK 被拒流）都是连接问题。
func registerFailureStage(errMsg string) (stage, code string, next []string) {
	lower := strings.ToLower(errMsg)
	authish := strings.Contains(lower, "permissiondenied") || strings.Contains(lower, "unauthenticated") ||
		strings.Contains(lower, "unauthorized") || strings.Contains(lower, "token") || strings.Contains(lower, "auth")
	if authish {
		return activateStageAuth, "registry_rejected_token", []string{
			"刷新调用方的注册令牌（get_plugin_env 可确认 token 来源）",
			"确认该 owner 的令牌已配置在平台的 GT_AUTH_TOKENS 里",
			"重新 activate_plugin",
		}
	}
	return activateStageConnection, "registry_unreachable_or_rejected", []string{
		"确认插件进程能访问 registry 地址（网络 / 防火墙）",
		"看插件 dev.log 里注册请求是否发出（隧道模式宿主不回拨，地址写错最典型）",
		"修好后重新 activate_plugin",
	}
}

// handleActivatePlugin launches the local plugin binary (Developer Plane) and
// injects GT_REGISTRY_ADDR so it registers with the runtime. registry_addr
// resolves in this order: explicit arg → GT_REGISTRY_ADDR env → the pipeline's
// actual registry address (via GetRegistryAddr). The last fallback means the
// caller never has to know the address — gt-mcp reads it from the runtime.
//
// 接入是否完成不以「进程启动 / activate 返回 ok（仅拿到 pid）」为准：启动后必须
// 联合校验 list_registered_plugins（registered）、status_plugin.online（online）、
// get_plugin_manifest（manifest_present）三项，全部满足才视为集成完成。
// 对外只给一个结论：status=ready|failed，失败时用 stage 指出断点、next 给出可执行
// 的修复步骤。
func (m *mcpCapture) handleActivatePlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}
	registryAddr := req.GetString("registry_addr", "")
	if registryAddr == "" {
		registryAddr = os.Getenv("GT_REGISTRY_ADDR")
	}
	if registryAddr == "" && m.pipelineClient != nil {
		// 回退到 pipeline 实际监听的 registry 地址，避免「不知道该连哪里」。
		if resp, err := m.pipelineClient.GetRegistryAddr(ctx, &pb.GetRegistryAddrRequest{}); err == nil && resp.GetRegistryAddr() != "" {
			registryAddr = resp.GetRegistryAddr()
		}
	}
	token, tokenSrc := m.callerToken(ctx)

	// 拉起的进程没接入完成时，统一补上这组「还没到哪一步」的机器字段。
	incomplete := func() map[string]any {
		return map[string]any{
			"process_launched": false,
			"registered":       false,
			"online":           false,
			"manifest_present": false,
			"integrated":       false,
		}
	}

	if m.pdClient == nil {
		out := activateEnvelope(name, activateStageArtifact,
			"插件开发平面（Developer Plane）不可用，无法编译 / 拉起插件制品",
			"dev_plane_unavailable", []string{
				"确认插件开发平面已随 gt-mcp 启动（未配置时无法 manage 插件制品）",
				"配置好后重新 activate_plugin",
			})
		out["registry_addr"] = registryAddr
		for k, v := range incomplete() {
			out[k] = v
		}
		out["next_action"] = map[string]any{"tool": "build_plugin", "why": "插件开发平面不可用", "requires": "dev_plane_available"}
		return successResult(out), nil
	}
	if registryAddr == "" {
		out := activateEnvelope(name, activateStageConnection,
			"不知道该让插件连哪个 registry：既没传 registry_addr、没设 GT_REGISTRY_ADDR，也读不到 pipeline 的监听地址",
			"registry_addr_unknown", []string{
				"确认 gt-pipeline 在运行（registry 地址由它提供）",
				"或显式传入 registry_addr / 设置 GT_REGISTRY_ADDR 环境变量",
				"再 activate_plugin",
			})
		for k, v := range incomplete() {
			out[k] = v
		}
		out["next_action"] = map[string]any{"tool": "status_plugin", "why": "registry 地址未知", "requires": "registry_reachable"}
		return successResult(out), nil
	}

	// token 预检：平台以 token 模式运行（GT_AUTH_TOKENS 已配置）时，插件启动后
	// 注册会被鉴权拦截。这里在拉起进程之前就快速失败，避免「.env 写错 → 旧进程
	// 带空 token → 一直 PermissionDenied」的接入主路径坑。
	if len(m.tokensByOwner) > 0 && token == "" {
		out := activateEnvelope(name, activateStageAuth,
			"平台以令牌模式运行，但调用方没有可注册的令牌；拒绝拉起一个注定被注册拒绝的插件",
			"missing_auth_token", []string{
				"先解析出调用方的注册令牌（get_plugin_env 可确认来源）",
				"确认该 owner 的令牌已配置在平台的 GT_AUTH_TOKENS 里",
				"再 activate_plugin",
			})
		out["registry_addr"] = registryAddr
		for k, v := range incomplete() {
			out[k] = v
		}
		out["next_action"] = map[string]any{"tool": "get_plugin_env", "why": "缺少可注册的令牌", "requires": "token_resolved"}
		return successResult(out), nil
	}

	resp, err := m.pdClient.Activate(ctx, name, registryAddr, token)
	if err != nil {
		// 拉起失败：Developer Plane 对所有启动期错误都返回 error（二进制缺失 /
		// 已在运行 / 启动即退），这里按文案归类断点，不再回一个光秃秃的 error。
		stage, code, next := launchFailureStage(err.Error())
		out := activateEnvelope(name, stage, err.Error(), code, next)
		out["registry_addr"] = registryAddr
		out["auth_token_source"] = tokenSrc
		for k, v := range incomplete() {
			out[k] = v
		}
		out["next_action"] = map[string]any{"tool": activateNextTool(stage), "why": err.Error(), "requires": code}
		return successResult(out), nil
	}

	// 进程已拉起 → 联合校验（注册 / 在线 / manifest），任一项没过都不算接入完成。
	registered, online, manifestPresent, detail := false, false, false, ""
	if m.pipelineClient != nil {
		registered, online, manifestPresent, detail = m.verifyPluginIntegration(ctx, name)
	} else {
		detail = "gt-mcp 连不上 Runtime Plane：无法确认 registered / online / manifest"
	}
	integrated := registered && online && manifestPresent

	stage, reason, code, next := activateStageReady, detail, "", []string(nil)
	switch {
	case m.pipelineClient == nil:
		stage, code = activateStageConnection, "pipeline_unreachable"
		reason = "插件进程已拉起，但 gt-mcp 连不上 Runtime Plane，无法确认它是否注册成功"
		next = []string{
			"确认 gt-pipeline 在运行，且 gt-mcp 能连上它",
			"连通后重新 activate_plugin，或直接调 status_plugin 复核",
		}
	case !registered:
		if f := m.latestRegisterFailure(ctx, name); f != nil {
			msg, _ := f["error"].(string)
			stage, code, next = registerFailureStage(msg)
			reason = "registry 拒绝了注册：" + msg
		} else {
			stage, code = activateStageConnection, "not_registered"
			reason = "插件进程活着，但 registry 里没有它（注册请求没到，或还没重试成功）"
			next = []string{
				"看插件目录下 <name>.dev.log 里注册请求是否发出、registry 地址是否正确",
				"确认插件进程能访问 registry 地址（网络 / 防火墙）",
				"再 activate_plugin",
			}
		}
	case !online:
		stage, code = activateStageConnection, "not_online"
		next = []string{
			"看插件 dev.log 是否在正常发心跳，并用 status_plugin 看 last_heartbeat",
			"确认插件主循环没有被阻塞或提前退出",
			"再 activate_plugin",
		}
	case !manifestPresent:
		stage, code = activateStageManifest, "missing_manifest"
		next = []string{
			"确认 plugin.yaml 与二进制一起随包提供（在插件目录下）",
			"重新 build_plugin 后再 activate_plugin",
		}
	}

	out := activateEnvelope(name, stage, reason, code, next)
	out["registry_addr"] = registryAddr
	out["auth_token"] = token
	out["auth_token_source"] = tokenSrc
	out["process_launched"] = resp.GetOk()
	out["process_message"] = resp.GetMessage()
	out["instance_id"] = resp.GetInstanceId()
	out["registered"] = registered
	out["online"] = online
	out["manifest_present"] = manifestPresent
	out["integrated"] = integrated
	out["verification_detail"] = detail
	if stage != activateStageReady {
		out["next_action"] = map[string]any{"tool": activateNextTool(stage), "why": reason, "requires": code}
	}
	return successResult(out), nil
}

// verifyPluginIntegration 轮询确认插件真正接入运行时：同时检查
// list_registered_plugins（registered）、status_plugin.online（online）、
// get_plugin_manifest（manifest_present）。三者皆满足才视为集成完成。仅进程
// 启动成功 / activate 返回 ok 不足以证明接入完成。
func (m *mcpCapture) verifyPluginIntegration(ctx context.Context, name string) (registered, online, manifestPresent bool, detail string) {
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
				parts = append(parts, "not in list_registered_plugins")
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

// handleDeactivatePlugin stops the process the Developer Plane launched for the
// plugin. It also best-effort force-deregisters the plugin from the runtime
// registry, covering the case where the process was started externally
// (design: deregister_plugin folds into deactivate — kill if we launched it,
// otherwise force-deregister).
func (m *mcpCapture) handleDeactivatePlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}
	if m.pdClient == nil {
		return errorResult(fmt.Errorf("plugin dev not available (Developer Plane not configured)")), nil
	}
	resp, err := m.pdClient.Deactivate(ctx, name)
	if err != nil {
		return errorResult(err), nil
	}
	out := map[string]any{
		"name":    name,
		"ok":      resp.GetOk(),
		"message": resp.GetMessage(),
	}
	// Best-effort: also remove from the runtime registry if present.
	if m.pipelineClient != nil {
		dresp, derr := m.pipelineClient.DeregisterPlugin(ctx, &pb.DeregisterPluginRequest{Name: name})
		if derr != nil {
			out["registry_deregister"] = map[string]any{"ok": false, "error": derr.Error()}
		} else {
			out["registry_deregister"] = map[string]any{"ok": dresp.GetOk(), "instance_id": dresp.GetInstanceId()}
		}
	}
	return successResult(out), nil
}

// handleGetRegistryAddr 返回插件注册所需的 registry 地址（写入 GT_REGISTRY_ADDR）。
//
// 两个地址必须分开给：
//   - listen_addr：pipeline 进程自己 bind 的地址，由 GetRegistryAddr RPC 原样拿到
//     （容器内视角，形如 :9091）。docker / NAT 部署下这个端口没有对外发布，
//     外面连不到，所以只能当诊断信息，不能给插件用。
//   - registry_addr：调用方真正该连的地址，走 advertisedAddrs（GT_PUBLIC_HOST /
//     GT_PUBLIC_REGISTRY_PORT 显式通告优先，否则按请求 Host 回推）。
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
	out["message"] = "set GT_REGISTRY_ADDR=" + registry + " when launching plugins" +
		" (listen_addr is the in-container bind address; it is usually unreachable from outside under docker/NAT)"
	return successResult(out), nil
}

// handleGetPluginEnv 返回解码插件 .env 的全部 4 项配置与可直接写入的 env_file。
// AI scaffold 插件后调一次即可生成完整 .env：地址走 advertisedAddrs（与
// get_registry_addr 同源），token 反查调用者自己的（与 OAuth 兑换同一信任级别）。
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

	return successResult(map[string]any{
		"registry_addr":       registry,
		"auth_token":          token,
		"token_source":        src,
		"env_file":            buildPluginEnvFile(registry, token),
		"notes": []string{
			"GT_REGISTRY_ADDR: 插件注册端点，原样使用",
			"GT_AUTH_TOKEN: 插件注册鉴权凭证；agent 托管（GT_TUNNEL=1）与 activate_plugin 本地托管都会由平台直接注入，可省略；手工启动时（.env / 命令行）必须自行提供，否则 token 模式下注册会被拒",
			"GT_TUNNEL: 平台统一以隧道模式运行（activate_plugin 与 gt-agent 都会注入 1）；插件不起本地端口、宿主不回拨，注册/心跳/解码帧共用 GT_REGISTRY_ADDR 这一条连接",
			"把 env_file 原样写入插件目录 .env；不要把 .env 提交到 git",
		},
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
	b.WriteString("# 由 gametrace get_plugin_env 生成 —— 复制为 .env，无需手填\n")
	b.WriteString("# 同名环境变量优先于本文件；不要把 .env 提交到 git\n")
	fmt.Fprintf(&b, "GT_REGISTRY_ADDR=%s\n", registryAddr)
	fmt.Fprintf(&b, "GT_AUTH_TOKEN=%s\n", token)
	return b.String()
}

// handleStatusPlugin returns the dual-state view (design §2): the artifact
// (Developer Plane, from disk) merged with the runtime state (Runtime Plane,
// from the registry via pipelineClient.ListPlugins), plus the last attempt for
// failure attribution and a suggested next_action.
func (m *mcpCapture) handleStatusPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}
	if m.pdClient == nil {
		return errorResult(fmt.Errorf("plugin dev not available (Developer Plane not configured)")), nil
	}
	ps, err := m.pdClient.Status(ctx, name)
	if err != nil {
		return errorResult(err), nil
	}

	artifact := map[string]any{
		"state":        "unknown",
		"binary_stale": false,
	}
	if a := ps.GetArtifact(); a != nil {
		artifact["state"] = a.GetState()
		artifact["source_dir"] = a.GetSourceDir()
		artifact["binary_path"] = a.GetBinaryPath()
		artifact["binary_stale"] = a.GetBinaryStale()
	}

	devProcess := map[string]any{"launched": false}
	if d := ps.GetDevProcess(); d != nil {
		devProcess = map[string]any{
			"launched":    d.GetLaunched(),
			"pid":         d.GetPid(),
			"instance_id": d.GetInstanceId(),
			"alive":       d.GetAlive(),
			"launched_at": d.GetLaunchedAtUnix(),
		}
	}

	lastAttempt := map[string]any{}
	if la := ps.GetLastAttempt(); la != nil {
		lastAttempt = map[string]any{
			"action":      la.GetAction(),
			"ok":          la.GetOk(),
			"at_unix":     la.GetAtUnix(),
			"duration_ms": la.GetDurationMs(),
			"message":     la.GetMessage(),
			"explain_ref": la.GetExplainRef(),
		}
		var errs []map[string]any
		for _, e := range la.GetErrors() {
			errs = append(errs, map[string]any{
				"file":    e.GetFile(),
				"line":    e.GetLine(),
				"col":     e.GetCol(),
				"message": e.GetMessage(),
			})
		}
		lastAttempt["errors"] = errs
	}

	// Runtime state comes from the Runtime Plane (the registry).
	runtimeState, runtime := m.runtimeState(ctx, name, devProcess)
	next := nextAction(artifact["state"].(string), runtimeState)

	out := map[string]any{
		"name":         name,
		"artifact":     artifact,
		"runtime":      runtime,
		"dev_process":  devProcess,
		"last_attempt": lastAttempt,
		"next_action":  next,
	}
	// 注册失败（Runtime Plane ring buffer）：命中同名失败时并入。
	// 平台只支持隧道注册，宿主不回拨；失败原因是注册/建流本身
	// （token、registry 地址、缺 instance_id 被拒流等），与 Decode 地址无关。
	if f := m.latestRegisterFailure(ctx, name); f != nil {
		out["register_failure"] = f
		if runtimeState == "offline" {
			out["next_action"] = "注册失败（隧道模式，宿主不回拨）：核对 .env 的 GT_REGISTRY_ADDR 与 GT_AUTH_TOKEN；若插件用旧 SDK 编译（Connect 未带 instance_id）会被拒流，用当前 SDK 重新 build_plugin 后 activate_plugin"
		}
	}
	return successResult(out), nil
}

// devPlaneArtifact 返回 Developer Plane 对指定插件制品的磁盘视图
// （源目录/构建产物/是否过期），与 handleStatusPlugin 的 artifact 字段同源。
// 查询失败或插件不在 Dev Plane 目录（如远端部署）时返回 nil，不阻断调用方。
func (m *mcpCapture) devPlaneArtifact(ctx context.Context, name string) map[string]any {
	ps, err := m.pdClient.Status(ctx, name)
	if err != nil || ps == nil || ps.GetArtifact() == nil {
		return nil
	}
	a := ps.GetArtifact()
	return map[string]any{
		"state":        a.GetState(),
		"source_dir":   a.GetSourceDir(),
		"binary_path":  a.GetBinaryPath(),
		"binary_stale": a.GetBinaryStale(),
	}
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

// runtimeState queries the registry for the named plugin and derives a runtime
// state string. When the pipeline is unavailable it falls back to the
// Developer Plane's own view of the launched process.
func (m *mcpCapture) runtimeState(ctx context.Context, name string, devProcess map[string]any) (string, map[string]any) {
	runtime := map[string]any{
		"state":          "offline",
		"instance_id":    "",
		"online":         false,
		"last_heartbeat": int64(0),
		"bound_sessions": []any{},
	}
	if m.pipelineClient == nil {
		// No runtime link; infer from the dev-launched process only.
		if launched, _ := devProcess["launched"].(bool); launched {
			if alive, _ := devProcess["alive"].(bool); alive {
				runtime["state"] = "registered"
			}
		}
		return runtime["state"].(string), runtime
	}
	resp, err := m.pipelineClient.ListPlugins(ctx, &pb.ListPluginsRequest{})
	if err != nil {
		// Registry unreachable: degrade to the dev-side view.
		if launched, _ := devProcess["launched"].(bool); launched {
			if alive, _ := devProcess["alive"].(bool); alive {
				runtime["state"] = "registered"
			}
		}
		return runtime["state"].(string), runtime
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
	// Not in registry: if we launched it and it's alive, it's mid-registration.
	if launched, _ := devProcess["launched"].(bool); launched {
		if alive, _ := devProcess["alive"].(bool); alive {
			runtime["state"] = "registered"
		}
	}
	return runtime["state"].(string), runtime
}

// nextAction suggests the next tool based on the dual-state, per design §2.2.
func nextAction(artifactState, runtimeState string) map[string]any {
	compiled := artifactState == "compiled"
	offlineOrRegistered := runtimeState == "offline" || runtimeState == "registered"
	switch {
	case artifactState == "unknown" || artifactState == "scaffolded":
		return map[string]any{"tool": "build_plugin", "why": "plugin not compiled yet"}
	case compiled && offlineOrRegistered:
		return map[string]any{"tool": "activate_plugin", "why": "compiled but not active; launch it and register with the runtime"}
	case compiled && runtimeState == "active":
		return map[string]any{"tool": "verify (P4)", "why": "active but not validated; run plugin.verify to reach artifact.state=validated"}
	default:
		return nil
	}
}

// handleExplainPlugin attributes the most recent failure of a plugin via the
// Developer Plane (design §2.3 / P3a / P3b). It is a pure forwarder — gt-mcp
// owns no attribution logic — and its ref is what status_plugin's
// last_attempt.explain_ref points back to.
//
// For decode-class attribution (P3b) the caller may pass a `verify` object
// (the plugin.verify result: {violations, quality, verdict}); it is mapped onto
// the gRPC VerifyResult and forwarded verbatim. When omitted, the Developer
// Plane attributes the most recent result recorded by plugin.verify (P4).
func (m *mcpCapture) handleExplainPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}
	action := req.GetString("action", "")
	if m.pdClient == nil {
		return errorResult(fmt.Errorf("plugin dev not available (Developer Plane not configured)")), nil
	}

	var pbVerify *plugindevpb.VerifyResult
	if m, ok := req.Params.Arguments.(map[string]any); ok {
		if raw, ok := m["verify"]; ok && raw != nil {
			pbVerify = verifyResultFromArg(raw)
		}
	}

	resp, err := m.pdClient.Explain(ctx, name, action, pbVerify)
	if err != nil {
		return errorResult(err), nil
	}
	out := map[string]any{
		"ref":         resp.GetRef(),
		"name":        resp.GetName(),
		"action":      resp.GetAction(),
		"at_unix":     resp.GetAtUnix(),
		"summary":     resp.GetSummary(),
		"next_action": resp.GetNextAction(),
	}
	var findings []map[string]any
	for _, f := range resp.GetFindings() {
		fm := map[string]any{
			"category": f.GetCategory(),
			"rule_id":  f.GetRuleId(),
			"why":      f.GetWhy(),
			"fix":      f.GetFix(),
		}
		if e := f.GetError(); e != nil {
			fm["error"] = map[string]any{
				"file":    e.GetFile(),
				"line":    e.GetLine(),
				"col":     e.GetCol(),
				"message": e.GetMessage(),
			}
		}
		findings = append(findings, fm)
	}
	out["findings"] = findings
	return successResult(out), nil
}

// verifyResultFromArg maps the `verify` tool argument (the verify_plugin JSON:
// {violations, quality, checks, verdict}) onto the gRPC VerifyResult. It is
// strict only about shape, never about attribution — all interpretation stays in
// the Developer Plane. An unparseable payload yields a nil result, which makes
// the Developer Plane attribute its last recorded verify instead.
//
// quality 的 input/decode 分组形状与 verify_plugin 的输出一致；verdict=
// not_applicable 时 quality 为 null，这里就映射成 nil Quality —— 对非本协议
// 流量算出的统计不该拿来归因插件。
func verifyResultFromArg(raw any) *plugindevpb.VerifyResult {
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
	out := &plugindevpb.VerifyResult{Verdict: v.Verdict}
	for _, vv := range v.Violations {
		out.Violations = append(out.Violations, &plugindevpb.Violation{
			RuleId:    vv.RuleID,
			Topic:     vv.Topic,
			Severity:  vv.Severity,
			Statement: vv.Statement,
			DocRef:    vv.DocRef,
			Count:     int32(vv.Count),
			Sample:    vv.Sample,
			Layer:     vv.Layer,
		})
	}
	if q := v.Quality; q != nil {
		out.Quality = &plugindevpb.QualityStats{
			InputRaw:           int32(q.Input.Raw),
			InputCandidate:     int32(q.Input.Candidate),
			DecodeSuccess:      int32(q.Decode.Success),
			DecodeUnknown:      int32(q.Decode.Unknown),
			DecodeUnknownRatio: q.Decode.UnknownRatio,
			CorrelatedInputs:   int32(q.CorrelatedInputs),
			LongPacketErrors:   int32(q.LongPacketErrors),
			EntropyEstimate:    q.EntropyEstimate,
			DecodeErrors:       int32(q.Decode.Errors),
		}
	}
	if c := v.Checks; c != nil {
		out.Checks = &plugindevpb.VerifyChecks{Decode: c.Decode, Semantic: c.Semantic}
	}
	return out
}
