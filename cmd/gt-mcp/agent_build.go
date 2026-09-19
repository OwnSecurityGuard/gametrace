// agent_build.go — gt-agent 探针的「现场编译」（平台页面点击触发，服务器代编译）。
//
// 下载页只下发预置二进制（见 agent_download.go），缺平台的历史行为是提示用
// make build-agents 在合适宿主预置（linux 需 Linux 宿主、darwin 还要求 macOS 宿主）。
// 现场编译让服务端（通常跑在 Linux Docker 里）直接补齐缺失的探针，Web 上点
// 「编译」即触发，部署方不再需要登进服务器操作：
//
//   - windows/amd64：gopacket/pcap 在 Windows 是纯 Go（运行时加载 wpcap.dll），
//     CGO_ENABLED=0 交叉编译，任何带 Go 工具链的宿主都能编；
//   - linux/amd64：cgo libpcap，需要编译环境自带 gcc + libpcap-dev（Dockerfile
//     runtime 已加，见「现场编译依赖」注释）；
//   - darwin/*：Linux 服务器无法交叉编译 macOS 产物，维持「镜像构建期用 osxcross
//     预置」（BUILD_DARWIN_AGENT=1 默认内建 darwin/amd64 + darwin/arm64）；缺失时
//     如实提示补齐方式，不引入独立编译镜像（macOS SDK/工具链进 runtime 不划算）。
//
// 行为契约：
//   - 幂等：产物已存在直接 available，绝不重复编译（编译结果落 GT_AGENT_BIN_DIR，
//     天然缓存——镜像内建的平台永远走缓存，零重复开销）；
//   - 并发：每平台一个在途任务（inflight 去重），重复触发只等同一份产物；
//     另用全局信号量限制并发编译数，避免多个 go build 同时打满内存；
//   - 原子落盘：先写临时文件再 rename，避免半成品被下载端点读到；
//   - 兜底：/download/agent 发现平台缺失且可现场编译时自动触发（显式「编译」
//     按钮之外的自动路径），wait 上限 agentBuildTimeout。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gametrace/pkg/auth"
)

// agentBuildTimeout 单次编译的最大时长；下载端点自动兜底时最长等它这么久。
const agentBuildTimeout = 10 * time.Minute

// agentBuildGlobalLimit 全局并发编译上限（避免多个 go build 同时吃满容器内存）。
const agentBuildGlobalLimit = 2

// agentBuildJob 是一次在途的现场编译任务（按平台去重）。
type agentBuildJob struct {
	done chan struct{}
	err  error
}

// agentBuildManager 管理 gt-agent 的按需现场编译。
type agentBuildManager struct {
	m *mcpCapture

	mu sync.Mutex
	// inflight 平台 -> 在途任务（去重：重复触发同一平台只会等同一份产物）。
	inflight map[string]*agentBuildJob
	// lastErr 平台 -> 最近一次编译失败原因（展示在下载页；成功后清除）。
	lastErr map[string]string
	// sem 全局并发信号量。
	sem chan struct{}
}

func newAgentBuildManager(m *mcpCapture, limit int) *agentBuildManager {
	if limit <= 0 {
		limit = agentBuildGlobalLimit
	}
	return &agentBuildManager{
		m:        m,
		inflight: map[string]*agentBuildJob{},
		lastErr:  map[string]string{},
		sem:      make(chan struct{}, limit),
	}
}

// plan 返回平台的现场编译参数；err 说明该平台为何不能在服务器上现场编译。
func plan(platform string) (goos, goarch, cgo string, exe bool, err error) {
	switch platform {
	case "windows/amd64", "windows/arm64":
		// gopacket/pcap 在 Windows 是纯 Go（运行时加载 Npcap 的 wpcap.dll），
		// CGO_ENABLED=0 即可交叉编译，无需 mingw。
		goarch = strings.TrimPrefix(platform, "windows/")
		return "windows", goarch, "0", true, nil
	case "linux/amd64":
		// cgo libpcap，需编译环境带 gcc + libpcap-dev（Dockerfile runtime 已装）。
		return "linux", "amd64", "1", false, nil
	case "linux/arm64":
		// cgo libpcap 交叉编译需要 aarch64 工具链与 arm64 的 libpcap 头文件，当前
		// runtime 未内置，报错提示补齐方式（镜像预置 / GT_AGENT_BIN_DIR 补充）。
		return "", "", "", false, errors.New("linux/arm64 需要 aarch64 交叉工具链，服务器现场编译暂不可用：请在镜像构建期预置（builder 加装 gcc-aarch64-linux-gnu + libpcap-dev:arm64）或先编好经 GT_AGENT_BIN_DIR 补充")
	default:
		if strings.HasPrefix(platform, "darwin/") {
			return "", "", "", false, errors.New("darwin 需要 macOS 工具链，无法在服务器现场编译：请在镜像构建期预置（BUILD_DARWIN_AGENT=1）或 macOS 宿主 make build-agents 后经 GT_AGENT_BIN_DIR 补充")
		}
		return "", "", "", false, fmt.Errorf("不支持的现场编译平台 %q", platform)
	}
}

// building 报告该平台当前是否有在途编译任务。
func (bm *agentBuildManager) building(platform string) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	_, ok := bm.inflight[platform]
	return ok
}

// lastError 返回该平台最近一次编译失败原因（无则空串）。
func (bm *agentBuildManager) lastError(platform string) string {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.lastErr[platform]
}

// trigger 请求现场编译该平台；返回执行状态：
//
//	"available" — 产物已存在（幂等，不再编译）；
//	"building"  — 已在编或在途（新任务已被启动）；
//	error       — 平台不支持现场编译。
func (bm *agentBuildManager) trigger(platform string) (string, error) {
	if _, _, _, _, perr := plan(platform); perr != nil {
		return "", perr
	}
	if p, ok := bm.platformInfo(platform); ok && p.Available {
		return "available", nil
	}

	bm.mu.Lock()
	if _, ok := bm.inflight[platform]; ok {
		bm.mu.Unlock()
		return "building", nil
	}
	j := &agentBuildJob{done: make(chan struct{})}
	bm.inflight[platform] = j
	bm.mu.Unlock()

	go func() {
		bm.sem <- struct{}{}
		defer func() { <-bm.sem }()

		ctx, cancel := context.WithTimeout(context.Background(), agentBuildTimeout)
		defer cancel()
		j.err = bm.build(ctx, platform)

		close(j.done)
		bm.mu.Lock()
		delete(bm.inflight, platform)
		if j.err != nil {
			bm.lastErr[platform] = j.err.Error()
		} else {
			delete(bm.lastErr, platform)
		}
		bm.mu.Unlock()
		slog.Info("agent build finished", "platform", platform, "error", j.err)
	}()
	return "building", nil
}

// wait 等某平台的在途编译结束（上限 timeout）；返回是否已结束（超时返回 false）。
func (bm *agentBuildManager) wait(platform string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		bm.mu.Lock()
		j, ok := bm.inflight[platform]
		bm.mu.Unlock()
		if !ok {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		select {
		case <-j.done:
		case <-time.After(remaining):
			return false
		}
	}
}

// platformInfo 返回平台矩阵中该平台的产物信息（含可用性）。
func (bm *agentBuildManager) platformInfo(platform string) (prebuiltAgentPlatform, bool) {
	for _, p := range bm.m.availableAgentPlatforms() {
		if p.OS+"/"+p.Arch == platform {
			return p, true
		}
	}
	return prebuiltAgentPlatform{}, false
}

// build 现场编译一份 gt-agent：GOOS/GOARCH 按 plan，linux 用容器内 gcc+libpcap-dev、
// windows 走 CGO_ENABLED=0 纯 Go。产物先写临时文件再原子改名，避免半成品被下载。
func (bm *agentBuildManager) build(ctx context.Context, platform string) error {
	srcDir, err := agentSrcDir()
	if err != nil {
		return err
	}
	goos, goarch, cgo, exe, err := plan(platform)
	if err != nil {
		return err
	}
	binDir, err := bm.m.agentBinDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	out := filepath.Join(binDir, "gt-agent-"+strings.ReplaceAll(platform, "/", "-"))
	if exe {
		out += ".exe"
	}
	tmp, err := os.CreateTemp(binDir, ".gt-agent-build-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-tags", "pcap",
		"-o", tmpPath, "./cmd/gt-agent")
	cmd.Dir = srcDir
	// 过滤宿主侧的 GOOS/GOARCH/CGO_ENABLED/CC/GOWORK 等导游变量，统一用现场值。
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		key := strings.SplitN(kv, "=", 2)[0]
		switch key {
		case "GOOS", "GOARCH", "CGO_ENABLED", "CC", "GOWORK":
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED="+cgo)

	buildOut, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s failed: %w\n%s", platform, err, buildOut)
	}
	if err := os.Rename(tmpPath, out); err != nil {
		return fmt.Errorf("install build artifact: %w", err)
	}
	// 探针产物要有可执行权限（下载解压即用，zip 里也带上 mode）。
	_ = os.Chmod(out, 0o755)
	return nil
}

// agentSrcDir 定位可现场编译的 gt-agent 源码根（必须含 go.mod）。
// 解析顺序：GT_AGENT_SRC_DIR 环境变量 → 当前工作目录（仓库根运行时就是 "."）。
func agentSrcDir() (string, error) {
	for _, g := range []string{os.Getenv("GT_AGENT_SRC_DIR"), "."} {
		if g == "" {
			continue
		}
		if abs, err := filepath.Abs(g); err == nil {
			if st, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil && !st.IsDir() {
				return abs, nil
			}
		}
	}
	return "", errors.New("无法定位 gt-agent 源码（需要含 go.mod 的目录）；请设置 GT_AGENT_SRC_DIR 或从仓库根运行")
}

// handleAgentBuild 是「现场编译探针」的 HTTP 端点（鉴权链内，同 /download/agent）：
//
//	POST /agent/build?platform=<os>/<arch>
//	Authorization: Bearer <token>
//
// 返回 JSON：{"status":"available"|"building"}；平台不支持现场编译时 400
// 并带 message。前端轮询 get_agent_download_options 直到该平台 available
// 后即可下载（下载端点亦会自动兜底等待编译完成）。
func (m *mcpCapture) handleAgentBuild(w http.ResponseWriter, r *http.Request) {
	owner := auth.OwnerFrom(r.Context())
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	if platform == "" {
		http.Error(w, "platform (os/arch) is required, e.g. windows/amd64", http.StatusBadRequest)
		return
	}
	if m.agentBuild == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status": "error", "message": "现场编译未启用（agentBuild manager 未装配）",
		})
		return
	}
	status, err := m.agentBuild.trigger(platform)
	if err != nil {
		slog.Warn("agent build rejected", "owner", owner, "platform", platform, "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": err.Error()})
		return
	}
	slog.Info("agent build requested", "owner", owner, "platform", platform, "status", status)
	code := http.StatusOK
	if status == "building" {
		code = http.StatusAccepted
	}
	writeJSON(w, code, map[string]any{"status": status})
}