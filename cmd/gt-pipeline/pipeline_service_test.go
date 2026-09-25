package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/plugin"
	"gametrace/pkg/store"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newTestPipelineService 创建用于测试的 pipelineService，使用空插件目录（无 tcp 插件）。
// 返回 service、工作目录和 ControlStore。ControlStore 的 Close 通过 t.Cleanup 注册。
func newTestPipelineService(t *testing.T) (*pipelineService, string, *store.ControlStore) {
	t.Helper()
	workDir := t.TempDir()
	controlPath := filepath.Join(workDir, "control.sqlite")
	controlStore, err := store.NewControlStore(controlPath)
	if err != nil {
		t.Fatalf("NewControlStore: %v", err)
	}
	t.Cleanup(func() { _ = controlStore.Close() })
	mgr := plugin.NewRegistryServer(10)
	s := newPipelineService(workDir, controlStore, mgr, ":9091", "sqlite", "")
	// 租约端口段改用本机当前空闲的连续端口，而不是生产固定段
	// （12100/19100/19500）：开发机上 gt-agent 等常驻进程会占住那些段，
	// 假控制服务便 bind 失败——早期实现又用 `_ = srv.Serve(ctx)` 吞掉该错误，
	// 请求落到真实进程上，表现为极具误导性的 401 Unauthorized。
	// 分配真实空闲端口后，probeFreePortFn 的预探测也恢复真实行为：端口若被
	// 抢占会立刻显式报错，而不是被 no-op 屏蔽。
	s.agentPorts, s.grpcPorts, s.ctrlPorts = isolatedLeasePortRanges(t)
	return s, workDir, controlStore
}

// isolatedLeasePortRanges 探测一段本机空闲的连续端口并切成三等份，分别作为
// agent / mobile gRPC / agent 控制端口段（每段 8 个）。目的是让租约测试与
// 开发机上常驻的 GameTrace 服务（如占着 19500 的 gt-agent）解耦。
func isolatedLeasePortRanges(t *testing.T) (agent, grpc, ctrl *portRange) {
	t.Helper()
	const perRange = 8
	base := freeContiguousPortBase(t, perRange*3)
	return newPortRange(base, base+perRange-1),
		newPortRange(base+perRange, base+2*perRange-1),
		newPortRange(base+2*perRange, base+3*perRange-1)
}

// freeContiguousPortBase 找出一个 base，使 [base, base+n-1] 全部可 bind。
// 先用端口 0 让 OS 给个临时起点，再逐个校验连续性；不满足就重试。
func freeContiguousPortBase(t *testing.T, n int) int {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		base := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if base+n-1 > 65535 {
			continue
		}
		if portsBindable(base, n) {
			return base
		}
	}
	t.Fatalf("could not find %d contiguous free ports for lease tests", n)
	return 0
}

// portsBindable 报告 [base, base+n-1] 是否全部可绑定（探测后立即释放）。
// 租约的 agent 端口按 0.0.0.0 监听，但用 127.0.0.1 探测已足够保守：
// 回环端口被占时 0.0.0.0 也必然绑不上。
func portsBindable(base, n int) bool {
	lns := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range lns {
			_ = l.Close()
		}
	}()
	for p := base; p < base+n; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			return false
		}
		lns = append(lns, l)
	}
	return true
}

// fileStartSessionRequest 构造一个使用 file source 的 StartSessionRequest。
// 路径指向不存在的 pcap 文件——StartSession 不检查文件存在性。
func fileStartSessionRequest(workDir string) capturecontrol.StartSessionRequest {
	return capturecontrol.StartSessionRequest{
		Plugin: "tcp",
		Port:   8080,
		File:   &capturecontrol.FileConfig{Path: filepath.Join(workDir, "nonexistent.pcap")},
	}
}

// waitForTaskDone 等待指定 session 的 task run goroutine 退出（done channel 关闭）。
// 由于没有 tcp 插件，run 会在 mgr.Find("tcp") 失败后立即退出并 close(done)。
func waitForTaskDone(t *testing.T, s *pipelineService, sessionID string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		task, ok := s.getTask(sessionID)
		if !ok {
			return // task 已被 finalizeTask 移除
		}
		select {
		case <-task.done:
			return
		case <-deadline:
			t.Fatalf("timeout waiting for task %s to exit after %v", sessionID, timeout)
		}
	}
}

// TestPipelineService_StartStopLifecycle 验证 StartSession → GetStatus → StopSession 全流程。
func TestPipelineService_StartStopLifecycle(t *testing.T) {
	s, workDir, controlStore := newTestPipelineService(t)

	ctx := context.Background()
	req := fileStartSessionRequest(workDir)

	res, err := s.StartSession(ctx, req)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if res.SessionID == "" {
		t.Error("SessionID is empty")
	}
	if res.State != "running" {
		t.Errorf("State = %q, want %q", res.State, "running")
	}
	if res.DBPath == "" {
		t.Error("DBPath is empty")
	}
	if _, err := os.Stat(res.DBPath); err != nil {
		t.Errorf("capture.sqlite not created at %s: %v", res.DBPath, err)
	}

	// 验证 ControlStore 记录存在
	// 注意：由于无 tcp 插件，task 可能已退出并 finalize 为 stopped，不强制检查 status=="running"
	meta, err := controlStore.GetSession(ctx, res.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if meta.Plugin != "tcp" {
		t.Errorf("ControlStore plugin = %q, want %q", meta.Plugin, "tcp")
	}
	if meta.Port != 8080 {
		t.Errorf("ControlStore port = %d, want %d", meta.Port, 8080)
	}
	if meta.DBPath != res.DBPath {
		t.Errorf("ControlStore db_path = %q, want %q", meta.DBPath, res.DBPath)
	}

	// 短暂等待 run goroutine 失败退出（无 tcp 插件）
	waitForTaskDone(t, s, res.SessionID, 2*time.Second)

	// 再等一小段时间让 finalizeTask 完成（写 ControlStore + removeTask）
	time.Sleep(100 * time.Millisecond)

	// 验证 ControlStore 已更新为 stopped
	meta, err = controlStore.GetSession(ctx, res.SessionID)
	if err != nil {
		t.Fatalf("GetSession after stop: %v", err)
	}
	if meta.Status != "stopped" {
		t.Errorf("ControlStore status after stop = %q, want %q", meta.Status, "stopped")
	}
	if meta.StoppedAt == nil {
		t.Error("ControlStore stopped_at is nil after stop")
	}

	// 验证 task 已从 map 移除
	if _, ok := s.getTask(res.SessionID); ok {
		t.Error("task still in map after finalize")
	}
}

// TestPipelineService_FinalizeKeepsProjectBinding 回归：项目内会话结束后必须仍归属该项目。
//
// finalizeTask 曾用一个只填了统计的 SessionMeta 整行写回 sessions，把 project_id /
// tenant_id / extra / manifest_snapshot 一并清零——表现为「项目里抓的包，一停止就
// 掉进未归属抓包」。
func TestPipelineService_FinalizeKeepsProjectBinding(t *testing.T) {
	s, workDir, controlStore := newTestPipelineService(t)
	ctx := context.Background()

	req := fileStartSessionRequest(workDir)
	req.ProjectID = "proj-keep"
	req.Metadata = map[string]string{"source": "probe-archive"}

	res, err := s.StartSession(ctx, req)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// 无 tcp 插件，run 会立即退出并触发 finalizeTask（自动结束路径）。
	waitForTaskDone(t, s, res.SessionID, 2*time.Second)

	var meta *store.SessionMeta
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m, err := controlStore.GetSession(ctx, res.SessionID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if m.Status == "stopped" {
			meta = m
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if meta == nil {
		t.Fatal("session never finalized to stopped")
	}
	if meta.ProjectID != "proj-keep" {
		t.Errorf("project_id after finalize = %q, want proj-keep", meta.ProjectID)
	}
	if meta.Extra["source"] != "probe-archive" {
		t.Errorf("extra after finalize = %v, want source=probe-archive", meta.Extra)
	}
}

// TestPipelineService_StopNoActive 验证停止不存在的会话返回 ErrNoActiveCapture。
func TestPipelineService_StopNoActive(t *testing.T) {
	s, _, _ := newTestPipelineService(t)
	ctx := context.Background()
	_, err := s.StopSession(ctx, "any-id")
	if err == nil {
		t.Fatal("StopSession: expected error, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("StopSession error code = %v, want %v", status.Code(err), codes.FailedPrecondition)
	}
}

// TestPipelineService_GetStatusNotActive 验证查询不存在的会话返回 State="closed"。
func TestPipelineService_GetStatusNotActive(t *testing.T) {
	s, _, _ := newTestPipelineService(t)
	ctx := context.Background()
	res, err := s.GetStatus(ctx, "any-id")
	if err != nil {
		t.Fatalf("GetStatus: unexpected error: %v", err)
	}
	if res.State != "closed" {
		t.Errorf("GetStatus State = %q, want %q", res.State, "closed")
	}
}

// TestPipelineService_MultiSessionConcurrent 验证多会话并发启动，各自有独立 sessionID，
// 且全部能自动 finalize 为 stopped。
//
// 注意：由于无 tcp 插件，每个 task 的 run goroutine 会立即退出并触发 finalizeTask。
// 因此本测试验证的是：并发 StartSession 不会冲突，sessionID 唯一，ControlStore 记录完整。
func TestPipelineService_MultiSessionConcurrent(t *testing.T) {
	s, workDir, controlStore := newTestPipelineService(t)
	ctx := context.Background()
	req := fileStartSessionRequest(workDir)

	// 启动 3 个并发会话
	var sessionIDs []string
	for i := 0; i < 3; i++ {
		res, err := s.StartSession(ctx, req)
		if err != nil {
			t.Fatalf("StartSession %d: %v", i, err)
		}
		sessionIDs = append(sessionIDs, res.SessionID)
		time.Sleep(20 * time.Millisecond) // 确保不同 sessionID
	}

	// 验证 sessionID 互不相同
	seen := make(map[string]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		if seen[id] {
			t.Errorf("duplicate sessionID: %s", id)
		}
		seen[id] = true
	}

	// 等待所有 task 自动 finalize 完成（无 tcp 插件，立即退出）
	time.Sleep(500 * time.Millisecond)

	// 验证所有 task 已从 map 移除
	sessions, _ := s.ListSessions(ctx)
	if len(sessions) != 0 {
		t.Errorf("after auto-finalize, ListSessions count = %d, want 0", len(sessions))
	}

	// 验证 ControlStore 中所有会话都已记录为 stopped
	for _, id := range sessionIDs {
		meta, err := controlStore.GetSession(ctx, id)
		if err != nil {
			t.Errorf("GetSession %s: %v", id, err)
			continue
		}
		if meta.Status != "stopped" {
			t.Errorf("session %s status = %q, want %q", id, meta.Status, "stopped")
		}
	}
}

// TestPipelineService_AutoFinalizeCleanup 验证 run goroutine 自动退出后
// finalizeTask 自动写 ControlStore + removeTask。
func TestPipelineService_AutoFinalizeCleanup(t *testing.T) {
	s, workDir, controlStore := newTestPipelineService(t)
	ctx := context.Background()
	req := fileStartSessionRequest(workDir)

	res, err := s.StartSession(ctx, req)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// 等 run goroutine 自动退出 + finalize 完成
	waitForTaskDone(t, s, res.SessionID, 2*time.Second)
	time.Sleep(200 * time.Millisecond)

	// 验证 task 已从 map 移除
	if _, ok := s.getTask(res.SessionID); ok {
		t.Error("task still in map after auto-finalize")
	}

	// 验证 ControlStore 已更新为 stopped
	meta, err := controlStore.GetSession(ctx, res.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if meta.Status != "stopped" {
		t.Errorf("ControlStore status = %q, want %q", meta.Status, "stopped")
	}
}
