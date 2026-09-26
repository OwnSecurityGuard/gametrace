package main

import (
	"net/netip"
	"testing"
	"time"

	"gametrace/pkg/event"
)

var (
	trackerClient = netip.MustParseAddrPort("10.0.0.2:50000")
	trackerServer = netip.MustParseAddrPort("10.0.0.1:9250")
)

func trackerTCP(src, dst netip.AddrPort, flags event.TCPFlags) event.Packet {
	return event.Packet{Protocol: "tcp", Src: src, Dst: dst, TCPFlags: flags}
}

func connIDOf(t *testing.T, tr *connTracker, p event.Packet) string {
	t.Helper()
	tr.assign(&p)
	v, _ := p.Metadata["conn_id"].(string)
	return v
}

// TestConnTrackerSameConnectionStable 同一连接内（含两个方向、无握手直接切入）
// 必须拿到同一个 ConnectionID。
func TestConnTrackerSameConnectionStable(t *testing.T) {
	tr := newConnTracker()
	syn := connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{SYN: true}))
	dataUp := connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{PSH: true, ACK: true}))
	dataDown := connIDOf(t, tr, trackerTCP(trackerServer, trackerClient, event.TCPFlags{PSH: true, ACK: true}))
	if syn == "" || dataUp != syn || dataDown != syn {
		t.Fatalf("同一连接 conn_id 不稳定: syn=%q up=%q down=%q", syn, dataUp, dataDown)
	}
}

// TestConnTrackerReconnectNewInstance 重连复用同一五元组时必须拿到新的
// ConnectionID——否则两代连接会被当成一条，pair 池把两代请求混在一起（F6）。
func TestConnTrackerReconnectNewInstance(t *testing.T) {
	tr := newConnTracker()
	first := connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{SYN: true}))
	// 双方 FIN 关闭（半关闭到齐才退役）
	connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{FIN: true, ACK: true}))
	connIDOf(t, tr, trackerTCP(trackerServer, trackerClient, event.TCPFlags{FIN: true, ACK: true}))

	second := connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{SYN: true}))
	if second == first {
		t.Fatalf("重连复用五元组后 conn_id 未变（会跨代错配）: %q", second)
	}
	// 新一代内保持稳定的同一 ID
	again := connIDOf(t, tr, trackerTCP(trackerServer, trackerClient, event.TCPFlags{PSH: true, ACK: true}))
	if again != second {
		t.Fatalf("新一代连接 conn_id 不稳定: %q vs %q", again, second)
	}
}

// TestConnTrackerHalfCloseKeepsID 一端 FIN 只是半关闭，另一端的后续数据仍属同一
// 连接实例——否则连接尾部的响应会被划到新 ID，与请求配不上对。
func TestConnTrackerHalfCloseKeepsID(t *testing.T) {
	tr := newConnTracker()
	id := connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{SYN: true}))
	connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{FIN: true, ACK: true}))
	tail := connIDOf(t, tr, trackerTCP(trackerServer, trackerClient, event.TCPFlags{PSH: true, ACK: true}))
	if tail != id {
		t.Fatalf("半关闭后服务端数据换了 conn_id: %q want %q", tail, id)
	}
}

// TestConnTrackerRSTRetires RST 立即退役，后续包属于新的连接实例。
func TestConnTrackerRSTRetires(t *testing.T) {
	tr := newConnTracker()
	first := connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{SYN: true}))
	connIDOf(t, tr, trackerTCP(trackerServer, trackerClient, event.TCPFlags{RST: true}))
	next := connIDOf(t, tr, trackerTCP(trackerClient, trackerServer, event.TCPFlags{PSH: true, ACK: true}))
	if next == first {
		t.Fatalf("RST 后应换新实例，仍为 %q", next)
	}
}

// TestConnTrackerProxyConnIDPreserved 业务侧（移动代理）给的真实连接身份优先。
func TestConnTrackerProxyConnIDPreserved(t *testing.T) {
	tr := newConnTracker()
	p := trackerTCP(trackerClient, trackerServer, event.TCPFlags{SYN: true})
	p.Metadata = map[string]any{"conn_id": "proxy-conn-1"}
	tr.assign(&p)
	if got := p.Metadata["conn_id"]; got != "proxy-conn-1" {
		t.Fatalf("代理 conn_id 被覆盖: %v", got)
	}
}

// TestConnTrackerUDPSweepIdle UDP 无 FIN/RST，只能按空闲退役。
func TestConnTrackerUDPSweepIdle(t *testing.T) {
	tr := newConnTracker()
	p := event.Packet{Protocol: "udp", Src: trackerClient, Dst: trackerServer}
	first := connIDOf(t, tr, p)

	tr.mu.Lock()
	for _, inst := range tr.active {
		inst.lastSeen = time.Now().Add(-udpIdleTTL - time.Minute)
	}
	tr.sweepLocked(time.Now())
	tr.mu.Unlock()

	second := connIDOf(t, tr, event.Packet{Protocol: "udp", Src: trackerClient, Dst: trackerServer})
	if second == first {
		t.Fatalf("UDP 空闲超时后未退役: %q", second)
	}
}

// TestConnTrackerNoEndpoints 无端点（代理 payload 等）不派生 conn_id。
func TestConnTrackerNoEndpoints(t *testing.T) {
	tr := newConnTracker()
	p := event.Packet{Protocol: "tcp"}
	tr.assign(&p)
	if _, ok := p.Metadata["conn_id"]; ok {
		t.Fatal("无端点的包不应派生 conn_id")
	}
}

// TestCanonicalTupleBidirectional 规范五元组双向一致，且不同协议不归一。
func TestCanonicalTupleBidirectional(t *testing.T) {
	up := trackerTCP(trackerClient, trackerServer, event.TCPFlags{})
	down := trackerTCP(trackerServer, trackerClient, event.TCPFlags{})
	if canonicalTuple(&up) != canonicalTuple(&down) {
		t.Fatalf("五元组未双向归一: %q vs %q", canonicalTuple(&up), canonicalTuple(&down))
	}
	udp := event.Packet{Protocol: "udp", Src: trackerClient, Dst: trackerServer}
	if canonicalTuple(&udp) == canonicalTuple(&up) {
		t.Fatal("tcp 与 udp 的五元组不应归一到同一个键")
	}
}
