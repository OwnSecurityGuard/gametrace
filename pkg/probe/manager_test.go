package probe

// 回归护栏：desired-state 的下发路径（StartCapture/StopCapture/Retry）必须在
// 探针在线时真的把指令投递进控制流，且不能自锁。
//
// 曾经的事故：syncLocked 在持 m.mu 的情况下调用 nextCmdID，而 nextCmdID 会
// 再次 m.mu.Lock —— Go 的 Mutex 不可重入，任何一次下发都会把整个
// CaptureControl handler 永久挂住，表现为「点了抓包，探针什么也没收到」。

import (
	"context"
	"testing"
	"time"

	"gametrace/pkg/capture/agent/proto"
	"gametrace/pkg/store"
)

// fakeProbeStore 只实现 Manager 用到的落库接口。
type fakeProbeStore struct {
	probes map[string]store.ProbeMeta
}

func newFakeProbeStore() *fakeProbeStore {
	return &fakeProbeStore{probes: map[string]store.ProbeMeta{
		"prb_1": {ProbeID: "prb_1", Name: "dev-pc", Owner: "alice", ConnectionState: "online"},
	}}
}

func (f *fakeProbeStore) GetProbe(_ context.Context, id string) (*store.ProbeMeta, error) {
	p, ok := f.probes[id]
	if !ok {
		return nil, errNotFound
	}
	return &p, nil
}
func (f *fakeProbeStore) GetProbeByTokenHash(context.Context, string) (*store.ProbeMeta, error) {
	return nil, errNotFound
}
func (f *fakeProbeStore) UpsertProbe(context.Context, store.ProbeMeta) error { return nil }
func (f *fakeProbeStore) UpdateProbeStatus(context.Context, string, store.ProbeRuntimeStatus) error {
	return nil
}
func (f *fakeProbeStore) UpdateProbeInterfaces(context.Context, string, string) error {
	return nil
}
func (f *fakeProbeStore) SetProbeConnection(context.Context, string, string, time.Time) error {
	return nil
}
func (f *fakeProbeStore) ListProbes(context.Context) ([]store.ProbeMeta, error) {
	out := make([]store.ProbeMeta, 0, len(f.probes))
	for _, p := range f.probes {
		out = append(out, p)
	}
	return out, nil
}
func (f *fakeProbeStore) RenameProbe(context.Context, string, string) error      { return nil }
func (f *fakeProbeStore) RevokeProbe(context.Context, string) error              { return nil }
func (f *fakeProbeStore) DeleteProbe(context.Context, string) error              { return nil }
func (f *fakeProbeStore) ReplaceProbeSegments(context.Context, string, []store.ArchiveSegmentMeta) error {
	return nil
}
func (f *fakeProbeStore) ListProbeSegments(context.Context, string, int64, int64) ([]store.ArchiveSegmentMeta, error) {
	return nil, nil
}

var errNotFound = errStoreNotFound("probe not found")

type errStoreNotFound string

func (e errStoreNotFound) Error() string { return string(e) }

// TestStartCaptureDispatchesAssign 验证 StartCapture 会向在线探针下发 AssignCapture。
// 有死锁时本测试会挂住，由 go test 的 -timeout 兜住（不是静默通过）。
func TestStartCaptureDispatchesAssign(t *testing.T) {
	m := NewManager(newFakeProbeStore(), nil, nil, nil)
	m.openConn("prb_1", func() {})

	done := make(chan error, 1)
	go func() { done <- m.StartCapture(context.Background(), "prb_1", Desired{SessionID: "s-1"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartCapture: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartCapture 未返回：下发路径自锁（syncLocked 持锁又调 nextCmdID）")
	}

	m.mu.Lock()
	conn := m.conns["prb_1"]
	m.mu.Unlock()
	select {
	case cmd := <-conn.send:
		a := cmd.GetAssign()
		if a == nil {
			t.Fatalf("期望 AssignCapture，实际 %T", cmd.GetPayload())
		}
		if a.GetSessionId() != "s-1" {
			t.Fatalf("session mismatch: %q", a.GetSessionId())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("探针未收到任何指令")
	}
}

// TestStopCaptureDispatchesStop 验证停止路径同样能把 Stop 投递出去。
func TestStopCaptureDispatchesStop(t *testing.T) {
	m := NewManager(newFakeProbeStore(), nil, nil, nil)
	m.openConn("prb_1", func() {})
	// 造一次「正在抓包」的快照，否则 desired 为空且非 capturing 时不会发 Stop。
	m.applyHeartbeat("prb_1", &proto.ProbeHeartbeat{
		Capture: &proto.ProbeCaptureStatus{State: "running", SessionId: "s-1"},
	})

	if _, err := m.StopCapture(context.Background(), "prb_1"); err != nil {
		t.Fatalf("StopCapture: %v", err)
	}
	m.mu.Lock()
	conn := m.conns["prb_1"]
	m.mu.Unlock()
	select {
	case cmd := <-conn.send:
		if cmd.GetStop() == nil {
			t.Fatalf("期望 StopCapture，实际 %T", cmd.GetPayload())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("探针未收到停止指令")
	}
}

// TestSendAlertCarriesBundle 验证 SendAlert 下发的是扩展版 Command_Notify：
// 除 title/message 外还带 alert_id + detail_json（探针据此存本地告警并开详情页），
// 并走 sendAndWait 等待探针确认。
func TestSendAlertCarriesBundle(t *testing.T) {
	m := NewManager(newFakeProbeStore(), nil, nil, nil)
	m.openConn("prb_1", func() {})

	detail := []byte(`{"alert_id":"al1","rule_id":"r1","title":"命中"}`)
	done := make(chan error, 1)
	go func() {
		done <- m.SendAlert(context.Background(), "prb_1", "命中", "登录告警", "al1", detail)
	}()

	m.mu.Lock()
	conn := m.conns["prb_1"]
	m.mu.Unlock()

	var cmd *proto.Command
	select {
	case cmd = <-conn.send:
	case <-time.After(2 * time.Second):
		t.Fatal("SendAlert 未投递任何指令")
	}
	n := cmd.GetNotify()
	if n == nil {
		t.Fatalf("期望 Command_Notify，实际 %T", cmd.GetPayload())
	}
	if n.GetAlertId() != "al1" {
		t.Fatalf("alert_id = %q, want al1", n.GetAlertId())
	}
	if string(n.GetDetailJson()) != string(detail) {
		t.Fatalf("detail_json = %q, want %q", n.GetDetailJson(), detail)
	}
	if n.GetTitle() != "命中" || n.GetMessage() != "登录告警" {
		t.Fatalf("title/message mismatch: %q / %q", n.GetTitle(), n.GetMessage())
	}

	// 探针确认成功 → 唤醒 sendAndWait。
	m.mu.Lock()
	ch, ok := m.pendingResults[cmd.GetId()]
	m.mu.Unlock()
	if !ok {
		t.Fatal("SendAlert 未在 pendingResults 注册结果通道")
	}
	ch <- &proto.CommandResult{Id: cmd.GetId(), Ok: true}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendAlert: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendAlert 未随结果确认返回")
	}
}

// TestSendAlertOffline 验证探针离线时 SendAlert 直接失败（一次性动作，不排队补发）。
func TestSendAlertOffline(t *testing.T) {
	m := NewManager(newFakeProbeStore(), nil, nil, nil)
	if err := m.SendAlert(context.Background(), "nope", "t", "m", "a1", nil); err == nil {
		t.Fatal("expected error for unknown/offline probe")
	}
}

// TestHeartbeatReconcilesOrphanCaptureAfterRestart 是平台重启后探针卡死在
// 「抓包中」的回归护栏。
//
// 事故链：重启清空 desired/latest → 探针重连时 syncLocked 因 latest 为空判定
// 「没在抓」不发 Stop → 之后没有任何路径再触发对账 → 探针永远 running，
// 心跳每 10s 把 running 刷回平台，UI 恒显示「抓包中」。
// 修复：applyHeartbeat 填上 latest 后，对无 desired 的探针立即对账一次。
func TestHeartbeatReconcilesOrphanCaptureAfterRestart(t *testing.T) {
	m := NewManager(newFakeProbeStore(), nil, nil, nil)
	// 模拟重启后探针重连：此刻 latest 为空，openConn 的对齐不发任何指令。
	m.openConn("prb_1", func() {})
	// 重启前遗留的抓包会话在 desired 里已不存在（内存清零 + 会话被 reconcile）。
	m.applyHeartbeat("prb_1", &proto.ProbeHeartbeat{
		Capture: &proto.ProbeCaptureStatus{State: "running", SessionId: "s-orphan"},
	})

	m.mu.Lock()
	conn := m.conns["prb_1"]
	m.mu.Unlock()
	select {
	case cmd := <-conn.send:
		if cmd.GetStop() == nil {
			t.Fatalf("期望孤儿抓包被 Stop 对账，实际 %T", cmd.GetPayload())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("心跳对账未下发 Stop：重启后探针会永久卡在 running")
	}
}

// TestHeartbeatWithDesiredDispatchesNothing 守住对账的边界：desired 存在且
// 探针状态与之相符（或处于 failed）时，心跳不得投递任何指令——否则 failed
// 探针会被每 10s 自动重发 Assign，破坏「失败等待手动 Retry」语义。
func TestHeartbeatWithDesiredDispatchesNothing(t *testing.T) {
	m := NewManager(newFakeProbeStore(), nil, nil, nil)
	m.openConn("prb_1", func() {})
	m.mu.Lock()
	m.desired["prb_1"] = Desired{SessionID: "s-1"}
	m.mu.Unlock()
	// 匹配会话的 running：幂等，无指令。
	m.applyHeartbeat("prb_1", &proto.ProbeHeartbeat{
		Capture: &proto.ProbeCaptureStatus{State: "running", SessionId: "s-1"},
	})
	// failed：也不得自动重发。
	m.applyHeartbeat("prb_1", &proto.ProbeHeartbeat{
		Capture: &proto.ProbeCaptureStatus{State: "failed", SessionId: "s-1", Error: "boom"},
	})

	m.mu.Lock()
	conn := m.conns["prb_1"]
	m.mu.Unlock()
	select {
	case cmd := <-conn.send:
		t.Fatalf("心跳对账不应投递指令，实际收到 %T", cmd.GetPayload())
	default:
	}
}
