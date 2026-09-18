package main

// decode_raw_seed_test.go —— 离线重解码的投影口径护栏。
//
// 背景：decode_raw 会清空并从头重跑整批 raw 包，而重解码产生的事件 ID 是新的。
// 「重解码会不会让 before 富化退化 / 会不会造出假变化」必须钉住。
//
// 2026-09-18 实测结论（两条，都是实测而非推断）：
//
//  1. **不干预时口径本来就一致。** 重解码是确定性重算：同一批 raw 包、同一个插件、
//     同样的时间序，基线在解码过程中按同一顺序重建，落点与实时那一轮逐条相同
//     （实时 526 条 / 149 resolved；重解码 526 条 / 149 resolved）。所以这条路径
//     不需要任何「复用上一轮富化结果」的机制——试图复用反而会引入错误。
//
//  2. **回放旧投影兜底是错的。** 把旧投影的终态（state.SeedFromRows）当基线起点，
//     再重放同一批流量，会双向出错：首见赋值被 noop 抑制（526 条 → 230 条，历史凭空
//     缺一块），场景中间值被判成「从终态退回去」的变化（Hero.level 4→1）造出反向假变化。
//     而且 forEachRawDecoded 没有「跳过已解码包」的能力，任何「增量」都必然重放，
//     无从规避。该能力保留在 state 包里供「真正续接新数据」的场景使用。
//
// 本测试锁住第 1 条：重解码后的变更集合与 before 富化必须与实时那一轮逐条相同。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/capture/agent"
	gevent "gametrace/pkg/event"
	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/plugin"
	"gametrace/pkg/store"
)

// scFingerprint 是变更行的全字段指纹（不含随机主键、事件 ID 与 before_resolved），
// 用于跨解码比对「这批流量到底产生了哪些变化」。
func scFingerprint(r store.StateChangeRow) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s→%s",
		r.SubjectType, r.SubjectID, r.Op, r.Path, r.Before, r.After)
}

// snapshotStateChanges 读取会话的全部状态变更，返回指纹计数与 resolved 条数。
func snapshotStateChanges(t *testing.T, dbPath, sessionID string) (map[string]int, int, int) {
	t.Helper()
	st, err := store.NewSQLiteStoreReadOnly(dbPath)
	if err != nil {
		t.Fatalf("打开会话库失败: %v", err)
	}
	defer st.Close()

	rows, err := st.QueryStateChanges(context.Background(), store.StateChangeQuery{
		SessionID: sessionID,
		Limit:     100000,
	})
	if err != nil {
		t.Fatalf("QueryStateChanges: %v", err)
	}
	counts := make(map[string]int, len(rows))
	resolved := 0
	for _, r := range rows {
		counts[scFingerprint(r)]++
		if r.BeforeResolved {
			resolved++
		}
	}
	return counts, len(rows), resolved
}

// TestDecodeRawProjectionConsistency 验证重解码后的变更集合与 before 富化
// 与实时抓包那一轮逐条相同（含 before_resolved 的分布）。
func TestDecodeRawProjectionConsistency(t *testing.T) {
	// 刻意不用 simWorkDir：本测试要独占一份 control.sqlite，避免与运行中的模拟器抢库。
	workDir := t.TempDir()
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建 work-dir 失败: %v", err)
	}
	controlStore, err := store.NewControlStore(filepath.Join(workDir, "control.sqlite"))
	if err != nil {
		t.Fatalf("NewControlStore: %v", err)
	}
	defer controlStore.Close()

	mgr := plugin.NewRegistryServer(10)
	hub := agent.NewHub()
	s := newPipelineService(workDir, controlStore, mgr, ":9091", "sqlite", "")
	s.SetAgentHub(hub)

	if _, stopDec, err := startSimDecoder(mgr); err != nil {
		t.Fatalf("启动 sim-game 解码器失败: %v", err)
	} else {
		defer stopDec()
	}

	owner, _ := loadTokensFromEnv()
	ctx := context.Background()
	if owner != "" {
		ctx = auth.WithPrincipal(ctx, &auth.Principal{Owner: owner})
	}

	res, err := s.StartSession(ctx, capturecontrol.StartSessionRequest{
		Agent:  true,
		Plugin: simPluginName,
		Port:   9250,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sessionID := res.SessionID
	dbPath := res.DBPath

	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(sessionID) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hub.Subscribers(sessionID) == 0 {
		t.Fatal("agent source 未订阅 hub")
	}

	for _, p := range generateScenario(time.Now()) {
		hub.Deliver(sessionID, []gevent.Packet{p})
	}
	time.Sleep(3 * time.Second) // 管线每秒 flush 一次
	if _, err := s.StopSession(ctx, sessionID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	liveCounts, liveTotal, liveResolved := snapshotStateChanges(t, dbPath, sessionID)
	if liveTotal == 0 {
		t.Fatal("实时抓包没有落库任何状态变更")
	}
	t.Logf("实时抓包：%d 条，其中 before 已解析 %d 条", liveTotal, liveResolved)
	if liveResolved == 0 {
		t.Fatal("实时抓包没有任何已解析的 before：实时基线没生效，本用例失去区分度")
	}

	decoded, err := s.DecodeRawPackets(ctx, capturecontrol.DecodeRawPacketsRequest{
		SessionID:     sessionID,
		Plugin:        simPluginName,
		ClearExisting: true,
	})
	if err != nil {
		t.Fatalf("DecodeRawPackets: %v", err)
	}
	t.Logf("重解码：raw=%d decoded=%d errors=%d", decoded.TotalRaw, decoded.Decoded, decoded.DecodeErrors)

	reCounts, reTotal, reResolved := snapshotStateChanges(t, dbPath, sessionID)
	t.Logf("重解码后：%d 条，其中 before 已解析 %d 条", reTotal, reResolved)

	// 少了 = 首见记录被误抑制；多了 = 出现了反向假变化。两边都不允许。
	if reTotal != liveTotal {
		t.Errorf("重解码后条数 = %d, want %d", reTotal, liveTotal)
	}
	if !reflect.DeepEqual(reCounts, liveCounts) {
		shown := 0
		for sig, n := range liveCounts {
			if got := reCounts[sig]; got != n {
				t.Errorf("实时 %d 次 / 重解码 %d 次：%s", n, got, sig)
				if shown++; shown >= 5 {
					break
				}
			}
		}
		for sig, n := range reCounts {
			if _, ok := liveCounts[sig]; !ok {
				t.Errorf("重解码凭空多出的变更：%s（×%d）", sig, n)
				if shown++; shown >= 10 {
					break
				}
			}
		}
	}
	// 重解码是确定性重算：同一批 raw 包 + 同一个插件 + 同样的时间序，基线在解码过程中
	// 按同一顺序重建，落点与实时那一轮完全一致。所以 before 富化这里本来就是等价的，
	// 不需要任何「复用上一轮结果」的机制（实测：实时 526 条 / 149 resolved，
	// 重解码 526 条 / 149 resolved）。
	if reResolved != liveResolved {
		t.Errorf("重解码后 before 已解析 = %d, want %d（与实时那一轮不一致）", reResolved, liveResolved)
	}
}
