// agent_download.go — 远程 agent 下载端点与选项查询。
//
// 让不在同一网络环境的成员也能抓包上报：用户在前端选择目标平台 + 抓包端口，服务端
// 打开一个 agent 接收会话，再把平台对应的**预置二进制**（见 Makefile `build-agents`，
// 产物位于 build/agents/，可由 GT_AGENT_BIN_DIR 覆盖）连同 config.embedded.json
// 打进 zip 下发。终端用户拿到产物直接运行，无需任何命令行参数。不再服务端现场编译。
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
)

// registryIngest 读取 pipeline 实际监听的 registry 地址并推导 ingest（registry 端口 +1，
// 与 gt-agent deriveAddrs 约定一致）。pipeline 不可达时回退默认端口段 :9091/:9092。
func (m *mcpCapture) registryIngest(ctx context.Context) (registry, ingest string) {
	registry = ":" + defaultRegistryPort
	if m.pipelineClient != nil {
		if resp, err := m.pipelineClient.GetRegistryAddr(ctx, &pb.GetRegistryAddrRequest{}); err == nil && resp.GetRegistryAddr() != "" {
			registry = resp.GetRegistryAddr()
		}
	}
	if host, port := splitHostPort(registry); port != "" && port != "0" {
		ingest = net.JoinHostPort(host, nextPort(port))
	}
	return registry, ingest
}

// defaultRegistryPort 是 pipeline 不可达时假定的 registry 端口（与 gt-agent 默认值一致）。
const defaultRegistryPort = "9091"

// 对外通告地址的环境变量（远端探针回连用）。
//
// 为什么必须可配：docker / NAT / 反代部署下，服务端自己看到的监听地址（:9091）
// 与探针真正能连到的地址（宿主机 IP:19091）不是同一个，且服务端无从推导端口映射
// 关系。部署方显式通告是唯一可靠来源；未配置时只能按"请求是怎么进来的"猜，
// 猜不出来退回 lanIP()（容器里会拿到 172.x 这类不可达地址，仅作最后兜底）。
const (
	envPublicHost         = "GT_PUBLIC_HOST"
	envPublicRegistryPort = "GT_PUBLIC_REGISTRY_PORT"
	envPublicIngestPort   = "GT_PUBLIC_INGEST_PORT"
)

// addrSource 标识回连地址的来源，供前端判断可信度并提示（只有 env 是部署方承诺的）。
type addrSource string

const (
	addrSourceEnv     addrSource = "env"     // GT_PUBLIC_HOST 显式配置
	addrSourceRequest addrSource = "request" // 按调用方请求的 Host 回推
	addrSourceLAN     addrSource = "lan"     // 服务端网卡启发式（容器内通常不可达）
)

// advertisedAddrs 解析探针应回连的 registry / ingest 地址。
//
// 优先级：
//  1. GT_PUBLIC_HOST（+ GT_PUBLIC_REGISTRY_PORT / GT_PUBLIC_INGEST_PORT）：部署方显式通告；
//  2. reqHost：调用方（浏览器 / 探针）请求的 Host——它怎么访问到我们，就怎么通告；
//  3. lanIP()：最后兜底。
//
// 端口：显式配置优先；只配了 registry 端口时 ingest 取 registry+1；都没配取
// pipeline 内部端口（裸机部署时内外一致，正确）。
func (m *mcpCapture) advertisedAddrs(ctx context.Context, reqHost string) (registry, ingest string, src addrSource) {
	internalReg, internalIng := m.registryIngest(ctx)
	fallbackHost, regPort := splitHostPort(internalReg)
	_, ingPort := splitHostPort(internalIng)

	host := strings.TrimSpace(os.Getenv(envPublicHost))
	src = addrSourceEnv
	if host == "" {
		host = requestHostname(reqHost)
		src = addrSourceRequest
	}
	if host == "" {
		host = lanIP()
		src = addrSourceLAN
	}
	if host == "" {
		host = fallbackHost
	}

	pubRegPort := strings.TrimSpace(os.Getenv(envPublicRegistryPort))
	if pubRegPort == "" {
		pubRegPort = regPort
	}
	pubIngPort := strings.TrimSpace(os.Getenv(envPublicIngestPort))
	if pubIngPort == "" {
		// 只显式配了 registry 端口时，按"ingest = registry+1"的既有约定推导；
		// 两者都没配就用 pipeline 内部端口（可能是互不相邻的端口段）。
		if strings.TrimSpace(os.Getenv(envPublicRegistryPort)) != "" {
			pubIngPort = nextPort(pubRegPort)
		} else {
			pubIngPort = ingPort
		}
	}
	return net.JoinHostPort(host, pubRegPort), net.JoinHostPort(host, pubIngPort), src
}

// requestHostname 从 HTTP 请求的 Host（host[:port]）取可通告的 hostname；
// 回环/空值一律返回空——探针在别的机器上，拿到 localhost 会连到它自己。
func requestHostname(reqHost string) string {
	h, _, err := net.SplitHostPort(strings.TrimSpace(reqHost))
	if err != nil {
		h = strings.Trim(strings.TrimSpace(reqHost), "[]")
	}
	h = strings.TrimSpace(h)
	if h == "" || h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "0.0.0.0" || h == "[::1]" {
		return ""
	}
	return h
}

// splitHostPort 拆 host:port；缺 host 时补回环，缺 port 时返回空 port。
func splitHostPort(addr string) (host, port string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if h := strings.Trim(addr, "[]"); h != "" {
			return h, ""
		}
		return "127.0.0.1", ""
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host, port
}

func nextPort(port string) string {
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n >= 65535 {
		return ""
	}
	return strconv.Itoa(n + 1)
}

// agentBinDir 定位「多平台预置 agent」目录。
// 解析顺序：GT_AGENT_BIN_DIR 环境变量 → 仓库根的 build/agents。
func (m *mcpCapture) agentBinDir() (string, error) {
	for _, guess := range []string{os.Getenv("GT_AGENT_BIN_DIR"), filepath.Join(".", "build", "agents")} {
		if guess == "" {
			continue
		}
		if abs, err := filepath.Abs(guess); err == nil {
			if st, err := os.Stat(abs); err == nil && st.IsDir() {
				return abs, nil
			}
		}
	}
	// 目录不存在也返回默认路径（供下载时给出明确“该平台未预置”错误）。
	return func() string {
		abs, _ := filepath.Abs(filepath.Join(".", "build", "agents"))
		return abs
	}(), nil
}

// prebuiltAgentPlatform 是一份已预置（或缺失）的 agent 平台产物。
type prebuiltAgentPlatform struct {
	OS        string `json:"os"`        // windows / linux / darwin
	Arch      string `json:"arch"`      // amd64 / arm64
	Label     string `json:"label"`     // 展示名，如 "Windows x64"
	ExeSuffix bool   `json:"exe"`       // 是否需要 .exe 后缀
	Available bool   `json:"available"` // 该平台产物是否已预置
	Filename  string `json:"filename"`  // 磁盘文件名（含 .exe 时）
}

// prebuiltplatforms 定义下载 agent 支持的目标平台矩阵（按公开顺序）。
func prebuiltPlatforms() []struct {
	OS        string
	Arch      string
	Label     string
	ExeSuffix bool
} {
	return []struct {
		OS        string
		Arch      string
		Label     string
		ExeSuffix bool
	}{
		{"windows", "amd64", "Windows x64", true},
		{"linux", "amd64", "Linux x64", false},
		{"windows", "arm64", "Windows ARM64", true},
		{"linux", "arm64", "Linux ARM64", false},
	}
}

// availableAgentPlatforms 扫描 agentBinDir，返回每份产物及其可用性。
func (m *mcpCapture) availableAgentPlatforms() []prebuiltAgentPlatform {
	binDir, _ := m.agentBinDir()
	out := make([]prebuiltAgentPlatform, 0, 4)
	for _, p := range prebuiltPlatforms() {
		fn := "gt-agent-" + p.OS + "-" + p.Arch
		if p.ExeSuffix {
			fn += ".exe"
		}
		avail := false
		if st, err := os.Stat(filepath.Join(binDir, fn)); err == nil && !st.IsDir() {
			avail = true
		}
		out = append(out, prebuiltAgentPlatform{
			OS:        p.OS,
			Arch:      p.Arch,
			Label:     p.Label,
			ExeSuffix: p.ExeSuffix,
			Available: avail,
			Filename:  fn,
		})
	}
	return out
}

// handleGetAgentDownloadOptions 返回下载 Agent 页面需要的服务端信息：
// 探针回连地址（registry/ingest，含对外端口）与可下载的目标平台矩阵。
//
// host 参数（可选）：调用方所在网络看到的服务器 host（前端传 window.location.hostname）。
// 服务端无法感知 NAT/端口映射，只能靠调用方告知或部署方配 GT_PUBLIC_HOST。
func (m *mcpCapture) handleGetAgentDownloadOptions(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	registry, ingest, src := m.advertisedAddrs(ctx, req.GetString("host", ""))
	host, registryPort := splitHostPort(registry)
	_, ingestPort := splitHostPort(ingest)
	msg := "选择目标操作系统下载 Agent。解压后双击运行 gt-agent(.exe) 即可接入；抓包端口与解码插件稍后在「开始抓包」里指定，由平台下发给探针。"
	if src != addrSourceEnv {
		msg += " 注意：服务端未配置 GT_PUBLIC_HOST，回连地址是按调用方请求回推的——Docker/公网部署请在服务端设置 GT_PUBLIC_HOST（必要时配 GT_PUBLIC_REGISTRY_PORT / GT_PUBLIC_INGEST_PORT），否则远端探针可能连不上。"
	}
	out := map[string]any{
		"host":          host,
		"registry_addr": registry,
		"ingest_addr":   ingest,
		"registry_port": registryPort,
		"ingest_port":   ingestPort,
		"addr_source":   string(src),
		"platforms":     m.availableAgentPlatforms(),
		"message":       msg,
	}
	return successResult(out), nil
}

// serviceBearerToken 从请求中提取当前调用者凭证，用于一并烧进 agent sidecar 配置。
// 优先 Authorization: Bearer，其次 X-GT-Token，最后兼容回退 query `token`
// （后者会进访问日志，仅作过渡保留）。
func serviceBearerToken(r *http.Request, q url.Values) string {
	if h := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(h, "Bearer ") {
		if t := strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")); t != "" {
			return t
		}
	}
	if h := strings.TrimSpace(r.Header.Get("X-GT-Token")); h != "" {
		return h
	}
	return strings.TrimSpace(q.Get("token"))
}

// buildAgentZip 把选定的预置平台二进制与该下载对应的 sidecar 配置
// （config.embedded.json）打成 zip。产物解压后，通用 gt-agent 会在运行时
// 读取同目录 config.embedded.json，从而免参数回连服务端、托管插件并抓包。
func buildAgentZip(binPath string, cfgJSON []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	cfgW, err := zw.Create("config.embedded.json")
	if err != nil {
		return nil, err
	}
	if _, err := cfgW.Write(cfgJSON); err != nil {
		return nil, err
	}
	binData, err := os.ReadFile(binPath)
	if err != nil {
		return nil, err
	}
	binW, err := zw.Create(filepath.Base(binPath))
	if err != nil {
		return nil, err
	}
	if _, err := binW.Write(binData); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// handleAgentDownload 是下载 Agent 的 HTTP 端点：
//
//	GET /download/agent?platform=<os>/<arch>[&server=host:port]
//	Authorization: Bearer <token>
//
// 只下发「平台产物 + 回连配置」：抓包端口、解码插件都不在下载时确定——
// 探针接入后由 Web 通过 probe_start_capture 下发（想改就改，不必重下探针）。
// 同理这里不开会话：会话是「开始抓包」时创建的。
//
// serveAgentZip 把选定的预置平台二进制与该平台对应的 sidecar 配置
// （config.embedded.json，含回连地址与 token）打成 zip 下发。
// cfgJSON 为空时回退占位 {}，但下载端点应始终传入带齐身份与回连的配置，
// 否则探针解压即待命却无凭证，注册会被服务端拒绝（见 handleAgentDownload）。
// 返回 true 表示成功写出 zip；false 表示已写出错误响应（404/400/500）。
func (m *mcpCapture) serveAgentZip(w http.ResponseWriter, platform string, cfgJSON []byte) bool {
	binDir, _ := m.agentBinDir()
	var binPath string
	for _, p := range m.availableAgentPlatforms() {
		if p.OS+"/"+p.Arch != platform {
			continue
		}
		if !p.Available {
			http.Error(w, "platform "+platform+" is not available; run `make build-agents` on the server to prebuild it", http.StatusNotFound)
			return false
		}
		binPath = filepath.Join(binDir, p.Filename)
		break
	}
	if binPath == "" {
		http.Error(w, "unsupported platform: "+platform, http.StatusBadRequest)
		return false
	}
	if len(cfgJSON) == 0 {
		cfgJSON = []byte("{}")
	}
	zipData, err := buildAgentZip(binPath, cfgJSON)
	if err != nil {
		http.Error(w, "package agent zip failed: "+err.Error(), http.StatusInternalServerError)
		return false
	}
	// 平台名含 "/"（windows/amd64），不能进 filename；文件名统一用连字符。
	zipName := "gt-agent-" + strings.ReplaceAll(platform, "/", "-") + "-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+zipName+`"`)
	w.Header().Set("Cache-Control", "no-store")
	// 显式 Content-Length：zip 已整体在内存中，用定长传输而非 chunked——
	// chunked 大响应经 Docker Desktop wslrelay 等中继链路转发时可能被截断，
	// 浏览器侧表现为 fetch 抛 "Failed to fetch"（服务端 Write 却全部成功）。
	w.Header().Set("Content-Length", strconv.Itoa(len(zipData)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(zipData); err != nil {
		slog.Warn("stream agent zip failed", "platform", platform, "error", err)
		return false
	}
	slog.Info("agent binary served", "platform", platform, "bytes", len(zipData))
	return true
}

// codeAgentConfig 为启动码接入模式构造 sidecar 配置（含 token），供下载 zip 烧入。
// 与 /access/claim 返回内容一致；返回 ok=false 时错误响应已写出。
func (m *mcpCapture) codeAgentConfig(w http.ResponseWriter, r *http.Request, code string) ([]byte, bool) {
	_, token, registry, ingest, ok := m.resolveAccessCode(w, r, code)
	if !ok {
		return nil, false
	}
	cfgJSON, err := json.Marshal(map[string]any{
		"server":        registry,
		"registry_addr": registry,
		"ingest_addr":   ingest,
		"token":         token,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	return cfgJSON, true
}

func (m *mcpCapture) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	owner := auth.OwnerFrom(r.Context())
	q := r.URL.Query()

	// 启动码接入：token 来自启动码对应的身份（与 /access/claim 同源），一并烧进
	// config.embedded.json，使下载产物自包含——即使脱离接入脚本直接运行也有凭证。
	if code := strings.TrimSpace(q.Get("code")); code != "" {
		platform := strings.TrimSpace(q.Get("platform"))
		if platform == "" {
			http.Error(w, "platform (os/arch) is required, e.g. windows/amd64", http.StatusBadRequest)
			return
		}
		cfgJSON, ok := m.codeAgentConfig(w, r, code)
		if !ok {
			return // 错误响应已在 codeAgentConfig 内写出
		}
		if !m.serveAgentZip(w, platform, cfgJSON) {
			return
		}
		slog.Info("agent downloaded (code mode)", "platform", platform)
		return
	}

	platform := strings.TrimSpace(q.Get("platform"))
	if platform == "" {
		http.Error(w, "platform (os/arch) is required, e.g. windows/amd64", http.StatusBadRequest)
		return
	}
	token := serviceBearerToken(r, q)

	// 回连地址：默认对外通告地址（GT_PUBLIC_HOST 显式配置 > 本请求的 Host 回推）。
	registry, ingest, _ := m.advertisedAddrs(r.Context(), r.Host)
	if server := strings.TrimSpace(q.Get("server")); server != "" {
		// 运维兜底：地址通告错了但又不想重新部署时，用 ?server= 现场覆盖。
		host, port := splitHostPort(server)
		if port == "" {
			port = defaultRegistryPort
		}
		registry = net.JoinHostPort(host, port)
		ingest = net.JoinHostPort(host, nextPort(port))
	}
	if ingest == "" {
		http.Error(w, "cannot resolve ingest address; set GT_PUBLIC_HOST or pass server=host:port", http.StatusInternalServerError)
		return
	}

	// sidecar 配置只带「身份与回连」：server/registry/ingest/token。
	// 不写 session/bpf/plugin_names——抓包参数由平台在下发抓包时给，探针免参数开机即待命。
	cfgJSON, err := json.Marshal(map[string]any{
		"server":        registry,
		"registry_addr": registry,
		"ingest_addr":   ingest,
		"token":         token,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !m.serveAgentZip(w, platform, cfgJSON) {
		return
	}
	slog.Info("agent downloaded", "owner", owner, "platform", platform,
		"registry", registry, "ingest", ingest)
}
