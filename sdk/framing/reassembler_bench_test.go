package framing

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
)

// benchSegment 构造一个 TCP 数据段（IsTCP，无 FIN/SYN）。
func benchSegment(flow FlowKey, seq uint32, size int) Segment {
	return Segment{
		Payload: bytes.Repeat([]byte{'x'}, size),
		Flow:    flow,
		Seq:     seq,
		IsTCP:   true,
		Flags:   TCPFlags{PSH: true, ACK: true},
	}
}

func benchFlow() FlowKey {
	return FlowKey{
		Src:      netip.MustParseAddrPort("10.0.0.1:443"),
		Dst:      netip.MustParseAddrPort("10.0.0.2:51234"),
		Protocol: "tcp",
	}
}

// BenchmarkReassemblerInOrder 是主流形态：顺序段到达即 drain。
func BenchmarkReassemblerInOrder(b *testing.B) {
	flow := benchFlow()
	segSize := 1448
	b.ReportAllocs()
	for b.Loop() {
		ra := NewReassembler()
		for i := 0; i < 100; i++ {
			st := ra.Push(benchSegment(flow, uint32(i)*uint32(segSize), segSize))
			if len(st.Bytes()) == 0 {
				b.Fatal("in-order segment must yield contiguous bytes")
			}
			st.Consume(segSize)
		}
	}
}

// BenchmarkReassemblerOutOfOrder 强制每段进 pending 缓冲、按空洞填补路径 drain
// （乱序重排是 pendingSeg/插入排序的热路径）。
func BenchmarkReassemblerOutOfOrder(b *testing.B) {
	flow := benchFlow()
	segSize := 1448
	b.ReportAllocs()
	for b.Loop() {
		ra := NewReassembler()
		for i := 99; i >= 0; i-- {
			st := ra.Push(benchSegment(flow, uint32(i)*uint32(segSize), segSize))
			st.Consume(len(st.Bytes()))
		}
	}
}

// benchEthernetFrame 手工拼 Ethernet/IPv4/TCP 完整帧（供 ExtractL7 基准）。
func benchEthernetFrame(src, dst net.IP, sport, dport uint16, payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x08, 0x00})
	l3 := 20 + 20 + len(payload)
	ip := []byte{0x45, 0x00, byte(l3 >> 8), byte(l3), 0x00, 0x01, 0x40, 0x00, 64, 6, 0x00, 0x00}
	ip = append(ip, src.To4()...)
	ip = append(ip, dst.To4()...)
	buf.Write(ip)
	var tcp [20]byte
	binary.BigEndian.PutUint16(tcp[0:2], sport)
	binary.BigEndian.PutUint16(tcp[2:4], dport)
	binary.BigEndian.PutUint32(tcp[4:8], 1)
	binary.BigEndian.PutUint32(tcp[8:12], 1)
	tcp[12] = 5 << 4
	tcp[13] = 0x18
	binary.BigEndian.PutUint16(tcp[14:16], 65535)
	buf.Write(tcp[:])
	buf.Write(payload)
	return buf.Bytes()
}

// BenchmarkExtractL7 是每帧一次的解封装热路径（gopacket DecodeLayers）。
func BenchmarkExtractL7(b *testing.B) {
	frame := benchEthernetFrame(net.IPv4(10, 0, 0, 1), net.IPv4(10, 0, 0, 2), 443, 51234, bytes.Repeat([]byte{'y'}, 1000))
	b.ReportAllocs()
	for b.Loop() {
		seg, ok := ExtractL7(frame, 1)
		if !ok || !seg.IsTCP {
			b.Fatal("frame must extract")
		}
	}
}
