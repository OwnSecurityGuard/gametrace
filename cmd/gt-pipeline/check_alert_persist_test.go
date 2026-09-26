package main

// check_alert_persist_test.go —— 检查规则命中的「平台端落库」端到端护栏。
//
// 关键语义：命中记录是为「会话内回看」而存在的，与会话是否挂着探针无关。曾经
// 只有探针链路（notifyProbe != nil）才武装 checkEngine，pcap/文件会话即使配了
// 规则也什么都没记下。本测试用一条真实的 agent 会话（probeMgr 刻意不设）跑完
// 整个解码链路，确认命中确实落进了会话库。

import (
	"context"
	"strings"
	"testing"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/capture/agent"
	"gametrace/pkg/checkrule"
	gevent "gametrace/pkg/event"
	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/plugin"
	"gametrace/pkg/store"

	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
)

func TestCheckRuleAlertsPersistedWithoutProbe(t *testing.T) {
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
	// 刻意不调 SetProbeManager：这正是「无探针会话」的形态。

	if _, stopDec, err := startSimDecoder(mgr); err != nil {
		t.Fatalf("启动 sim-game 解码器失败: %v", err)
	} else {
		defer stopDec()
	}

	ctx := context.Background()
	if owner, _ := loadTokensFromEnv(); owner != "" {
		ctx = auth.WithPrincipal(ctx, &auth.Principal{Owner: owner})
	}

	res, err := s.StartSession(ctx, capturecontrol.StartSessionRequest{
		Agent:  true,
		Plugin: simPluginName,
		Port:   9250,
		CheckRules: []checkrule.CheckRule{{
			ID:                  "r-move",
			Name:                "移动命中",
			Enabled:             true,
			When:                rule.Predicate{Path: "_meta.msg_name", Op: rule.OpEq, Value: "Move"},
			ContextPerDirection: 2,
		}},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(res.SessionID) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hub.Subscribers(res.SessionID) == 0 {
		t.Fatalf("agent source 未订阅 hub（session=%s）", res.SessionID)
	}
	for _, p := range generateScenario(time.Now()) {
		hub.Deliver(res.SessionID, []gevent.Packet{p})
	}
	time.Sleep(3 * time.Second)
	if _, err := s.StopSession(ctx, res.SessionID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	st, err := store.NewSQLiteStoreReadOnly(res.DBPath)
	if err != nil {
		t.Fatalf("打开会话库失败: %v", err)
	}
	defer st.Close()

	rows, total, err := st.QueryAlerts(ctx, store.AlertQuery{SessionID: res.SessionID, Limit: 50})
	if err != nil {
		t.Fatalf("QueryAlerts: %v", err)
	}
	if total == 0 || len(rows) == 0 {
		t.Fatalf("无探针会话的命中没有落库（session=%s）", res.SessionID)
	}
	row := rows[0]
	if row.RuleID != "r-move" || row.RuleName != "移动命中" {
		t.Errorf("rule columns = %q/%q, want r-move/移动命中", row.RuleID, row.RuleName)
	}
	if !strings.Contains(row.TriggerJSON, "Move") {
		t.Errorf("trigger_json 不含触发消息名: %s", row.TriggerJSON)
	}
	// 冷却窗口（默认 30s）内多次 Move 只记一条，落库不能被逐事件刷满。
	if total != 1 {
		t.Errorf("命中条数 = %d, want 1（同规则同会话冷却去重）", total)
	}
}
