// singbox_profile.go — 手机 sing-box 客户端远程 profile 配置端点。
//
// 手机端扫码导入的二维码内容是 sing-box URI（sing-box://import-remote-profile?url=...），
// 其中 url 指向本端点 GET /singbox/profile?port=<租约的 agent 监听端口>。SFA 拉取后
// 把返回的完整 sing-box JSON 作为 profile 直接运行：手机以 TUN 模式接管流量，
// TCP 经 HTTP CONNECT 代理转发到该租约 gt-singbox-agent 的监听端口，从而把手机端
// tun 流量送进 GameTrace 租约会话（按用户/设备隔离，互不串流）。
//
// port 参数必填且必须对应一个活跃租约：租约已释放时返回 404（防陈旧二维码），
// pipeline 不可达时返回 503。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
)

// lanFromHost 从 Host 头提取可达的 IPv4 主机地址（排除端口）。
// 手机访问本端点时 Host 即二维码中填写的 LAN IP。回环地址视为不可达。
func lanFromHost(host string) (string, bool) {
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	ip := net.ParseIP(h)
	if ip == nil || ip.IsLoopback() || ip.To4() == nil {
		return "", false
	}
	return ip.String(), true
}

// leasePortActive 校验 port 是否对应一个活跃租约的 agent 监听端口。
// pipeline 不可达返回 err（调用方回 503）；无匹配租约返回 found=false（调用方回 404）。
func (m *mcpCapture) leasePortActive(ctx context.Context, port int) (found bool, err error) {
	if m.pipelineClient == nil {
		return false, fmt.Errorf("pipeline client not available")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := m.pipelineClient.ListProxyLeases(ctx, &pb.ListProxyLeasesRequest{AllOwners: true})
	if err != nil {
		return false, err
	}
	for _, l := range resp.GetLeases() {
		if int(l.GetAgentListenPort()) == port {
			return true, nil
		}
	}
	return false, nil
}

// buildSingboxConfig 生成手机 sing-box 客户端可运行的完整配置：
// TUN 入站接管手机流量，出站经 HTTP CONNECT 代理转发到 agent，
// DNS 直连本地，最终路由走代理。
//
// server:port 是手机视角可达的地址——即宿主对外映射出来的地址与端口，
// 不是容器内 agent 的监听地址。
func buildSingboxConfig(server string, port int) map[string]any {
	return map[string]any{
		"log": map[string]any{
			"level":     "info",
			"timestamp": true,
		},
		"dns": map[string]any{
			"servers": []any{
				// sing-box 1.12+ 移除了 legacy 格式（"address": "local"），
				// 必须使用 type 字段声明 DNS 服务器类型。
				map[string]any{"type": "local", "tag": "local"},
			},
			"final": "local",
		},
		"inbounds": []any{
			map[string]any{
				"type":           "tun",
				"tag":            "tun-in",
				"interface_name": "gta0",
				"mtu":            9000,
				"address":        []string{"172.19.0.1/30"},
				"auto_route":     true,
				"strict_route":   false,
				"stack":          "gvisor",
			},
		},
		"outbounds": []any{
			map[string]any{
				"type":        "http",
				"tag":         "proxy",
				"server":      server,
				"server_port": port,
			},
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			"final":                 "proxy",
			"auto_detect_interface": true,
			"rules": []any{
				// sniff / hijack-dns 是 sing-box 1.11+ 的 rule action，
				// 替代 legacy 的 inbound.sniff 与 dns 特殊出站（1.13.0 移除）。
				map[string]any{"action": "sniff"},
				map[string]any{"protocol": "dns", "action": "hijack-dns"},
			},
		},
	}
}

// handleSingboxProfile 输出手机 sing-box 客户端可导入的远程 profile 配置。
// GET /singbox/profile?port=<agent_listen_port>
// port 必填：租约二维码携带的 agent 监听端口；对应租约不活跃时 404（防陈旧二维码）。
func (m *mcpCapture) handleSingboxProfile(w http.ResponseWriter, r *http.Request) {
	portStr := strings.TrimSpace(r.URL.Query().Get("port"))
	publicPort, err := strconv.Atoi(portStr)
	if err != nil || publicPort <= 0 || publicPort > 65535 {
		http.Error(w, "missing or invalid required query param: port (the proxy lease's public CONNECT port)", http.StatusBadRequest)
		return
	}
	// 二维码里带的是宿主对外端口（手机可达），pipeline 上报的是容器内 agent 端口，
	// 按同一偏移反解后再比对；恒等映射时两者相同。
	agentPort := m.agentPortFromPublic(publicPort)
	if agentPort <= 0 || agentPort > 65535 {
		http.Error(w, fmt.Sprintf("port %d does not map to a valid agent listen port (offset %d)",
			publicPort, m.proxyPortOffset), http.StatusBadRequest)
		return
	}
	server, ok := lanFromHost(r.Host)
	if !ok {
		// 本地/回环访问时回退到探测的局域网 IP，便于浏览器直接测试。
		server = lanIP()
	}
	if server == "" {
		http.Error(w, "cannot determine proxy server address", http.StatusServiceUnavailable)
		return
	}
	// 本端点刻意挂在鉴权链之外（SFA 扫码导入时无法携带 Bearer 头），所以
	// r.Context() 里没有调用方凭证。但反查租约要走 pipeline 的 CaptureControl，
	// mcp 的 gRPC 客户端拦截器只能从 ctx 取 token 附加 Bearer——取不到就不加
	// header，pipeline 的 auth 拦截器直接 PermissionDenied，这里表现为 503
	// 「pipeline unavailable」。故用进程内确定性服务凭证补上（与 WatchPlugins
	// 的后台流同一条路径），且该凭证优先取 admin，才能跨 owner 查到别人的租约。
	ctx := auth.WithToken(r.Context(), m.serviceToken())
	found, err := m.leasePortActive(ctx, agentPort)
	if err != nil {
		slog.Warn("singbox profile: lease lookup failed", "port", publicPort, "agent_port", agentPort, "error", err)
		http.Error(w, "pipeline unavailable", http.StatusServiceUnavailable)
		return
	}
	if !found {
		http.Error(w, fmt.Sprintf("no active proxy lease listening on port %d (released?)", publicPort), http.StatusNotFound)
		return
	}
	// 配置里写对外端口——手机连的是宿主机映射出来的那个端口。
	cfg := buildSingboxConfig(server, publicPort)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		slog.Error("encode singbox profile", "error", err)
		http.Error(w, fmt.Sprintf("encode failed: %v", err), http.StatusInternalServerError)
		return
	}
	slog.Info("served singbox profile", "server", server, "port", publicPort, "remote", r.RemoteAddr)
}
