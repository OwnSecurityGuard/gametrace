package store

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"gametrace/pkg/capture"
	"gametrace/pkg/event"
)

// newCloseTestStore 打开一个临时 SQLite store，写入给定连接的 raw 帧。
func newCloseTestStore(t *testing.T, packets []event.Packet) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "close.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.AppendRawPackets(context.Background(), packets); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestQueryConnections_CloseStatus 覆盖三种关闭来源：
//  1. pcap 源：FIN/RST 帧写发送端地址，按 client/server 取向归一；
//  2. 移动 SDK：ConnClose.close_side 直写 "client"|"server"；
//  3. 无法归类的关闭地址 → "unknown"。
func TestQueryConnections_CloseStatus(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	clientAddr := "10.0.0.1:55001"
	serverAddr := "10.0.0.2:443"
	src := netip.MustParseAddrPort(clientAddr)
	dst := netip.MustParseAddrPort(serverAddr)

	// c1：pcap 方向，关闭帧发送端是客户端地址（写地址值），按 client/server 取向
	// 归一为 client（验证「地址 vs 权威侧别字符串」两条分支）。
	packets := []event.Packet{
		{ID: "c1a", Timestamp: base, Src: src, Dst: dst, Protocol: "tcp",
			Metadata: map[string]any{"conn_id": "c1", capture.MetaClientAddr: clientAddr, capture.MetaServerAddr: serverAddr}},
		// 关闭标记帧：发送端 clientAddr（pcap 写地址取向）。
		{ID: "c1b", Timestamp: base.Add(time.Millisecond), Src: src, Dst: dst, Protocol: "tcp",
			Metadata: map[string]any{"conn_id": "c1", capture.MetaClientAddr: clientAddr, capture.MetaServerAddr: serverAddr, TCPCloseKey: clientAddr}},
		// c2：移动 SDK 直写 "server"。
		{ID: "c2a", Timestamp: base.Add(time.Millisecond), Src: src, Dst: dst, Protocol: "tcp",
			Metadata: map[string]any{"conn_id": "c2", capture.MetaClientAddr: clientAddr, capture.MetaServerAddr: serverAddr}},
		{ID: "c2b", Timestamp: base.Add(2 * time.Millisecond), Src: src, Dst: dst, Protocol: "tcp",
			Metadata: map[string]any{"conn_id": "c2", capture.MetaClientAddr: clientAddr, capture.MetaServerAddr: serverAddr, TCPCloseKey: "server"}},
		// c3：关闭地址与 client/server 都不匹配 → unknown。
		{ID: "c3a", Timestamp: base.Add(time.Millisecond), Src: src, Dst: dst, Protocol: "tcp",
			Metadata: map[string]any{"conn_id": "c3", capture.MetaClientAddr: clientAddr, capture.MetaServerAddr: serverAddr}},
		{ID: "c3b", Timestamp: base.Add(2 * time.Millisecond), Src: src, Dst: dst, Protocol: "tcp",
			Metadata: map[string]any{"conn_id": "c3", capture.MetaClientAddr: clientAddr, capture.MetaServerAddr: serverAddr, TCPCloseKey: "9.9.9.9:1"}},
		// c4：无任何关闭标记 → 活跃（closed=false）。
		{ID: "c4a", Timestamp: base.Add(time.Millisecond), Src: src, Dst: dst, Protocol: "tcp",
			Metadata: map[string]any{"conn_id": "c4", capture.MetaClientAddr: clientAddr, capture.MetaServerAddr: serverAddr}},
		{ID: "c4b", Timestamp: base.Add(2 * time.Millisecond), Src: src, Dst: dst, Protocol: "tcp",
			Metadata: map[string]any{"conn_id": "c4", capture.MetaClientAddr: clientAddr, capture.MetaServerAddr: serverAddr}},
	}

	s := newCloseTestStore(t, packets)

	conns, err := s.QueryConnections(context.Background(), "s1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ConnectionSummary{}
	for _, c := range conns {
		got[c.ConnID] = c
	}

	// c1：pcap 方向，客户端地址取向 → closed_by=client。
	if c := got["c1"]; !c.Closed || c.ClosedBy != "client" {
		t.Fatalf("c1: closed=%v closed_by=%q want true/client", c.Closed, c.ClosedBy)
	}
	// c2：移动直写 server。
	if c := got["c2"]; !c.Closed || c.ClosedBy != "server" {
		t.Fatalf("c2: closed=%v closed_by=%q want true/server", c.Closed, c.ClosedBy)
	}
	// c3：无法归类 → unknown。
	if c := got["c3"]; !c.Closed || c.ClosedBy != "unknown" {
		t.Fatalf("c3: closed=%v closed_by=%q want true/unknown", c.Closed, c.ClosedBy)
	}
	// c4：无关闭标记 → 活跃。
	if c := got["c4"]; c.Closed || c.ClosedBy != "" {
		t.Fatalf("c4: closed=%v closed_by=%q want false/\"\"", c.Closed, c.ClosedBy)
	}

	// Detail 与 Summary 口径一致。
	d, err := s.QueryConnectionDetail(context.Background(), "s1", "c1")
	if err != nil || d == nil {
		t.Fatalf("detail c1: %v %v", d, err)
	}
	if !d.Closed || d.ClosedBy != "client" {
		t.Fatalf("detail c1: closed=%v closed_by=%q want true/client", d.Closed, d.ClosedBy)
	}
}

// TestClosedByFromMeta_ExactString 直接验证归一化辅助的字符串分支。
func TestClosedByFromMeta_ExactString(t *testing.T) {
	if c, b := closedByFromMeta(`{"tcp_close":"server"}`, "c", "s"); !c || b != "server" {
		t.Fatalf("server: got %v/%v", c, b)
	}
	if c, b := closedByFromMeta(`{"tcp_close":"client"}`, "c", "s"); !c || b != "client" {
		t.Fatalf("client: got %v/%v", c, b)
	}
	// 地址取向。
	if c, b := closedByFromMeta(`{"tcp_close":"10.0.0.1:1"}`, "10.0.0.1:1", "10.0.0.2:2"); !c || b != "client" {
		t.Fatalf("addr-client: got %v/%v", c, b)
	}
	// 无标记 / 非法 JSON / 空值。
	if c, b := closedByFromMeta("", "c", "s"); c || b != "" {
		t.Fatalf("empty: got %v/%v", c, b)
	}
	if c, b := closedByFromMeta("not-json", "c", "s"); c || b != "" {
		t.Fatalf("badjson: got %v/%v", c, b)
	}
	if c, b := closedByFromMeta(`{"other":"x"}`, "c", "s"); c || b != "" {
		t.Fatalf("nokey: got %v/%v", c, b)
	}
	if c, b := closedByFromMeta(`{"tcp_close":""}`, "c", "s"); c || b != "" {
		t.Fatalf("emptyval: got %v/%v", c, b)
	}
}