package main

// baseline_scope_test.go —— 实体基线隔离边界的端到端护栏。
//
// 背景：基线原来每个抓包会话一份，重抓同一客户端时整份数据视图都会退化成「首见」
//（模拟数据 526 条里 377 条），真实变化被埋在批量同步噪音里。改成进程级共享 +
// 可选的对端作用域（ScopePeer）后，「重抓」能接着上一轮的终值算 before。
//
// 本测试跑两遍同一份合成场景（相同客户端、相同服务端端口），用真实管线验证：
//   - ScopeSession（默认）：第二遍依然全部首见 —— 重放友好，这是默认值的原因；
//   - ScopePeer：第二遍能拿到上一轮的终值（before_resolved > 0），延续成立。
//
// ⚠️ ScopePeer 下第二遍的行数会少于第一遍：重放同一批流量时「初始值 vs 上一轮终值」
// 会被如实报成反向变化，同值部分则被 noop 抑制。这是作用域语义的必然结果，
// 也是它不能当默认值的理由（pcap 回放会看起来像数据缺失）。

import (
	"context"
	"testing"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/capture/agent"
	gevent "gametrace/pkg/event"
	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/plugin"
	"gametrace/pkg/state"
	"gametrace/pkg/store"
)

// runSimSession 开一个 agent 会话，把合成场景经 hub 投递进去，停会话后返回落库的变更行。
func runSimSession(t *testing.T, ctx context.Context, s *pipelineService, hub *agent.Hub) (string, []store.StateChangeRow) {
	t.Helper()

	res, err := s.StartSession(ctx, capturecontrol.StartSessionRequest{
		Agent:  true,
		Plugin: simPluginName,
		Port:   9250, // 当作游戏服端口：对端标识（PeerKey）依赖方向推断
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sessionID := res.SessionID

	// 等 agent source 完成 hub 订阅，否则投递进来的包会被整批丢弃。
	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(sessionID) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hub.Subscribers(sessionID) == 0 {
		t.Fatalf("agent source 未订阅 hub（session=%s）", sessionID)
	}

	for _, p := range generateScenario(time.Now()) {
		hub.Deliver(sessionID, []gevent.Packet{p})
	}
	// 等主循环消费并落库（管线每秒 flush 一次）。
	time.Sleep(3 * time.Second)
	if _, err := s.StopSession(ctx, sessionID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	st, err := store.NewSQLiteStoreReadOnly(res.DBPath)
	if err != nil {
		t.Fatalf("打开会话库失败: %v", err)
	}
	defer st.Close()
	rows, err := st.QueryStateChanges(ctx, store.StateChangeQuery{SessionID: sessionID, Limit: 100000})
	if err != nil {
		t.Fatalf("QueryStateChanges: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("state_changes 为空（session=%s）", sessionID)
	}
	return sessionID, rows
}

// countResolved 统计有平台旧值的变更条数，以及落库 seq 非 0（= 位次确实写入了）的条数。
func countResolved(rows []store.StateChangeRow) (resolved, withSeq int) {
	for _, r := range rows {
		if r.BeforeResolved {
			resolved++
		}
		if r.Seq > 0 {
			withSeq++
		}
	}
	return resolved, withSeq
}

// TestBaselineScopeAcrossSessions 验证两种隔离边界的实际行为差异。
func TestBaselineScopeAcrossSessions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		scope         state.Scope
		wantCarryOver bool // 第二遍是否应复用上一轮的终值
	}{
		{name: "session 作用域：重抓从零开始", scope: state.ScopeSession, wantCarryOver: false},
		{name: "peer 作用域：重抓延续上一轮终值", scope: state.ScopePeer, wantCarryOver: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			controlStore, err := store.NewControlStore(workDir + "/control.sqlite")
			if err != nil {
				t.Fatalf("NewControlStore: %v", err)
			}
			defer controlStore.Close()

			mgr := plugin.NewRegistryServer(10)
			hub := agent.NewHub()
			s := newPipelineService(workDir, controlStore, mgr, ":9091", "sqlite", "")
			s.SetAgentHub(hub)
			s.SetBaselineScope(tc.scope)

			if _, stopDec, err := startSimDecoder(mgr); err != nil {
				t.Fatalf("启动 sim-game 解码器失败: %v", err)
			} else {
				defer stopDec()
			}

			ctx := context.Background()
			if owner, _ := loadTokensFromEnv(); owner != "" {
				ctx = auth.WithPrincipal(ctx, &auth.Principal{Owner: owner})
			}

			firstID, first := runSimSession(t, ctx, s, hub)
			firstResolved, firstSeq := countResolved(first)

			secondID, second := runSimSession(t, ctx, s, hub)
			secondResolved, secondSeq := countResolved(second)

			// 探针：会话结束时按会话回收的基线应已归零；按对端共享的仍在（这就是「延续」的载体）。
			t.Logf("作用域探针：scope=%s 保留实体=%d", s.baselines.Scope().String(), s.baselines.Len())
			t.Logf("第一遍 session=%s 条数=%d resolved=%d 带 seq 的条数=%d",
				firstID, len(first), firstResolved, firstSeq)
			t.Logf("第二遍 session=%s 条数=%d resolved=%d 带 seq 的条数=%d",
				secondID, len(second), secondResolved, secondSeq)

			// 每条落库变更都应带 1 基 seq（0 只留给未设置的老行/外部写入）。
			if firstSeq != len(first) {
				t.Errorf("带 seq 的条数=%d / 总条数=%d：seq 列没有真正写入", firstSeq, len(first))
			}
			if len(second) == 0 {
				t.Error("第二遍没有任何变更")
			}
			// 第一遍的 resolved 只可能来自会话内重复上报（同一 path 被反复声明），
			// 两种作用域下它都一样 —— 下面的比较以此为前提。
			if secondResolved < firstResolved {
				t.Errorf("第二遍 resolved(%d) 少于第一遍(%d)：基线被重置了", secondResolved, firstResolved)
			}

			if tc.wantCarryOver {
				// 延续成立的两个证据：同值回吐被大量抑制（条数明显变少），
				// 且剩下的变化几乎都带平台旧值（不再是首见）。
				if secondResolved == firstResolved {
					t.Errorf("peer 作用域下第二遍 resolved 与第一遍相同(%d)：没有复用上一轮终值", secondResolved)
				}
				if len(second) >= len(first) {
					t.Errorf("peer 作用域下第二遍条数(%d)未少于第一遍(%d)：同值回吐没有被抑制",
						len(second), len(first))
				}
			} else if secondResolved != firstResolved {
				// 会话作用域下第二遍与第一遍同构：每次抓包都从零开始记。
				t.Errorf("session 作用域下第二遍不该多出旧值：%d vs %d", secondResolved, firstResolved)
			}
		})
	}
}
