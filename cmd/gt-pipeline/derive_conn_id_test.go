package main

import (
	"net/netip"
	"testing"

	"gametrace/pkg/event"
)

func TestDeriveConnID(t *testing.T) {
	src := netip.MustParseAddrPort("127.0.0.1:50000")
	dst := netip.MustParseAddrPort("127.0.0.1:9250")

	// 双向排序：同一连接两个方向得到相同 conn_id。
	fwd := event.Packet{Protocol: "tcp", Src: src, Dst: dst}
	rev := event.Packet{Protocol: "tcp", Src: dst, Dst: src}
	deriveConnID(&fwd)
	deriveConnID(&rev)
	if fwd.Metadata["conn_id"] == "" || fwd.Metadata["conn_id"] != rev.Metadata["conn_id"] {
		t.Fatalf("bidirectional conn_id mismatch: %q vs %q", fwd.Metadata["conn_id"], rev.Metadata["conn_id"])
	}
	want := "tcp:127.0.0.1:50000<->127.0.0.1:9250"
	if fwd.Metadata["conn_id"] != want {
		t.Fatalf("conn_id = %q, want %q", fwd.Metadata["conn_id"], want)
	}

	// 已有 conn_id（移动代理路径）必须保留。
	proxy := event.Packet{Protocol: "tcp", Src: src, Dst: dst, Metadata: map[string]any{"conn_id": "proxy-conn-1"}}
	deriveConnID(&proxy)
	if proxy.Metadata["conn_id"] != "proxy-conn-1" {
		t.Fatalf("existing conn_id overwritten: %q", proxy.Metadata["conn_id"])
	}

	// UDP 也派生 conn_id（udp: 前缀，且双向一致）。
	udpFwd := event.Packet{Protocol: "udp", Src: src, Dst: dst}
	udpRev := event.Packet{Protocol: "udp", Src: dst, Dst: src}
	deriveConnID(&udpFwd)
	deriveConnID(&udpRev)
	if udpFwd.Metadata["conn_id"] == "" {
		t.Fatal("udp packet should get conn_id")
	}
	if udpFwd.Metadata["conn_id"] != udpRev.Metadata["conn_id"] {
		t.Fatalf("udp conn_id not bidirectional: %q vs %q", udpFwd.Metadata["conn_id"], udpRev.Metadata["conn_id"])
	}
	if want := "udp:127.0.0.1:50000<->127.0.0.1:9250"; udpFwd.Metadata["conn_id"] != want {
		t.Fatalf("udp conn_id = %q, want %q", udpFwd.Metadata["conn_id"], want)
	}
	bare := event.Packet{Protocol: "tcp"}
	deriveConnID(&bare)
	if _, ok := bare.Metadata["conn_id"]; ok {
		t.Fatal("packet without endpoints should not get conn_id")
	}
}
