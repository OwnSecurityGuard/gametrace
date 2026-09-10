package main

// simulate_test.go —— 状态变更模块的端到端「探针 + 解码」模拟器。
//
// 它不是单元回归测试，而是一个用真实管线跑真实解码器的测试程序：
//   1. 起一个 in-process pipelineService（与线上同一套代码），带 agent hub + 插件注册表；
//   2. 在 unix socket 上起一个真实的解码 gRPC 服务（sim-game 插件）并注册；
//   3. 开一个 agent 会话，把合成抓包帧经 hub 投递进抓包会话（= 探针上报虚拟数据）；
//   4. 解码器把帧解成带 _state_changes 的事件，真实落库；
//   5. 停会话、读 state_changes 校验落库；若设了 SIM_SERVE=1，再拉起真实 gt-mcp
//      子进程（指向同一个 work-dir），把数据直接喂给 webui 做可视化验证。
//
// 运行：
//   基础（只生成数据并打印落库统计）：
//     go test ./cmd/gt-pipeline/ -run TestSimulate -v
//   生成数据并起 webui（Ctrl-C 停止）：
//     SIM_SERVE=1 go test ./cmd/gt-pipeline/ -run TestSimulate -v
// 鉴权身份直接读项目 .env 的 GT_AUTH_TOKENS，与线上保持一致。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/capture/agent"
	gevent "gametrace/pkg/event"
	"gametrace/pkg/internalipc"
	"gametrace/pkg/internalipc/capturecontrol"
	pb "gametrace/pkg/internalipc/proto"
	"gametrace/pkg/plugin"
	"gametrace/pkg/store"

	"google.golang.org/grpc"
)

// TestSimulate 端到端跑一遍「探针上报 → 解码 → 状态变更落库」。
func TestSimulate(t *testing.T) {
	workDir := simWorkDir()
	controlPath := filepath.Join(workDir, "control.sqlite")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建 work-dir 失败: %v", err)
	}
	controlStore, err := store.NewControlStore(controlPath)
	if err != nil {
		t.Fatalf("NewControlStore: %v", err)
	}
	defer controlStore.Close()

	mgr := plugin.NewRegistryServer(10)
	hub := agent.NewHub()
	s := newPipelineService(workDir, controlStore, mgr, ":9091", "sqlite", "")
	s.SetAgentHub(hub)

	sock, stopDec, err := startSimDecoder(mgr)
	if err != nil {
		t.Fatalf("启动 sim-game 解码器失败: %v", err)
	}
	defer stopDec()
	t.Logf("sim-game 解码器已注册 (socket=%s)", sock)

	owner, tokens := loadTokensFromEnv()
	ctx := context.Background()
	if owner != "" {
		ctx = auth.WithPrincipal(ctx, &auth.Principal{Owner: owner})
	}
	t.Logf("会话归属 owner=%q（token 取自 .env 的 GT_AUTH_TOKENS，与线上一致）", owner)

	res, err := s.StartSession(ctx, capturecontrol.StartSessionRequest{
		Agent:  true,
		Plugin: simPluginName,
		Port:   9250, // 当作游戏服端口，便于方向推断
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sessionID := res.SessionID
	dbPath := res.DBPath
	t.Logf("会话已开 session_id=%s db=%s", sessionID, dbPath)

	// 等 agent source 完成 hub 订阅，否则投递进来的包会被整批丢弃。
	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(sessionID) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hub.Subscribers(sessionID) == 0 {
		t.Fatal("agent source 未订阅 hub：探针推来的包会被整批丢弃")
	}

	// 探针上报：把合成帧当成抓包流量经 hub 投递。
	pkts := generateScenario(time.Now())
	for _, p := range pkts {
		hub.Deliver(sessionID, []gevent.Packet{p})
	}
	t.Logf("探针已上报 %d 帧合成数据", len(pkts))

	// 等主循环消费并落库（管线每秒 flush 一次）。
	time.Sleep(3 * time.Second)
	if _, err := s.StopSession(ctx, sessionID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	// 校验状态变更真的落库了。
	st, err := store.NewSQLiteStoreReadOnly(dbPath, nil)
	if err != nil {
		t.Fatalf("打开会话库失败: %v", err)
	}
	defer st.Close()

	rows, err := st.QueryStateChanges(ctx, store.StateChangeQuery{SessionID: sessionID, Limit: 10000})
	if err != nil {
		t.Fatalf("QueryStateChanges: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("state_changes 为空：解码器没有产出任何状态变更")
	}

	// 打印分布，便于肉眼确认数据真实可信。
	byEntity := map[string]int{}
	byOp := map[string]int{}
	for _, r := range rows {
		key := r.SubjectType + ":" + r.SubjectID
		byEntity[key]++
		byOp[r.Op]++
	}
	t.Logf("落库 state_changes 共 %d 条", len(rows))
	t.Logf("实体数=%d  操作类型分布=%v", len(byEntity), byOp)
	for k, v := range byEntity {
		t.Logf("  %s: %d 次变更", k, v)
	}
	if len(byEntity) < 3 {
		t.Fatalf("实体种类偏少（%d），预期覆盖 Player/Mob 等多类实体", len(byEntity))
	}

	if os.Getenv("SIM_SERVE") != "" {
		// gt-mcp 启动硬依赖能拨通 pipeline 的 CaptureControl gRPC；
		// 测试进程内同时 serve 一份（复用同一个 in-process pipelineService），
		// 这样 SIM_SERVE=1 是自包含的「生成数据 + 起 webui」一键体验，无需另跑 gp-pipeline。
		pipelineAddr := "127.0.0.1:59999"
		stopCtrl, err := servePipelineControl(s, pipelineAddr)
		if err != nil {
			t.Fatalf("serve in-process pipeline control gRPC 失败: %v", err)
		}
		defer stopCtrl()
		serveMCP(workDir, tokens, sessionID, pipelineAddr)
	}
}

// servePipelineControl 在测试进程内起一份 CaptureControl gRPC server，复用同一个
// in-process pipelineService，使拉起的 gt-mcp 子进程能拨通它（否则 gt-mcp 拒绝启动）。
// 仅对外暴露控制面，不重新打开存储——会话数据早已落库在 work-dir 的 sqlite。
func servePipelineControl(engine capturecontrol.CaptureEngine, addr string) (func(), error) {
	lis, err := internalipc.ListenAddr(addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	grpcSrv := grpc.NewServer()
	pb.RegisterCaptureControlServer(grpcSrv, capturecontrol.NewServer(engine))
	go func() {
		if serveErr := grpcSrv.Serve(lis); serveErr != nil {
			slog.Warn("in-process pipeline control gRPC stopped", "error", serveErr)
		}
	}()
	stop := func() {
		grpcSrv.Stop()
		_ = lis.Close()
	}
	return stop, nil
}

// simWorkDir 返回模拟器的工作目录：优先 SIM_WORKDIR，否则用系统临时目录下的固定路径，
// 以保证多次运行时 webui 指向同一份数据。
func simWorkDir() string {
	if d := os.Getenv("SIM_WORKDIR"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "gametrace-sim")
}

// loadTokensFromEnv 从环境变量或 .env 读取 GT_AUTH_TOKENS（owner=token 形式），
// 返回（首个 owner, 原始 spec）。spec 原样传给 gt-mcp，使 webui 用同一身份鉴权。
//
// 注意：go test 的工作目录是包目录（cmd/gt-pipeline/），而 .env 在仓库根；
// 因此这里用 findRepoRoot() 定位根目录再读 .env，否则读不到（曾经因此落到匿名会话）。
func loadTokensFromEnv() (owner, spec string) {
	spec = strings.TrimSpace(os.Getenv("GT_AUTH_TOKENS"))
	if spec == "" {
		if root, err := findRepoRoot(); err == nil {
			if b, err := os.ReadFile(filepath.Join(root, ".env")); err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "GT_AUTH_TOKENS=") {
						spec = strings.Trim(strings.TrimPrefix(line, "GT_AUTH_TOKENS="), `"`)
					}
				}
			}
		}
	}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", ""
	}
	seg := strings.SplitN(spec, ",", 2)[0]
	if eq := strings.IndexByte(seg, '='); eq >= 0 {
		owner = strings.TrimSpace(seg[:eq])
	}
	return owner, spec
}

// resolveMCPBin 构建一份与当前源码一致的 gt-mcp 二进制。
//
// 刻意不复用仓库根目录下可能存在的 gt-mcp[.exe] 预编译产物——那份二进制可能早于
// 当前代码（例如早于「状态变更三视图」那次提交），用它起 webui 会缺少新 tool
// （query_state_changes 等），验证不到最新功能。这里始终从源码现场构建到
// bin/gt-mcp-sim[.exe]，保证 webui 与当前代码一致。依赖已在 module cache，离线即可。
func resolveMCPBin() (string, error) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	built := filepath.Join(repoRoot, "bin", "gt-mcp-sim.exe")
	if runtime.GOOS != "windows" {
		built = filepath.Join(repoRoot, "bin", "gt-mcp-sim")
	}
	cmd := exec.Command("go", "build", "-o", built, "./cmd/gt-mcp")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("构建 gt-mcp 失败: %w\n%s", err, out)
	}
	return built, nil
}

// serveMCP 拉起真实 gt-mcp 子进程（与模拟器共用同一个 work-dir，并拨通同一个
// in-process pipeline 控制面），把数据喂给 webui。阻塞直到子进程退出（用户 Ctrl-C）。
func serveMCP(workDir, tokens, sessionID, pipelineAddr string) {
	bin, err := resolveMCPBin()
	if err != nil {
		fmt.Printf("无法启动 gt-mcp：%v\n可手动运行：go build -o gt-mcp.exe ./cmd/gt-mcp && gt-mcp.exe -work-dir %s\n", err, workDir)
		return
	}
	args := []string{"-work-dir", workDir, "-addr", "127.0.0.1:8781", "-pipeline-addr", pipelineAddr}
	cmd := exec.Command(bin, args...)
	if tokens != "" {
		cmd.Env = append(os.Environ(), "GT_AUTH_TOKENS="+tokens)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Printf("启动 gt-mcp 失败: %v\n", err)
		return
	}
	fmt.Println("========================================================")
	fmt.Printf("WebUI 已启动：http://127.0.0.1:8781/   (session_id=%s)\n", sessionID)
	if tokens != "" {
		fmt.Println("提示：webui 已开启鉴权，用 .env 中的 token 登录后即可看到本次模拟会话。")
	} else {
		fmt.Println("提示：未检测到 GT_AUTH_TOKENS，gt-mcp 以匿名模式运行，打开即见数据。")
	}
	fmt.Println("按 Ctrl-C 停止。")
	fmt.Println("========================================================")
	_ = cmd.Wait()
}

// findRepoRoot 从当前目录向上找包含 go.mod 的目录。
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 go.mod（请从仓库内运行）")
		}
		dir = parent
	}
}
