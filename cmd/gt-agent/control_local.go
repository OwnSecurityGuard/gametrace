package main

// control_local.go 是探针的本地控制面：回环 HTTP（127.0.0.1:19500，被占自动 +1），
// 给"坐在那台机器前的人/脚本"用。写操作需 Bearer control.token（首启生成，0600）。
//
// 接口与 gt-singbox-agent 的 /v1 语义对齐（docs/plans/2026-09-05 §4.2）：
//
//	GET  /v1/status      三维度状态
//	GET  /v1/health      存活
//	GET  /v1/config      当前配置（token 脱敏）
//	PUT  /v1/config      部分更新（name / server / token / archive.*）
//	GET  /v1/interfaces  pcap 设备清单
//	POST /v1/capture/start  {session_id, iface?, ports?[], hosts?[], bpf?, snaplen?, promisc?}
//	POST /v1/capture/stop
//	POST /v1/capture/filter {ports?[], hosts?[], bpf?}   热更新，不断流
//	POST /v1/notify         {title, message}   本机弹系统通知
//	GET  /v1/alerts         检查规则告警列表（JSON）
//	GET  /v1/alerts/{id}    单条告警详情页（自包含 HTML）
//
// 不监听非回环地址：需要跨机控制走远端控制通道，不放监听。

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gametrace/pkg/checkrule"
	"gametrace/pkg/version"
)

// localControl 是本地控制面服务。
type localControl struct {
	runner    *captureRunner
	cfg       *agentConfig
	tokenFile string // control.token 路径
	token     string
	ingest    string // 生效的 ingest 地址（start 用）
	alerts    *alertStore
	srv       *http.Server
	addr      string
}

func newLocalControl(runner *captureRunner, cfg *agentConfig, ingest string, alerts *alertStore) *localControl {
	lc := &localControl{runner: runner, cfg: cfg, ingest: ingest, alerts: alerts}
	lc.tokenFile = filepath.Join(configDir(), "control.token")
	if b, err := os.ReadFile(lc.tokenFile); err == nil && len(b) >= 32 {
		lc.token = strings.TrimSpace(string(b))
	} else {
		lc.token = randomHex(24)
		_ = os.MkdirAll(configDir(), 0o700)
		_ = os.WriteFile(lc.tokenFile, []byte(lc.token), 0o600)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", lc.handleStatus)
	mux.HandleFunc("/v1/health", lc.handleHealth)
	mux.HandleFunc("/v1/config", lc.handleConfig)
	mux.HandleFunc("/v1/interfaces", lc.handleInterfaces)
	mux.HandleFunc("/v1/capture/start", lc.handleStart)
	mux.HandleFunc("/v1/capture/stop", lc.handleStop)
	mux.HandleFunc("/v1/capture/filter", lc.handleFilter)
	mux.HandleFunc("/v1/notify", lc.handleNotify)
	mux.HandleFunc("/v1/alerts", lc.handleAlertsList)
	mux.HandleFunc("/v1/alerts/", lc.handleAlertDetail)
	lc.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return lc
}

// Serve 监听（默认 127.0.0.1:19500，被占自动 +1，最多试 20 次）。
func (lc *localControl) Serve(ctx context.Context, addr string) error {
	var lis net.Listener
	var err error
	for i := 0; i < 20; i++ {
		lis, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		host, port, perr := net.SplitHostPort(addr)
		if perr != nil {
			break
		}
		n, _ := strconv.Atoi(port)
		addr = net.JoinHostPort(host, strconv.Itoa(n+1))
	}
	if err != nil {
		return fmt.Errorf("listen local control: %w", err)
	}
	lc.addr = lis.Addr().String()
	// 端口文件：本地脚本/运维从固定位置取真实端口。
	_ = os.WriteFile(filepath.Join(configDir(), "control.port"), []byte(lc.addr), 0o600)
	// 告警存储记下有效地址（端口可能已 +1），供 control_client 拼详情页 URL。
	if lc.alerts != nil {
		lc.alerts.SetAddr(lc.addr)
	}
	slog.Info("local control listening", "addr", lc.addr)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lc.srv.Shutdown(shutdownCtx)
	}()
	if err := lc.srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// authorize 写操作校验：回环端口对本机任意进程可达，没有 token 的控制面等于裸奔。
func (lc *localControl) authorize(r *http.Request) bool {
	if !isLoopback(r.RemoteAddr) {
		return false
	}
	v := r.Header.Get("Authorization")
	v = strings.TrimPrefix(v, "Bearer ")
	return v != "" && v == lc.token
}

func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (lc *localControl) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true})
}

func (lc *localControl) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true, "status": lc.statusSnapshot()})
}

// statusSnapshot 组装三维度状态（本地 /v1/status 与写操作回显共用）。
func (lc *localControl) statusSnapshot() map[string]any {
	state, sessionID, iface, portsCSV, lastErr, updatedMs := lc.runner.State()
	d := lc.runner.Data()
	return map[string]any{
		"probe_id":     lc.cfg.ProbeID,
		"name":         lc.cfg.Name,
		"version":      version.String(),
		"control_addr": lc.addr,
		"ingest_addr":  lc.ingest,
		"registered":   lc.cfg.ProbeID != "" && lc.cfg.ProbeToken != "",
		"capture": map[string]any{
			"state": state, "session_id": sessionID, "iface": iface,
			"ports": portsCSV, "error": lastErr, "updated_unix_ms": updatedMs,
		},
		"data": map[string]any{
			"last_packet_unix_ms": d.LastPacketMs, "last_upload_unix_ms": d.LastUploadMs,
			"packets_captured": d.PacketsCaptured, "packets_acked": d.PacketsAcked,
			"spool_depth": d.SpoolDepth, "dropped": d.Dropped,
		},
	}
}

func (lc *localControl) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg := *lc.cfg
		cfg.ProbeToken = maskSecret(cfg.ProbeToken)
		cfg.UserToken = maskSecret(cfg.UserToken)
		writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true, "config": cfg})
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !lc.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if errStr := lc.applyConfigPatch(patch); errStr != "" {
		writeJSONLocal(w, http.StatusBadRequest, map[string]any{"ok": false, "error": errStr})
		return
	}
	writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true})
}

// applyConfigPatch 应用白名单键（与远端 SetConfig 同一白名单，复用 ControlAgent 的校验逻辑）。
// 这里做的是本地等价实现（saveAgentConfig + 字段映射），避免为复用而引入循环依赖。
func (lc *localControl) applyConfigPatch(patch map[string]any) string {
	get := func(k string) (string, bool) {
		v, ok := patch[k]
		if !ok {
			return "", false
		}
		s, ok := v.(string)
		return s, ok
	}
	if v, ok := get("name"); ok {
		lc.cfg.Name = v
	}
	if v, ok := get("server"); ok {
		lc.cfg.Server = v
		// server 变更需要重启进程（回连地址是长连接的根基），只落盘不热生效。
	}
	if v, ok := get("archive_enabled"); ok {
		switch v {
		case "true", "1", "on":
			lc.cfg.Archive.Enabled = true
		case "false", "0", "off":
			lc.cfg.Archive.Enabled = false
		default:
			return "archive_enabled: invalid value"
		}
	}
	if v, ok := get("archive_max_age_hours"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return "archive_max_age_hours: invalid value"
		}
		lc.cfg.Archive.MaxAgeHrs = n
	}
	if v, ok := get("archive_max_bytes"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return "archive_max_bytes: invalid value"
		}
		lc.cfg.Archive.MaxBytes = n
	}
	for k := range patch {
		switch k {
		case "name", "server", "archive_enabled", "archive_max_age_hours", "archive_max_bytes":
		default:
			return fmt.Sprintf("config key %q not allowed", k)
		}
	}
	if err := saveAgentConfig(lc.cfg); err != nil {
		return err.Error()
	}
	return ""
}

func (lc *localControl) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	ifaces := listInterfacesLocal()
	writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true, "interfaces": ifaces})
}

func (lc *localControl) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !lc.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		SessionID string   `json:"session_id"`
		Iface     string   `json:"iface"`
		Ports     []int32  `json:"ports"`
		Hosts     []string `json:"hosts"`
		BPF       string   `json:"bpf"`
		Protocol  string   `json:"protocol"` // tcp/udp/both；空 = tcp（端口派生）
		SnapLen   int32    `json:"snaplen"`
		Promisc   bool     `json:"promisc"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Promisc == false && req.SnapLen == 0 && req.SessionID == "" {
		// 区分"没填"与"填了 false"：默认混杂开启（与命令行 --promisc 默认一致）。
		req.Promisc = true
	}
	iface := req.Iface
	if iface == "" {
		resolved, err := resolveDefaultIface()
		if err != nil {
			writeJSONLocal(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		iface = resolved
	}
	err := lc.runner.Start(CaptureParams{
		SessionID: req.SessionID, Iface: iface, Ports: req.Ports,
		Hosts: req.Hosts, Protocol: req.Protocol, BPF: req.BPF, SnapLen: req.SnapLen, Promisc: req.Promisc,
	}, lc.ingest, lc.cfg.ProbeToken)
	if err != nil {
		writeJSONLocal(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	lc.writeStatus(w)
}

func (lc *localControl) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !lc.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := lc.runner.Stop(); err != nil {
		writeJSONLocal(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	lc.writeStatus(w)
}

func (lc *localControl) handleFilter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !lc.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Ports    []int32  `json:"ports"`
		Hosts    []string `json:"hosts"`
		BPF      string   `json:"bpf"`
		Protocol string   `json:"protocol"` // tcp/udp/both；空 = tcp（端口派生）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := lc.runner.UpdateFilter(req.Ports, req.Hosts, req.Protocol, req.BPF); err != nil {
		writeJSONLocal(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	lc.writeStatus(w)
}

// handleNotify 在本机弹一条系统通知（{title, message}）——与平台下发的 Notify
// 走同一个实现，供坐在机器前的人自测通知链路是否可用。
func (lc *localControl) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !lc.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := sendSystemNotification(req.Title, req.Message); err != nil {
		writeJSONLocal(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true})
}

func (lc *localControl) writeStatus(w http.ResponseWriter) {
	writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true, "status": lc.statusSnapshot()})
}

// handleAlertsList 返回本地告警摘要列表（JSON，新→旧）。回环只读，免鉴权。
func (lc *localControl) handleAlertsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopback(r.RemoteAddr) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if lc.alerts == nil {
		writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true, "alerts": []any{}})
		return
	}
	writeJSONLocal(w, http.StatusOK, map[string]any{"ok": true, "alerts": lc.alerts.List()})
}

// handleAlertDetail 渲染单条告警的自包含 HTML 详情页（触发记录 + 各方向上下文）。
// 回环只读，免鉴权；未知 id 返回 404。
func (lc *localControl) handleAlertDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopback(r.RemoteAddr) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/alerts/")
	id = strings.Trim(id, "/")
	if lc.alerts == nil || !safeAlertID(id) {
		http.Error(w, "alert not found", http.StatusNotFound)
		return
	}
	b, ok := lc.alerts.Get(id)
	if !ok || b == nil {
		http.Error(w, "alert not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(alertDetailHTML(*b)))
}

// alertDetailHTML 把一条 AlertBundle 渲染成自包含 HTML（内联样式，无外部依赖）。
func alertDetailHTML(b checkrule.AlertBundle) string {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">`)
	sb.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	title := b.Title
	if title == "" {
		title = b.RuleName
	}
	if title == "" {
		title = "检查规则告警"
	}
	sb.WriteString(`<title>` + html.EscapeString(title) + `</title>`)
	sb.WriteString(alertDetailCSS)
	sb.WriteString(`</head><body>`)

	sb.WriteString(`<div class="wrap">`)
	sb.WriteString(`<header><h1>` + html.EscapeString(title) + `</h1>`)
	if b.Message != "" {
		sb.WriteString(`<p class="msg">` + html.EscapeString(b.Message) + `</p>`)
	}
	sb.WriteString(`<div class="meta-grid">`)
	sb.WriteString(metaKV("规则", b.RuleName))
	sb.WriteString(metaKV("规则 ID", b.RuleID))
	sb.WriteString(metaKV("会话", b.SessionID))
	sb.WriteString(metaKV("告警 ID", b.AlertID))
	sb.WriteString(metaKV("生成时间", b.GeneratedAt))
	sb.WriteString(`</div></header>`)

	sb.WriteString(`<section><h2>触发记录</h2>`)
	sb.WriteString(recordHTML(b.Trigger))
	sb.WriteString(`</section>`)

	if len(b.Context) > 0 {
		sb.WriteString(`<section><h2>触发前上下文（各方向最近若干条）</h2>`)
		// 固定方向展示顺序，避免 map 迭代乱序导致每次刷新排版跳动。
		for _, dir := range sortedDirs(b.Context) {
			recs := b.Context[dir]
			sb.WriteString(`<div class="dir"><h3>` + html.EscapeString(dirLabel(dir)) +
				` <span class="count">` + strconv.Itoa(len(recs)) + `</span></h3>`)
			if len(recs) == 0 {
				sb.WriteString(`<p class="empty">（无记录）</p>`)
			} else {
				for _, rec := range recs {
					sb.WriteString(recordHTML(rec))
				}
			}
			sb.WriteString(`</div>`)
		}
		sb.WriteString(`</section>`)
	}

	sb.WriteString(`</div></body></html>`)
	return sb.String()
}

// recordHTML 渲染一条解码记录卡片。
func recordHTML(rec checkrule.AlertRecord) string {
	var sb strings.Builder
	sb.WriteString(`<div class="rec">`)
	sb.WriteString(`<div class="rec-head">`)
	sb.WriteString(`<span class="badge dir-` + html.EscapeString(normalizeDirClass(rec.Direction)) + `">` +
		html.EscapeString(dirLabel(rec.Direction)) + `</span>`)
	if rec.Type != "" {
		sb.WriteString(`<span class="type">` + html.EscapeString(rec.Type) + `</span>`)
	}
	if rec.Timestamp != "" {
		sb.WriteString(`<span class="ts">` + html.EscapeString(rec.Timestamp) + `</span>`)
	}
	if rec.ID != "" {
		sb.WriteString(`<span class="id">` + html.EscapeString(rec.ID) + `</span>`)
	}
	sb.WriteString(`</div>`)
	sb.WriteString(jsonBlock("data", rec.Data))
	sb.WriteString(jsonBlock("meta", rec.Meta))
	sb.WriteString(`</div>`)
	return sb.String()
}

// jsonBlock 渲染一个带标题的 JSON 代码块（值为空则省略）。
func jsonBlock(label string, v any) string {
	if v == nil {
		return ""
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return `<div class="block"><div class="label">` + html.EscapeString(label) +
		`</div><pre>` + html.EscapeString(string(raw)) + `</pre></div>`
}

func metaKV(k, v string) string {
	if v == "" {
		return ""
	}
	return `<div class="kv"><span class="k">` + html.EscapeString(k) +
		`</span><span class="v">` + html.EscapeString(v) + `</span></div>`
}

// sortedDirs 返回 map 的稳定键序：request → response → 其它按字典序 → unknown 末尾。
func sortedDirs(m map[string][]checkrule.AlertRecord) []string {
	dirs := make([]string, 0, len(m))
	for d := range m {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool { return dirRank(dirs[i]) < dirRank(dirs[j]) })
	return dirs
}

func dirRank(d string) int {
	switch d {
	case "request":
		return 0
	case "response":
		return 1
	case "unknown":
		return 3
	default:
		return 2
	}
}

// dirLabel 把归一方向映射成中文标签。
func dirLabel(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "request", "client_to_server", "c2s", "up", "outgoing":
		return "客户端 → 服务端"
	case "response", "server_to_client", "s2c", "down", "incoming":
		return "服务端 → 客户端"
	case "", "unknown":
		return "未知方向"
	default:
		return d
	}
}

// normalizeDirClass 给方向徽章挑一个 CSS 类名（req/resp/unknown）。
func normalizeDirClass(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "request", "client_to_server", "c2s", "up", "outgoing":
		return "req"
	case "response", "server_to_client", "s2c", "down", "incoming":
		return "resp"
	default:
		return "unknown"
	}
}

// alertDetailCSS 是详情页内联样式（暗色优先，兼顾浅色）。
const alertDetailCSS = `<style>
:root{color-scheme:light dark;--bg:#0f1115;--fg:#e6e8eb;--muted:#9aa4b2;--card:#171a21;--line:#262b36;--accent:#4f8cff;}
@media (prefers-color-scheme: light){:root{--bg:#f6f7f9;--fg:#1b1f27;--muted:#5b6472;--card:#fff;--line:#e3e6ea;--accent:#2f6fed;}}
*{box-sizing:border-box;}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif;}
.wrap{max-width:960px;margin:0 auto;padding:24px 16px 64px;}
header h1{font-size:22px;margin:0 0 6px;}
.msg{color:var(--muted);margin:0 0 16px;}
.meta-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:8px 16px;background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px 14px;margin-bottom:24px;}
.kv{display:flex;gap:8px;font-size:13px;}
.kv .k{color:var(--muted);min-width:64px;}
.kv .v{word-break:break-all;}
section{margin-bottom:28px;}
section h2{font-size:16px;margin:0 0 12px;padding-bottom:6px;border-bottom:1px solid var(--line);}
.dir{margin-bottom:16px;}
.dir h3{font-size:14px;margin:0 0 8px;color:var(--muted);display:flex;align-items:center;gap:8px;}
.dir h3 .count{background:var(--line);color:var(--fg);border-radius:10px;padding:0 8px;font-size:12px;}
.empty{color:var(--muted);font-size:13px;margin:0 0 8px;}
.rec{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:10px 12px;margin-bottom:10px;}
.rec-head{display:flex;flex-wrap:wrap;align-items:center;gap:10px;margin-bottom:8px;}
.badge{font-size:12px;font-weight:600;padding:2px 8px;border-radius:6px;}
.badge.dir-req{background:rgba(79,140,255,.15);color:var(--accent);}
.badge.dir-resp{background:rgba(46,184,120,.15);color:#2eb878;}
.badge.dir-unknown{background:var(--line);color:var(--muted);}
.type{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:13px;font-weight:600;}
.ts{color:var(--muted);font-size:12px;}
.id{color:var(--muted);font-size:11px;margin-left:auto;word-break:break-all;}
.block{margin-top:6px;}
.label{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.04em;margin-bottom:2px;}
pre{margin:0;background:var(--bg);border:1px solid var(--line);border-radius:8px;padding:10px;overflow:auto;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12.5px;white-space:pre-wrap;word-break:break-word;}
</style>`

func maskSecret(s string) string {
	if len(s) <= 6 {
		return ""
	}
	return s[:6] + "..."
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x%d", b, time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

func writeJSONLocal(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
