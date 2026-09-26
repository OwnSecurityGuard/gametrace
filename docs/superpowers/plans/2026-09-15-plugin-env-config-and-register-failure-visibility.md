# 插件 .env 配置工具 + 解码器注册失败可见性 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `get_plugin_env` MCP 工具一次返回解码插件 `.env` 全部 4 项配置（含调用者自己的 token，零手填），并让「平台拨号插件地址失败」的注册失败在插件控制台、前端 toast、插件面板、AI `status_plugin` 四处可见。

**Architecture:** 失败信息源头在 `pkg/plugin` 的 `Register`（拨号验证处）：增强错误文本 + 内存 ring buffer（容量 20 / TTL 15min / 同 key 5min 去重）+ `register_failed` 事件，经 internalipc proto → capturecontrol → gt-pcp SSE（owner 过滤）到前端；ring buffer 随 `ListPlugins` RPC 顺带返回供面板与 `status_plugin` 查询。`get_plugin_env` 复用 `advertisedAddrs`（地址）与 OAuth 的 ownerToken 反查逻辑（token）。

**Tech Stack:** Go（gRPC/protobuf/SSE）、protoc v3.21.4 + protoc-gen-go v1.36.11 + protoc-gen-go-grpc 1.6.2（已装）、React 19 + TanStack Query + vitest。

**Spec:** `docs/superpowers/specs/2026-09-15-plugin-env-config-and-register-failure-visibility-design.md`

**约束（执行前必读）：**
- 平台必须以 Docker 运行验证（`docker compose build && docker compose up`），禁止 `go run` 起平台（spec §6）。
- 前端改动最终经 `//go:embed` 打进二进制，验证需重建镜像。
- 每个 Task 结束跑一次该包测试并 commit；全部完成后跑 Task 9 的全量验证。

---

### Task 1: pkg/plugin — 注册失败捕获（ring buffer + 事件 + 诊断建议）

**Files:**
- Modify: `pkg/plugin/manager.go`（PluginEvent 类型/结构、RegistryServer 结构与构造、Register 拨号失败分支、新增 helper 与查询方法）
- Test: `pkg/plugin/manager_register_failure_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `pkg/plugin/manager_register_failure_test.go`：

```go
package plugin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gametrace/pkg/auth"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// failManifest 是合法最小 manifest（与 manager_test.go 的 maniA 同构）。
const failManifest = "api_version: gt.decoder/v2\nname: fail-decoder\nprotocol: tcp\ntype: decoder\n"

func registerFail(s *RegistryServer, ctx context.Context, socketPath string) error {
	_, err := s.Register(ctx, &pb.RegisterRequest{SocketPath: socketPath, Manifest: []byte(failManifest)})
	return err
}

func TestRegisterDialFailureRecordedAndEmitted(t *testing.T) {
	s := NewRegistryServer(10)
	events, unsub := s.Subscribe()
	defer unsub()

	err := registerFail(s, context.Background(), "unix:/nonexistent/fail.sock")
	if err == nil {
		t.Fatal("Register should fail when decoder socket is unreachable")
	}
	if !strings.Contains(err.Error(), "dial plugin socket") {
		t.Errorf("error should mention dial plugin socket: %v", err)
	}

	fs := s.ListRegisterFailures()
	if len(fs) != 1 {
		t.Fatalf("want 1 failure, got %d", len(fs))
	}
	if fs[0].Name != "fail-decoder" || fs[0].SocketPath != "unix:/nonexistent/fail.sock" || fs[0].Error == "" {
		t.Errorf("failure record mismatch: %+v", fs[0])
	}

	select {
	case ev := <-events:
		if ev.Type != PluginEventRegisterFailed || ev.Name != "fail-decoder" {
			t.Errorf("unexpected event: %+v", ev)
		}
		if ev.SocketPath != "unix:/nonexistent/fail.sock" || ev.Error == "" {
			t.Errorf("event missing dial details: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("register_failed event not emitted")
	}
}

func TestRegisterFailureDedupAndKeyChange(t *testing.T) {
	s := NewRegistryServer(10)

	_ = registerFail(s, context.Background(), "unix:/nonexistent/a.sock")
	_ = registerFail(s, context.Background(), "unix:/nonexistent/a.sock")
	if got := len(s.ListRegisterFailures()); got != 1 {
		t.Errorf("identical failure within dedup window should be suppressed, got %d", got)
	}

	// key 变化（用户改了 .env 重启）立即记录
	_ = registerFail(s, context.Background(), "unix:/nonexistent/b.sock")
	if got := len(s.ListRegisterFailures()); got != 2 {
		t.Errorf("changed key should record immediately, got %d", got)
	}
}

func TestRegisterFailureTTL(t *testing.T) {
	oldTTL, oldDedup := failureTTL, failureDedupWindow
	failureTTL = 20 * time.Millisecond
	failureDedupWindow = time.Hour // 防去重干扰 TTL 断言
	defer func() { failureTTL, failureDedupWindow = oldTTL, oldDedup }()

	s := NewRegistryServer(10)
	_ = registerFail(s, context.Background(), "unix:/nonexistent/ttl.sock")
	time.Sleep(30 * time.Millisecond)
	if got := len(s.ListRegisterFailures()); got != 0 {
		t.Errorf("expired failure should be dropped, got %d", got)
	}
}

func TestRegisterFailureCapacity(t *testing.T) {
	s := NewRegistryServer(10)
	for i := 0; i < maxRecentFailures+5; i++ {
		_ = registerFail(s, context.Background(), fmt.Sprintf("unix:/nonexistent/cap-%d.sock", i))
	}
	if got := len(s.ListRegisterFailures()); got != maxRecentFailures {
		t.Errorf("ring buffer should cap at %d, got %d", maxRecentFailures, got)
	}
}

func TestRegisterFailureOwnerRecorded(t *testing.T) {
	s := NewRegistryServer(10)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"})
	if err := registerFail(s, ctx, "unix:/nonexistent/own.sock"); err == nil {
		t.Fatal("register should fail")
	}
	fs := s.ListRegisterFailures()
	if len(fs) != 1 || fs[0].Owner != "bob" {
		t.Fatalf("failure owner not recorded: %+v", fs)
	}
}
```

- [ ] **Step 2: 跑测试确认编译失败**

Run: `go test ./pkg/plugin/ -run TestRegisterFailure -v`
Expected: FAIL（`PluginEventRegisterFailed`、`failureTTL`、`ListRegisterFailures`、`SocketPath` 等未定义，编译错误）

- [ ] **Step 3: 实现**

修改 `pkg/plugin/manager.go`：

(a) 事件类型常量（L62-71 的 const 块内，`PluginEventOffline` 后追加）：

```go
	// PluginEventRegisterFailed 注册失败（平台拨号插件 Decode 地址不通等）。
	// 失败的注册不进注册表；事件携带 SocketPath/Error/Owner 诊断字段。
	PluginEventRegisterFailed PluginEventType = "register_failed"
```

(b) `PluginEvent` 结构体（L74-80）替换为：

```go
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
```

(c) 在 `PluginEvent` 结构体后新增类型与参数：

```go
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
)
```

(d) `RegistryServer` 结构体（`tunnelAwaiting` 字段后，L103 与 L105 之间）追加：

```go
	// 注册失败 ring buffer（register_failed 事件源数据）：容量 20、TTL 15min、
	// name+socket_path+error 同 key 5min 去重。
	failMu       sync.Mutex
	failures     []RegisterFailure
	failLastSeen map[string]time.Time
```

(e) `NewRegistryServer`（L118-125 的字面量）追加一行初始化：

```go
		failLastSeen:  map[string]time.Time{},
```

(f) `Register` 的拨号失败分支（L243-249）替换为：

```go
	if !tunnel {
		conn, err = dialDecoder(ctx, req.SocketPath)
		if err != nil {
			// 诊断建议随错误返回给 SDK（插件控制台可见），同时入账 ring buffer
			// 并发 register_failed 事件（前端 toast / 插件面板 / status_plugin）。
			hint := dialFailureHint(ctx, req.SocketPath, err)
			s.recordFailure(m, req.SocketPath, hint, owner)
			return nil, fmt.Errorf("dial plugin socket: %w (%s)", err, hint)
		}
		client = pb.NewDecoderClient(conn)
	}
```

(g) 文件尾部（`hostIPv4` 等函数之后，或 `Register` 附近的合适位置）新增：

```go
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
```

(h) imports 增加 `"google.golang.org/grpc/peer"`（其余 `fmt`/`net`/`strings`/`sync`/`time` 已有）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/plugin/ -v`
Expected: 全部 PASS（含既有测试——`manager_test.go` 中 `SocketPath: "/nonexistent.sock"` 的既有用例不受影响，错误文本仍含 `dial plugin socket`）。

- [ ] **Step 5: Commit**

```bash
git add pkg/plugin/manager.go pkg/plugin/manager_register_failure_test.go
git commit -m "feat(plugin): 注册失败捕获——register_failed 事件、拨号诊断建议与失败 ring buffer"
```

---

### Task 2: internal.proto 扩展 + protoc 重新生成

**Files:**
- Modify: `pkg/internalipc/proto/internal.proto`
- Regen: `pkg/internalipc/proto/internal.pb.go`、`internal_grpc.pb.go`

- [ ] **Step 1: 编辑 proto**

(a) `PluginEvent`（L408-416）替换为：

```proto
// PluginEvent 插件注册表状态变化通知。
// type 取值：register | deregister | online | offline | register_failed。
message PluginEvent {
  string type = 1;             // register | deregister | online | offline | register_failed
  string instance_id = 2;      // 插件实例 ID
  string name = 3;             // 插件名（manifest.name）
  bool online = 4;            // 事件后是否在线
  int64 timestamp_unix = 5;   // 事件发生时间（Unix 秒）
  string socket_path = 6;     // register_failed：注册时上报、拨号失败的插件地址
  string error = 7;           // register_failed：拨号错误与诊断建议
  string owner = 8;           // 事件归属（register_failed 订阅侧按 owner 过滤）
}
```

(b) `ListPluginsResponse`（L359-361）替换为：

```proto
message ListPluginsResponse {
  repeated PluginSummary plugins = 1;
  // 最近注册失败记录（内存 ring buffer，15min TTL，owner 作用域同 plugins）。
  repeated PluginFailure recent_failures = 2;
}

// PluginFailure 最近的解码器注册失败记录（dial 插件地址不通等）。
message PluginFailure {
  string name = 1;
  string socket_path = 2;    // 注册时上报的地址
  string error = 3;          // 拨号错误与诊断建议
  string owner = 4;          // 注册方属主（空串 = 匿名/系统）
  int64 timestamp_unix = 5;
}
```

- [ ] **Step 2: 重新生成 pb（在仓库根目录 e:\ai_workspace\gta 执行）**

Run: `protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative pkg/internalipc/proto/internal.proto`
Expected: 无输出；`git diff --stat` 显示 `internal.pb.go` 变更（新增 `SocketPath`/`Error`/`Owner`/`RecentFailures`/`PluginFailure`），`internal_grpc.pb.go` 通常无实质变更（无新 RPC）。

- [ ] **Step 3: 编译验证**

Run: `go build ./...`
Expected: PASS（此时新字段尚无消费方，仅生成代码）。

- [ ] **Step 4: Commit**

```bash
git add pkg/internalipc/proto/
git commit -m "feat(proto): PluginEvent 与 ListPlugins 扩展注册失败字段"
```

---

### Task 3: capturecontrol — Engine 接口 + WatchPlugins/ListPlugins 透传

**Files:**
- Modify: `pkg/internalipc/capturecontrol/server.go`（Engine 接口、PluginEvent 结构、新 RegisterFailure 结构、WatchPlugins、ListPlugins handler）
- Modify: `pkg/internalipc/capturecontrol/server_test.go`（fakeEngine 桩 + 新测试）
- Modify: `pkg/internalipc/e2e_test.go`（fakeCaptureEngine 桩）

- [ ] **Step 1: 写失败测试**

在 `pkg/internalipc/capturecontrol/server_test.go` 末尾追加（fakeEngine 增加字段与桩见 Step 3）：

```go
func TestListPluginsIncludesRegisterFailures(t *testing.T) {
	ts := time.Now()
	s := NewServer(&fakeEngine{failures: []RegisterFailure{{
		Name: "my-plug", SocketPath: "127.0.0.1:61887", Error: "connection refused", Owner: "alice", Timestamp: ts,
	}}})
	resp, err := s.ListPlugins(context.Background(), &pb.ListPluginsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	fs := resp.GetRecentFailures()
	if len(fs) != 1 {
		t.Fatalf("want 1 recent failure, got %d", len(fs))
	}
	if fs[0].GetName() != "my-plug" || fs[0].GetSocketPath() != "127.0.0.1:61887" ||
		fs[0].GetError() != "connection refused" || fs[0].GetOwner() != "alice" ||
		fs[0].GetTimestampUnix() != ts.Unix() {
		t.Errorf("failure mapping mismatch: %+v", fs[0])
	}
}
```

并在 `server_test.go` 的 import 块加 `"time"`。

- [ ] **Step 2: 跑测试确认编译失败**

Run: `go test ./pkg/internalipc/capturecontrol/ -run TestListPluginsIncludesRegisterFailures -v`
Expected: FAIL（`RegisterFailure` 未定义、fakeEngine 缺 `failures` 字段）

- [ ] **Step 3: 实现**

修改 `pkg/internalipc/capturecontrol/server.go`：

(a) Engine 接口（`ListPlugins` 声明 L34-35 后）追加：

```go
	// ListRegisterFailures 列出最近的解码器注册失败记录（owner 作用域同 ListPlugins）。
	ListRegisterFailures(ctx context.Context) ([]RegisterFailure, error)
```

(b) `PluginEvent` 结构体（L157 起）在 `Timestamp` 字段后追加：

```go
	// register_failed 专用字段（其余事件为零值）。
	SocketPath string
	Error      string
	Owner      string
```

(c) `PluginEvent` 结构体后新增：

```go
// RegisterFailure 是最近一次注册失败的诊断记录（与 proto PluginFailure 对应）。
type RegisterFailure struct {
	Name       string
	SocketPath string
	Error      string
	Owner      string
	Timestamp  time.Time
}
```

(d) `WatchPlugins`（L615-632）的 `stream.Send` 替换为：

```go
		for ev := range ch {
			if err := stream.Send(&pb.PluginEvent{
				Type:          ev.Type,
				InstanceId:    ev.InstanceID,
				Name:          ev.Name,
				Online:        ev.Online,
				TimestampUnix: ev.Timestamp.Unix(),
				SocketPath:    ev.SocketPath,
				Error:         ev.Error,
				Owner:         ev.Owner,
			}); err != nil {
				return err
			}
		}
```

(e) `ListPlugins` handler（L532-552）的 return 前追加失败映射，替换整个函数尾部：

```go
	failures, err := s.engine.ListRegisterFailures(withRequestOwner(ctx, req.GetOwner(), req.GetAllOwners()))
	if err != nil {
		return nil, err
	}
	fails := make([]*pb.PluginFailure, 0, len(failures))
	for _, f := range failures {
		fails = append(fails, &pb.PluginFailure{
			Name:          f.Name,
			SocketPath:    f.SocketPath,
			Error:         f.Error,
			Owner:         f.Owner,
			TimestampUnix: f.Timestamp.Unix(),
		})
	}
	return &pb.ListPluginsResponse{Plugins: out, RecentFailures: fails}, nil
```

(f) `server_test.go` 的 `fakeEngine` 结构体加字段 `failures []RegisterFailure`，并加桩：

```go
func (f *fakeEngine) ListRegisterFailures(ctx context.Context) ([]RegisterFailure, error) {
	return f.failures, nil
}
```

(g) `pkg/internalipc/e2e_test.go` 的 `fakeCaptureEngine` 加桩（返回空）：

```go
func (f *fakeCaptureEngine) ListRegisterFailures(ctx context.Context) ([]capturecontrol.RegisterFailure, error) {
	return nil, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/internalipc/... -v`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add pkg/internalipc/
git commit -m "feat(capturecontrol): 透传注册失败事件与列表查询"
```

---

### Task 4: gt-pipeline — pipeline_service 适配 + owner 作用域

**Files:**
- Modify: `cmd/gt-pipeline/pipeline_service.go`（SubscribePlugins 透传新字段、新增 ListRegisterFailures）
- Test: `cmd/gt-pipeline/register_failure_scope_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `cmd/gt-pipeline/register_failure_scope_test.go`：

```go
package main

import (
	"context"
	"testing"

	"gametrace/pkg/auth"
	"gametrace/pkg/plugin"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

const scopeManifest = "api_version: gt.decoder/v2\nname: scope-decoder\nprotocol: tcp\ntype: decoder\n"

func failRegister(s *pipelineService, ctx context.Context, socketPath string) {
	if _, err := s.registry.Register(ctx, &pb.RegisterRequest{
		SocketPath: socketPath, Manifest: []byte(scopeManifest),
	}); err == nil {
		panic("register should fail: " + socketPath)
	}
}

// owner 作用域与 ListPlugins 一致：非 admin 见匿名+自己的；admin 全见。
func TestListRegisterFailuresOwnerScope(t *testing.T) {
	s := &pipelineService{registry: plugin.NewRegistryServer(10)}
	failRegister(s, auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"}),
		"unix:/nonexistent/bob.sock")
	failRegister(s, context.Background(), "unix:/nonexistent/anon.sock")

	// alice 非 admin：只见匿名
	got, err := s.ListRegisterFailures(auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "alice"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Owner != "" {
		t.Fatalf("alice should see only anonymous failure, got %+v", got)
	}

	// bob：自己的 + 匿名
	got, err = s.ListRegisterFailures(auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("bob should see own+anonymous failures, got %d", len(got))
	}

	// admin：全部
	got, err = s.ListRegisterFailures(auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "root", IsAdmin: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("admin should see all failures, got %d", len(got))
	}
}
```

- [ ] **Step 2: 跑测试确认编译失败**

Run: `go test ./cmd/gt-pipeline/ -run TestListRegisterFailuresOwnerScope -v`
Expected: FAIL（`s.ListRegisterFailures` 未定义）

- [ ] **Step 3: 实现**

修改 `cmd/gt-pipeline/pipeline_service.go`：

(a) `SubscribePlugins`（L533-561）中 `out <- capturecontrol.PluginEvent{...}` 替换为：

```go
				out <- capturecontrol.PluginEvent{
					Type:       string(ev.Type),
					InstanceID: ev.InstanceID,
					Name:       ev.Name,
					Online:     ev.Online,
					Timestamp:  ev.Timestamp,
					SocketPath: ev.SocketPath,
					Error:      ev.Error,
					Owner:      ev.Owner,
				}
```

(b) `SubscribePlugins` 函数后新增：

```go
// ListRegisterFailures 列出最近的解码器注册失败（owner 作用域同 ListPlugins：
// 非 admin 只见自己的 + 匿名，admin 见全部）。
func (s *pipelineService) ListRegisterFailures(ctx context.Context) ([]capturecontrol.RegisterFailure, error) {
	if s.registry == nil {
		return nil, fmt.Errorf("registry not available")
	}
	owner := auth.OwnerFrom(ctx)
	allOwners := false
	if p, ok := auth.PrincipalFrom(ctx); ok {
		allOwners = p.IsAdmin
	}
	out := make([]capturecontrol.RegisterFailure, 0)
	for _, f := range s.registry.ListRegisterFailures() {
		if !allOwners {
			if owner == "" {
				if f.Owner != "" {
					continue // 匿名调用方只见匿名失败
				}
			} else if f.Owner != "" && f.Owner != owner {
				continue // 其他 owner 的失败不可见
			}
		}
		out = append(out, capturecontrol.RegisterFailure{
			Name:       f.Name,
			SocketPath: f.SocketPath,
			Error:      f.Error,
			Owner:      f.Owner,
			Timestamp:  f.Timestamp,
		})
	}
	return out, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/gt-pipeline/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add cmd/gt-pipeline/pipeline_service.go cmd/gt-pipeline/register_failure_scope_test.go
git commit -m "feat(pipeline): 注册失败 owner 作用域查询与事件字段透传"
```

---

### Task 5: gt-mcp — get_plugin_env 工具

**Files:**
- Modify: `cmd/gt-mcp/dev_tools.go`（handler + callerToken + buildPluginEnvFile）
- Modify: `cmd/gt-mcp/main.go`（工具注册，`get_registry_addr` 后 L2513-2516 附近）
- Modify: `cmd/gt-mcp/capabilities.go`（plugin-runtime 工具组）
- Test: `cmd/gt-mcp/plugin_env_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `cmd/gt-mcp/plugin_env_test.go`：

```go
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"google.golang.org/grpc"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
)

func (f *fakeCaptureClient) GetRegistryAddr(ctx context.Context, in *pb.GetRegistryAddrRequest, _ ...grpc.CallOption) (*pb.GetRegistryAddrResponse, error) {
	return &pb.GetRegistryAddrResponse{RegistryAddr: ":9091"}, nil
}

func envResultText(t *testing.T, m *mcpCapture, ctx context.Context, args map[string]any) string {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := m.handleGetPluginEnv(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(mcp.TextContent).Text
}

func TestGetPluginEnvAuthenticatedCaller(t *testing.T) {
	t.Setenv("GT_PUBLIC_HOST", "192.168.31.87")
	fc := &fakeCaptureClient{dbDir: t.TempDir()}
	m := &mcpCapture{pipelineClient: fc, tokensByOwner: map[string]string{"alice": "gt_tok_alice"}}

	text := envResultText(t, m, auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "alice"}), map[string]any{})
	for _, want := range []string{
		`"registry_addr":"192.168.31.87:9091"`,
		`"auth_token":"gt_tok_alice"`,
		`"token_source":"env"`,
		`"decoder_addr":"0.0.0.0:61887"`,
		`"decoder_public_addr":"192.168.31.87:61887"`,
		"GT_REGISTRY_ADDR=192.168.31.87:9091",
		"GT_AUTH_TOKEN=gt_tok_alice",
		"GT_DECODER_ADDR=0.0.0.0:61887",
		"GT_DECODER_PUBLIC_ADDR=192.168.31.87:61887",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %s: %s", want, text)
		}
	}
}

func TestGetPluginEnvAnonymous(t *testing.T) {
	t.Setenv("GT_PUBLIC_HOST", "192.168.31.87")
	fc := &fakeCaptureClient{dbDir: t.TempDir()}
	m := &mcpCapture{pipelineClient: fc}

	text := envResultText(t, m, context.Background(), map[string]any{})
	if !strings.Contains(text, `"token_source":"anonymous"`) || !strings.Contains(text, `"auth_token":""`) {
		t.Errorf("anonymous caller should get empty token: %s", text)
	}
}

func TestGetPluginEnvDecoderPortOverride(t *testing.T) {
	t.Setenv("GT_PUBLIC_HOST", "192.168.31.87")
	fc := &fakeCaptureClient{dbDir: t.TempDir()}
	m := &mcpCapture{pipelineClient: fc}

	text := envResultText(t, m, context.Background(), map[string]any{"decoder_port": 61882})
	if !strings.Contains(text, `"decoder_addr":"0.0.0.0:61882"`) ||
		!strings.Contains(text, `"decoder_public_addr":"192.168.31.87:61882"`) {
		t.Errorf("decoder_port override not applied: %s", text)
	}
}

func TestGetPluginEnvHostOverride(t *testing.T) {
	// 不设 GT_PUBLIC_HOST：host 参数（跨机部署时插件所在机器视角）生效
	fc := &fakeCaptureClient{dbDir: t.TempDir()}
	m := &mcpCapture{pipelineClient: fc}

	text := envResultText(t, m, context.Background(), map[string]any{"host": "10.0.0.9"})
	if !strings.Contains(text, `"registry_addr":"10.0.0.9:9091"`) ||
		!strings.Contains(text, `"decoder_public_addr":"10.0.0.9:61887"`) {
		t.Errorf("host override not applied: %s", text)
	}
}
```

- [ ] **Step 2: 跑测试确认编译失败**

Run: `go test ./cmd/gt-mcp/ -run TestGetPluginEnv -v`
Expected: FAIL（`handleGetPluginEnv` 未定义）

- [ ] **Step 3: 实现**

(a) `cmd/gt-mcp/dev_tools.go` 在 `handleGetRegistryAddr`（L198-227）后新增：

```go
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

	port := 61887
	if p := req.GetInt("decoder_port", 0); p > 0 && p < 65536 {
		port = p
	}
	regHost, _, err := net.SplitHostPort(registry)
	if err != nil {
		return errorResult(fmt.Errorf("parse registry addr %q: %w", registry, err)), nil
	}
	decoderAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	publicAddr := net.JoinHostPort(regHost, strconv.Itoa(port))

	token, src := m.callerToken(ctx)

	return successResult(map[string]any{
		"registry_addr":       registry,
		"auth_token":          token,
		"token_source":        src,
		"decoder_addr":        decoderAddr,
		"decoder_public_addr": publicAddr,
		"env_file":            buildPluginEnvFile(registry, token, decoderAddr, publicAddr),
		"notes": []string{
			"GT_REGISTRY_ADDR: 插件注册端点，原样使用",
			"GT_AUTH_TOKEN: 调用者自己的注册 token（agent 托管 GT_TUNNEL=1 时平台自动注入，可留空）",
			"GT_DECODER_ADDR: 插件本地监听地址，0.0.0.0 保证平台可回拨",
			"GT_DECODER_PUBLIC_ADDR: 平台回拨地址；填错时平台连不上解码器，注册失败",
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

// buildPluginEnvFile 生成可直接写入 .env 的文本（含注释）。
func buildPluginEnvFile(registryAddr, token, decoderAddr, publicAddr string) string {
	var b strings.Builder
	b.WriteString("# 由 gametrace get_plugin_env 生成 —— 复制为 .env，无需手填\n")
	b.WriteString("# 同名环境变量优先于本文件；不要把 .env 提交到 git\n")
	fmt.Fprintf(&b, "GT_REGISTRY_ADDR=%s\n", registryAddr)
	fmt.Fprintf(&b, "GT_AUTH_TOKEN=%s\n", token)
	fmt.Fprintf(&b, "GT_DECODER_ADDR=%s\n", decoderAddr)
	fmt.Fprintf(&b, "GT_DECODER_PUBLIC_ADDR=%s\n", publicAddr)
	return b.String()
}
```

(b) `dev_tools.go` imports 增加 `"net"`、`"strconv"`、`"gametrace/pkg/auth"`（现有：context/json/fmt/os/strings/time/mcp/pb/plugindevpb）。

(c) `cmd/gt-mcp/main.go` 在 `get_registry_addr` 注册（L2513-2516）后追加：

```go
	s.AddTool(mcp.NewTool("get_plugin_env",
		mcp.WithDescription("Return the complete .env for a decoder plugin: GT_REGISTRY_ADDR/GT_AUTH_TOKEN/GT_DECODER_ADDR/GT_DECODER_PUBLIC_ADDR plus a ready-to-write env_file block. The token is the caller's own registration token (anonymous mode returns empty). Scaffold a plugin, call this once, write env_file to .env — zero manual fill."),
		mcp.WithString("host", mcp.Description("Explicit externally reachable host, same semantics as get_registry_addr (used when the plugin runs on a different machine than this caller)")),
		mcp.WithNumber("decoder_port", mcp.Description("Decoder listen port, default 61887")),
	), capture.handleGetPluginEnv)
```

(d) `cmd/gt-mcp/capabilities.go` plugin-runtime 工具组（L69-73）的 Tools 加入 `"get_plugin_env"`：

```go
				Tools: []string{
					"list_plugins", "list_registered_plugins",
					"get_plugin_manifest", "deregister_plugin", "get_registry_addr",
					"get_plugin_env",
				},
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/gt-mcp/ -run TestGetPluginEnv -v`
Expected: 4 个测试 PASS。

- [ ] **Step 5: Commit**

```bash
git add cmd/gt-mcp/dev_tools.go cmd/gt-mcp/main.go cmd/gt-mcp/capabilities.go cmd/gt-mcp/plugin_env_test.go
git commit -m "feat(mcp): 新增 get_plugin_env 一次返回插件 .env 全量配置（含调用者 token）"
```

---

### Task 6: gt-mcp — 事件通路、SSE owner 过滤、list_registered_plugins 与 status_plugin 并入失败

**Files:**
- Modify: `cmd/gt-mcp/main.go`（pluginEventJSON、事件常量、startPluginEventWatcher、handleEventsSSE + visibleToSubscriber、handleListRegisteredPlugins）
- Modify: `cmd/gt-mcp/dev_tools.go`（handleStatusPlugin + latestRegisterFailure）
- Modify: `cmd/gt-mcp/verify_tools_test.go`（fakeCaptureClient 加 recentFailures 字段）
- Modify: `cmd/gt-mcp/t13_test.go`（fake ListPlugins 返回 RecentFailures）
- Test: `cmd/gt-mcp/register_failure_surface_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `cmd/gt-mcp/register_failure_surface_test.go`：

```go
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
)

func TestRegisterFailedVisibility(t *testing.T) {
	alice := &auth.Principal{Owner: "alice"}
	admin := &auth.Principal{Owner: "root", IsAdmin: true}
	cases := []struct {
		name string
		ev   pluginEventJSON
		sub  *auth.Principal
		has  bool
		want bool
	}{
		{"匿名事件全员可见", pluginEventJSON{Owner: ""}, nil, false, true},
		{"同 owner 可见", pluginEventJSON{Owner: "alice"}, alice, true, true},
		{"他人 owner 不可见", pluginEventJSON{Owner: "bob"}, alice, true, false},
		{"admin 全可见", pluginEventJSON{Owner: "bob"}, admin, true, true},
		{"匿名订阅者不见他人失败", pluginEventJSON{Owner: "bob"}, nil, false, false},
	}
	for _, c := range cases {
		if got := visibleToSubscriber(c.ev, c.sub, c.has); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestListRegisteredPluginsIncludesFailures(t *testing.T) {
	fc := &fakeCaptureClient{dbDir: t.TempDir(), recentFailures: []*pb.PluginFailure{{
		Name: "my-plug", SocketPath: "127.0.0.1:61887", Error: "connection refused", Owner: "alice", TimestampUnix: 1700000000,
	}}}
	m := &mcpCapture{pipelineClient: fc}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := m.handleListRegisteredPlugins(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(mcp.TextContent).Text
	for _, want := range []string{"recent_register_failures", "my-plug", "127.0.0.1:61887", "connection refused"} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %s: %s", want, text)
		}
	}
}

func TestLatestRegisterFailure(t *testing.T) {
	fc := &fakeCaptureClient{dbDir: t.TempDir(), recentFailures: []*pb.PluginFailure{{
		Name: "my-plug", SocketPath: "127.0.0.1:61887", Error: "connection refused", Owner: "alice", TimestampUnix: 1700000000,
	}}}
	m := &mcpCapture{pipelineClient: fc}

	f := m.latestRegisterFailure(context.Background(), "my-plug")
	if f == nil || f["socket_path"] != "127.0.0.1:61887" || f["error"] != "connection refused" {
		t.Errorf("latestRegisterFailure mismatch: %+v", f)
	}
	if got := m.latestRegisterFailure(context.Background(), "other"); got != nil {
		t.Errorf("unmatched name should return nil, got %+v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认编译失败**

Run: `go test ./cmd/gt-mcp/ -run 'TestRegisterFailedVisibility|TestListRegisteredPluginsIncludesFailures|TestLatestRegisterFailure' -v`
Expected: FAIL（`visibleToSubscriber`、`latestRegisterFailure` 未定义；`recentFailures` 字段不存在）

- [ ] **Step 3: 实现**

(a) `cmd/gt-mcp/verify_tools_test.go` 的 `fakeCaptureClient` 结构体加字段（`manifestReq` 之后）：

```go
	// recentFailures 预设 ListPlugins 返回的注册失败记录。
	recentFailures []*pb.PluginFailure
```

(b) `cmd/gt-mcp/t13_test.go` 的 `ListPlugins` 桩替换为：

```go
func (f *fakeCaptureClient) ListPlugins(ctx context.Context, in *pb.ListPluginsRequest, _ ...grpc.CallOption) (*pb.ListPluginsResponse, error) {
	f.listPluginsReq = in
	return &pb.ListPluginsResponse{
		Plugins: []*pb.PluginSummary{
			{Name: "http", Online: true, Owner: "alice"},
		},
		RecentFailures: f.recentFailures,
	}, nil
}
```

(c) `cmd/gt-mcp/main.go` `pluginEventJSON`（L81-88）替换为：

```go
// pluginEventJSON 是插件事件 SSE 推送的 JSON 负载，与 proto PluginEvent 对应。
type pluginEventJSON struct {
	Type       string `json:"type"` // register | deregister | online | offline | register_failed
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
	Online     bool   `json:"online"`
	Timestamp  int64  `json:"timestamp_unix"`
	SocketPath string `json:"socket_path,omitempty"` // register_failed：尝试拨号的地址
	Error      string `json:"error,omitempty"`       // register_failed：拨号错误与诊断建议
	Owner      string `json:"owner,omitempty"`       // register_failed：订阅侧过滤用
}

// pluginEventTypeRegisterFailed 是 register_failed 的 type 值
// （对应 pkg/plugin.PluginEventRegisterFailed，此处用字面量避免跨包依赖）。
const pluginEventTypeRegisterFailed = "register_failed"
```

(d) `startPluginEventWatcher`（L2109-2115）的 broadcast 替换为：

```go
				m.broadcastPluginEvent(pluginEventJSON{
					Type:       ev.GetType(),
					InstanceID: ev.GetInstanceId(),
					Name:       ev.GetName(),
					Online:     ev.GetOnline(),
					Timestamp:  ev.GetTimestampUnix(),
					SocketPath: ev.GetSocketPath(),
					Error:      ev.GetError(),
					Owner:      ev.GetOwner(),
				})
```

(e) `handleEventsSSE`（L2124 起）：在 `ch, unsub := m.subscribeEvents()` 之前插入订阅者身份解析，在 `case ev := <-ch:` 分支 marshal 前插入过滤：

```go
	// 订阅者身份（auth.Middleware 注入；匿名模式下无 Principal）。
	sub, hasSub := auth.PrincipalFrom(r.Context())

	ch, unsub := m.subscribeEvents()
```

```go
		case ev := <-ch:
			// register_failed 按 owner 过滤（匿名事件与 admin 全见）；
			// 其余事件保持既有全员广播行为不变。
			if ev.Type == pluginEventTypeRegisterFailed && !visibleToSubscriber(ev, sub, hasSub) {
				continue
			}
			data, err := json.Marshal(ev)
```

并在 `handleEventsSSE` 后新增：

```go
// visibleToSubscriber 判断 register_failed 事件是否对当前 SSE 订阅者可见：
// 事件无 owner（匿名/系统）→ 全员；订阅者 admin → 全部；否则仅同 owner；
// 匿名订阅者（无 Principal）只见匿名事件。仅用于 register_failed，
// 其余事件类型不过滤（保持既有广播行为）。
func visibleToSubscriber(ev pluginEventJSON, sub *auth.Principal, hasSub bool) bool {
	if ev.Owner == "" {
		return true
	}
	if hasSub && sub != nil {
		if sub.IsAdmin {
			return true
		}
		return sub.Owner == ev.Owner
	}
	return false
}
```

(f) `handleListRegisteredPlugins`（L863-894）：循环后追加失败透传，并改 return：

```go
	var failures []map[string]any
	for _, f := range resp.GetRecentFailures() {
		failures = append(failures, map[string]any{
			"name":           f.GetName(),
			"socket_path":    f.GetSocketPath(),
			"error":          f.GetError(),
			"owner":          f.GetOwner(),
			"timestamp_unix": f.GetTimestampUnix(),
		})
	}
	return successResult(map[string]any{
		"plugins": plugins, "count": len(plugins),
		"recent_register_failures": failures,
	}), nil
```

（原 `return successResult(map[string]any{"plugins": plugins, "count": len(plugins)}), nil` 删除。）

(g) `cmd/gt-mcp/dev_tools.go` `handleStatusPlugin` 的 return 段（L290-302）替换为：

```go
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
	// 注册失败（Runtime Plane ring buffer）：命中同名失败时并入，
	// 指导修 .env 的 GT_DECODER_PUBLIC_ADDR（error 里含来源 IP 建议）。
	if f := m.latestRegisterFailure(ctx, name); f != nil {
		out["register_failure"] = f
		if runtimeState == "offline" {
			out["next_action"] = "注册失败：平台拨不通插件 Decode 地址（见 register_failure.error，含来源 IP 建议值）；修正 .env 的 GT_DECODER_PUBLIC_ADDR 后重新 activate_plugin"
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
				"socket_path":    f.GetSocketPath(),
				"error":          f.GetError(),
				"owner":          f.GetOwner(),
				"timestamp_unix": f.GetTimestampUnix(),
			}
		}
	}
	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/gt-mcp/ -v`
Expected: 全部 PASS（含既有 webui/authz 等测试）。

- [ ] **Step 5: Commit**

```bash
git add cmd/gt-mcp/main.go cmd/gt-mcp/dev_tools.go cmd/gt-mcp/verify_tools_test.go cmd/gt-mcp/t13_test.go cmd/gt-mcp/register_failure_surface_test.go
git commit -m "feat(mcp): 注册失败事件通路、SSE owner 过滤与 status_plugin 并入失败原因"
```

---

### Task 7: 前端 — toast + 插件面板「最近注册失败」栏

**Files:**
- Modify: `web/src/types/registered-plugin.ts`
- Modify: `web/src/hooks/use-mcp.ts`（usePluginEventStream 解析事件 + toast）
- Modify: `web/src/components/plugin-panel.tsx`（失败栏）

- [ ] **Step 1: 类型扩展**

`web/src/types/registered-plugin.ts` 的 `ListRegisteredPluginsResult` 前追加接口并改响应类型：

```ts
/** list_registered_plugins 返回的最近注册失败记录（后端 15min TTL 自动消失） */
export interface PluginRegisterFailure {
  name: string;
  socket_path: string;
  error: string;
  owner?: string;
  timestamp_unix: number;
}

/** list_registered_plugins 完整响应 */
export interface ListRegisteredPluginsResult {
  ok: boolean;
  plugins: RegisteredPlugin[];
  /** 最近的解码器注册失败（平台拨号插件地址不通等） */
  recent_register_failures?: PluginRegisterFailure[] | null;
}
```

- [ ] **Step 2: usePluginEventStream 解析事件 + toast**

`web/src/hooks/use-mcp.ts`：

(a) 顶部 import 区加：

```ts
import { toast } from "@/components/ui/toast";
```

(b) `usePluginEventStream`（L366-394）的事件监听替换为：

```ts
    es.addEventListener("plugin", (e) => {
      // register_failed：即时 toast（含尝试地址与诊断建议），其余事件维持缓存失效
      try {
        const ev = JSON.parse((e as MessageEvent<string>).data) as {
          type: string;
          name?: string;
          socket_path?: string;
          error?: string;
        };
        if (ev.type === "register_failed") {
          toast.error(
            `插件 ${ev.name ?? "未知"} 注册失败：平台拨不通 ${ev.socket_path ?? "解码器地址"}`,
            ev.error,
          );
        }
      } catch {
        // 非 JSON 负载：维持原行为（仅失效缓存）
      }
      void queryClient.invalidateQueries({ queryKey: ["registeredPlugins"] });
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
    });
```

- [ ] **Step 3: 插件面板「最近注册失败」栏**

`web/src/components/plugin-panel.tsx`：

(a) import 区的类型导入加 `PluginRegisterFailure`（与 `RegisteredPlugin` 同一行来源）：

```ts
import type { PluginRegisterFailure, RegisteredPlugin } from "@/types/registered-plugin";
```

(b) `EMPTY_PLUGINS` 常量（L32）后追加：

```ts
const EMPTY_FAILURES: PluginRegisterFailure[] = [];
```

(c) `const plugins = data?.plugins ?? EMPTY_PLUGINS;`（L78）后追加：

```ts
  // 最近注册失败（平台拨号插件地址不通）：后端 15min TTL，SSE 失效 + 5s 轮询双保障
  const failures = data?.recent_register_failures ?? EMPTY_FAILURES;
```

(d) 插件列表容器 `</div>`（L255，`plugins.map` 所在容器的闭合标签）之后、「测试插件」区块注释（L257）之前插入：

```tsx
      {failures.length > 0 && (
        <div className="border-t px-4 py-3 space-y-2">
          <div className="flex items-center gap-2 text-xs font-semibold text-destructive">
            <AlertTriangle className="h-3.5 w-3.5" />
            最近注册失败（15 分钟内）
          </div>
          {failures.map((f) => (
            <div
              key={`${f.name}-${f.timestamp_unix}`}
              className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-xs space-y-1"
            >
              <div className="font-medium">
                {f.name} — 平台拨不通 <code className="text-[11px]">{f.socket_path}</code>
              </div>
              <div className="text-destructive break-all">{f.error}</div>
              <div className="text-muted-foreground">{fmtTime(f.timestamp_unix)}</div>
            </div>
          ))}
        </div>
      )}
```

（`AlertTriangle` 与 `fmtTime` 已在该文件 import/定义。）

- [ ] **Step 4: 构建 + 前端测试验证**

Run（cwd=`web`）: `npm run build`
Expected: tsc + vite build 无错误。

Run（cwd=`web`）: `npm run test`
Expected: vitest 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add web/src/types/registered-plugin.ts web/src/hooks/use-mcp.ts web/src/components/plugin-panel.tsx
git commit -m "feat(web): 注册失败 toast 与插件面板最近失败栏"
```

---

### Task 8: SKILL.md 步骤 0 重写为 get_plugin_env 零手填

**Files:**
- Modify: `skills/decoder-plugin-guide/SKILL.md`（L34-70 步骤 0 与 0.1；L118-122 目录说明）

- [ ] **Step 1: 重写步骤 0（L34-52 整段替换）**

```markdown
### 0. 确认平台连接信息（第一步必做，别猜）

插件通过环境变量连接平台。**先与用户确认要连哪个平台、怎么连**，再写代码。4 个变量**全部自动获取**：调一次平台工具 `get_plugin_env`，把返回的 `env_file` 原样写入 `.env` 即可，无需任何手填。

| 变量 | 含义 | 获取方式 |
|---|---|---|
| `GT_REGISTRY_ADDR` | registry 端点（插件注册） | **自动**：`get_plugin_env` 的 `registry_addr` |
| `GT_AUTH_TOKEN` | 注册鉴权 Bearer token | **自动**：`get_plugin_env` 返回调用者自己的 token；agent 托管（`GT_TUNNEL=1`）下平台自动注入，可留空 |
| `GT_DECODER_ADDR` | 解码器本地监听地址 | **自动**：`get_plugin_env` 的 `decoder_addr`（默认 `0.0.0.0:61887`） |
| `GT_DECODER_PUBLIC_ADDR` | 注册时上报、宿主回拨的地址 | **自动**：`get_plugin_env` 的 `decoder_public_addr`（外部可达 host + 端口） |

确认步骤：

1. 问用户平台部署形态：**本机单机** / **Docker 局域网** / **远端公网**。
2. 调 `get_plugin_env`（跨机部署、插件与调用方不在同一台机器时，参数 `host` 传插件所在机器视角的可达主机；平台已配 `GT_PUBLIC_HOST` 的公网/Docker 部署可省略）→ 把返回的 `env_file` 原样写入插件目录 `.env`。匿名模式（平台未配 token）下 `auth_token` 为空属正常。
3. 写错了也不怕：注册失败时平台会记录诊断（含注册连接来源 IP 建议值），`status_plugin` 返回的 `register_failure` 字段可查，按其提示修正 `.env` 后重新 `activate_plugin`。
```

（原 L52 的引用块「平台侧已暴露 get_registry_addr……仅保留 token 一项由用户填写」整段删除。）

- [ ] **Step 2: 重写 0.1 的自动回填说明（L58-68 替换）**

```markdown
**零手填**：让用户（或引导 agent）调 `get_plugin_env`，把返回的 `env_file` 原样写入 `.env`：

```
# .env.example —— 复制为 .env。内容按「步骤 0」调 get_plugin_env 生成；
#               同名环境变量优先于本文件。
GT_REGISTRY_ADDR=<get_plugin_env 的 registry_addr>
GT_AUTH_TOKEN=<get_plugin_env 的 auth_token；agent 托管 GT_TUNNEL 下可留空>
GT_DECODER_ADDR=0.0.0.0:61887
GT_DECODER_PUBLIC_ADDR=<registry_addr 的 host 段>:61887
```
```

- [ ] **Step 3: 更新目录表（L122）**

```markdown
├── .env.example    # 连接配置模板：复制为 .env 后按「步骤 0」调 get_plugin_env 自动生成，零手填
```

- [ ] **Step 4: Commit**

```bash
git add skills/decoder-plugin-guide/SKILL.md
git commit -m "docs(skill): 步骤 0 改用 get_plugin_env 零手填生成 .env"
```

---

### Task 9: 全量构建与 Docker 端到端验证

- [ ] **Step 1: Go 全量构建与测试**

Run: `go build ./... && go test ./...`
Expected: 全部 PASS。

- [ ] **Step 2: Docker 重建并启动（平台唯一运行方式）**

Run: `docker compose build && docker compose up -d`
Expected: 镜像构建成功（4 平台 probe 正常产出；osxcross 失败仅 WARN 不中断——见项目约束）。

- [ ] **Step 3: 端到端验证（错误 GT_DECODER_PUBLIC_ADDR 场景）**

用现有示例插件（如 `examples/lp-decoder`）在宿主机以错误配置启动：

```
GT_REGISTRY_ADDR=127.0.0.1:19091
GT_AUTH_TOKEN=<真实 token>
GT_DECODER_ADDR=0.0.0.0:61882
GT_DECODER_PUBLIC_ADDR=127.0.0.1:61882   # 故意填错：Docker 容器内回拨 127.0.0.1 拨不到宿主机
```

Expected（四处验证）：
1. 插件控制台日志出现增强错误：`dial plugin socket ... connection refused (…; GT_DECODER_PUBLIC_ADDR/GT_DECODER_ADDR 指向回环地址：…)`
2. 前端 toast：`插件 xxx 注册失败：平台拨不通 127.0.0.1:61882`
3. 插件面板出现「最近注册失败（15 分钟内）」栏，含错误与时间；改为正确地址重启插件、注册成功后，失败记录 15 分钟内自动消失
4. `status_plugin` 返回 `register_failure`，`next_action` 提示修正 `.env`

- [ ] **Step 4: 端到端验证（get_plugin_env）**

Agent 带 token 调 `get_plugin_env` → 返回 4 项配置 + `env_file`；用 `env_file` 启动示例插件（正确 `GT_DECODER_PUBLIC_ADDR`）→ 注册成功、出现在插件面板。

- [ ] **Step 5: 收尾**

确认 `git status` 干净；如验证中修复了问题，追加 commit。

---

## Self-Review 记录

- **Spec coverage**：§3.1-3.4（工具+SKILL）→ Task 5/8；§4.1（捕获/ring buffer/去重/TTL）→ Task 1；§4.2（proto）→ Task 2；§4.3（通路+SSE 过滤）→ Task 3/4/6；§4.4（前端三处）→ Task 7；§4.5（status_plugin）→ Task 6；§6（测试+Docker 验证）→ 各 Task Step + Task 9。无遗漏。
- **Type consistency**：`RegisterFailure`（pkg/plugin）→ `capturecontrol.RegisterFailure` → `pb.PluginFailure` → 前端 `PluginRegisterFailure` 字段名一致（name/socket_path/error/owner/timestamp(_unix)）；事件常量 `register_failed` 三处（manager.go / pluginEventTypeRegisterFailed / proto 注释）一致。
- **Placeholder scan**：无 TBD/TODO；所有代码步骤给出完整代码。
