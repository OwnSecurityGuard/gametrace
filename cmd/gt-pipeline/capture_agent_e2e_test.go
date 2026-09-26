package main

// 探针抓包链路的端到端护栏（2026-09-08）：
// 探针推流 → AgentIngest → Hub → 会话的 agent source → capture_task →
// raw_packets 落库。「连接」页（按 conn_id 聚合）与「原始包」页都依赖这张表，
// 因此这里断言"推进去的包真的落到了 raw_packets 且带了 conn_id"，
// 而不是只断言"hub 收到了"。

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/capture/agent"
	"gametrace/pkg/event"
	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/store"
)

// TestAgentSessionRawPacketsPersisted 验证探针推来的包会落进会话库并派生 conn_id。
func TestAgentSessionRawPacketsPersisted(t *testing.T) {
	s, _, _ := newTestPipelineService(t)
	hub := agent.NewHub()
	s.SetAgentHub(hub)

	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "alice"})
	res, err := s.StartSession(ctx, capturecontrol.StartSessionRequest{Agent: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopSession(context.Background(), res.SessionID)

	// 等会话的 agent source 完成 Hub 订阅（否则 Deliver 会以"无订阅者"被丢弃）。
	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(res.SessionID) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hub.Subscribers(res.SessionID) == 0 {
		t.Fatal("agent source 未订阅 Hub：探针推来的包会被整批丢弃")
	}

	pkt := event.Packet{
		ID:        "pkt-1",
		Raw:       []byte("hello-game-traffic"),
		LinkType:  event.LinkTypeEthernet,
		Protocol:  "tcp",
		Timestamp: time.Now(),
		Src:       netip.MustParseAddrPort("10.0.0.1:4444"),
		Dst:       netip.MustParseAddrPort("10.0.0.2:9250"),
	}
	delivered, busy, noSub := hub.Deliver(res.SessionID, []event.Packet{pkt})
	if delivered != 1 || busy != 0 || noSub != 0 {
		t.Fatalf("deliver: delivered=%d busy=%d noSub=%d, want 1/0/0", delivered, busy, noSub)
	}

	// 等主循环消费并落库（主循环每秒 flush 一次），再停会话。
	time.Sleep(2 * time.Second)
	if _, err := s.StopSession(ctx, res.SessionID); err != nil {
		t.Fatalf("stop session: %v", err)
	}

	st, err := store.NewSQLiteStoreReadOnly(res.DBPath)
	if err != nil {
		t.Fatalf("open session db: %v", err)
	}
	defer st.Close()

	conns, err := st.QueryConnections(context.Background(), res.SessionID, 10, 0)
	if err != nil {
		t.Fatalf("query connections: %v", err)
	}
	if len(conns) == 0 {
		t.Fatal("连接页为空：raw_packets 里没有带 conn_id 的记录（探针抓到包但看不到连接就是此症状）")
	}
	if got := conns[0].ConnID; got == "" {
		t.Fatal("conn_id 未派生")
	}
}
