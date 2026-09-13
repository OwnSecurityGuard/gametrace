package framing

import (
	"bytes"
	"net"
	"net/netip"
	"testing"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
)

// buildL3L4 serializes an IPv4 + TCP (or UDP) packet carrying payload, returning
// the raw L3/L4 bytes and the source/destination endpoint the layers declare.
func buildL3L4(t *testing.T, payload []byte, udp bool, seq uint32) ([]byte, netip.AddrPort, netip.AddrPort) {
	t.Helper()
	srcIP := net.ParseIP("10.0.0.1").To4()
	dstIP := net.ParseIP("10.0.0.2").To4()
	ip := &layers.IPv4{
		Version: 4, IHL: 5, TTL: 64,
		SrcIP: srcIP, DstIP: dstIP,
	}
	opts := gopacket.SerializeOptions{ComputeChecksums: true, FixLengths: true}
	buf := gopacket.NewSerializeBuffer()
	var err error
	if udp {
		ip.Protocol = layers.IPProtocolUDP
		u := &layers.UDP{SrcPort: 12345, DstPort: 80}
		u.SetNetworkLayerForChecksum(ip)
		err = gopacket.SerializeLayers(buf, opts, ip, u, gopacket.Payload(payload))
	} else {
		ip.Protocol = layers.IPProtocolTCP
		tc := &layers.TCP{SrcPort: 12345, DstPort: 80, Seq: seq, ACK: true, PSH: true}
		tc.SetNetworkLayerForChecksum(ip)
		err = gopacket.SerializeLayers(buf, opts, ip, tc, gopacket.Payload(payload))
	}
	if err != nil {
		t.Fatalf("serialize L3/L4: %v", err)
	}
	src := netip.MustParseAddrPort("10.0.0.1:12345")
	dst := netip.MustParseAddrPort("10.0.0.2:80")
	return buf.Bytes(), src, dst
}

// wrapFrame prepends the link-layer header for lt to the L3/L4 bytes.
func wrapFrame(t *testing.T, lt event.LinkType, l3l4 []byte) []byte {
	t.Helper()
	switch lt {
	case event.LinkTypeEthernet:
		eth := &layers.Ethernet{
			EthernetType: layers.EthernetTypeIPv4,
			SrcMAC:       net.HardwareAddr{1, 2, 3, 4, 5, 6},
			DstMAC:       net.HardwareAddr{6, 5, 4, 3, 2, 1},
		}
		buf := gopacket.NewSerializeBuffer()
		if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{}, eth, gopacket.Payload(l3l4)); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	case event.LinkTypeNull:
		// DLT_NULL: 4-byte family. gopacket writes it little-endian and probes
		// both orders on decode, so this is the Npcap/BSD loopback format.
		loop := &layers.Loopback{Family: layers.ProtocolFamilyIPv4}
		buf := gopacket.NewSerializeBuffer()
		if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{}, loop, gopacket.Payload(l3l4)); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	case event.LinkTypeLinuxSLL:
		// 16-byte cooked header, big-endian, protocol = IPv4 (0x0800).
		sll := []byte{0, 0, 0, 1, 0, 6, 1, 2, 3, 4, 5, 6, 0, 0, 8, 0}
		out := make([]byte, 0, len(sll)+len(l3l4))
		out = append(out, sll...)
		out = append(out, l3l4...)
		return out
	case event.LinkTypeRaw, event.LinkTypeRawIP, event.LinkTypeIPv4, event.LinkTypeIPv6:
		return l3l4 // raw IPv4 packet, no link header
	default:
		t.Fatalf("unsupported link type in test: %v", lt)
		return nil
	}
}

func TestExtractL7_TCPByLinkType(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\n")
	l3l4, src, dst := buildL3L4(t, payload, false, 1000)
	for _, lt := range []event.LinkType{
		event.LinkTypeEthernet,
		event.LinkTypeNull,
		event.LinkTypeLinuxSLL,
		event.LinkTypeRaw,
		event.LinkTypeRawIP,
	} {
		raw := wrapFrame(t, lt, l3l4)
		seg, ok := ExtractL7(raw, int32(lt))
		if !ok {
			t.Fatalf("lt=%v: ok=false (expected L7 extraction)", lt)
		}
		if !seg.IsTCP {
			t.Fatalf("lt=%v: IsTCP=false", lt)
		}
		if !bytes.Equal(seg.Payload, payload) {
			t.Fatalf("lt=%v: payload mismatch: got %q want %q", lt, seg.Payload, payload)
		}
		if seg.Flow.Protocol != "tcp" {
			t.Fatalf("lt=%v: protocol %q", lt, seg.Flow.Protocol)
		}
		if seg.Flow.Src != src || seg.Flow.Dst != dst {
			t.Fatalf("lt=%v: flow %v (want %v>%v)", lt, seg.Flow, src, dst)
		}
		if seg.Seq != 1000 {
			t.Fatalf("lt=%v: seq=%d want 1000", lt, seg.Seq)
		}
	}
}

func TestExtractL7_ProxyPassthrough(t *testing.T) {
	payload := []byte("already-L7 application bytes")
	for _, lt := range []event.LinkType{event.LinkTypeProxyPayload, event.LinkTypeTLSPlaintext} {
		seg, ok := ExtractL7(payload, int32(lt))
		if !ok {
			t.Fatalf("lt=%v: ok=false", lt)
		}
		if seg.IsTCP {
			t.Fatalf("lt=%v: IsTCP should be false for proxy-class input", lt)
		}
		if !bytes.Equal(seg.Payload, payload) {
			t.Fatalf("lt=%v: payload mismatch: got %q", lt, seg.Payload)
		}
		if seg.Flow.Protocol != "" {
			t.Fatalf("lt=%v: flow protocol should be empty, got %q", lt, seg.Flow.Protocol)
		}
	}
}

func TestExtractL7_UDP(t *testing.T) {
	payload := []byte("DNS query payload")
	l3l4, src, dst := buildL3L4(t, payload, true, 0)
	raw := wrapFrame(t, event.LinkTypeEthernet, l3l4)
	seg, ok := ExtractL7(raw, int32(event.LinkTypeEthernet))
	if !ok {
		t.Fatal("ok=false for UDP frame")
	}
	if seg.IsTCP {
		t.Fatal("IsTCP should be false for UDP")
	}
	if seg.Flow.Protocol != "udp" {
		t.Fatalf("protocol %q want udp", seg.Flow.Protocol)
	}
	if !bytes.Equal(seg.Payload, payload) {
		t.Fatalf("payload mismatch: got %q want %q", seg.Payload, payload)
	}
	if seg.Flow.Src != src || seg.Flow.Dst != dst {
		t.Fatalf("flow %v (want %v>%v)", seg.Flow, src, dst)
	}
}

func TestExtractL7_NonTCPAndEmpty(t *testing.T) {
	// Empty payload is never decodable.
	if _, ok := ExtractL7(nil, int32(event.LinkTypeEthernet)); ok {
		t.Fatal("empty input should not be ok")
	}
	// Unknown link type.
	if _, ok := ExtractL7([]byte{0x01, 0x02}, int32(99999)); ok {
		t.Fatal("unknown link type should not be ok")
	}
	// An IP packet that is neither TCP nor UDP (ICMP) is not L7-decodable.
	icmp := &layers.ICMPv4{TypeCode: layers.CreateICMPv4TypeCode(8, 0), Id: 1, Seq: 1}
	ip := &layers.IPv4{Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolICMPv4,
		SrcIP: net.ParseIP("10.0.0.1").To4(), DstIP: net.ParseIP("10.0.0.2").To4()}
	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true}, ip, icmp, gopacket.Payload([]byte("ping"))); err != nil {
		t.Fatal(err)
	}
	eth := &layers.Ethernet{EthernetType: layers.EthernetTypeIPv4,
		SrcMAC: net.HardwareAddr{1, 2, 3, 4, 5, 6}, DstMAC: net.HardwareAddr{6, 5, 4, 3, 2, 1}}
	wrap := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(wrap, gopacket.SerializeOptions{}, eth, gopacket.Payload(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if _, ok := ExtractL7(wrap.Bytes(), int32(event.LinkTypeEthernet)); ok {
		t.Fatal("ICMP frame should not be L7-decodable (ok should be false)")
	}
}

func TestFlowKey_CanonicalAndReverse(t *testing.T) {
	a := FlowKey{
		Src:      netip.MustParseAddrPort("1.2.3.4:1111"),
		Dst:      netip.MustParseAddrPort("5.6.7.8:80"),
		Protocol: "tcp",
	}
	if a.Reverse().Src != a.Dst || a.Reverse().Dst != a.Src {
		t.Fatal("Reverse did not swap endpoints")
	}
	if a.Canonical() != a.Reverse().Canonical() {
		t.Fatalf("Canonical not direction-independent: %q vs %q", a.Canonical(), a.Reverse().Canonical())
	}
	if a.String() != "tcp 1.2.3.4:1111>5.6.7.8:80" {
		t.Fatalf("String() = %q", a.String())
	}
}

func tcpFlow() FlowKey {
	return FlowKey{
		Src:      netip.MustParseAddrPort("10.0.0.1:12345"),
		Dst:      netip.MustParseAddrPort("10.0.0.2:80"),
		Protocol: "tcp",
	}
}

func TestReassembler_InOrder(t *testing.T) {
	ra := NewReassembler()
	f := tcpFlow()
	segs := []Segment{
		{Flow: f, Seq: 100, IsTCP: true, Payload: []byte("GET /"), Flags: TCPFlags{PSH: true}}, // 5 bytes -> next seq 105
		{Flow: f, Seq: 105, IsTCP: true, Payload: []byte("index"), Flags: TCPFlags{PSH: true}}, // 5 bytes -> next seq 110
		{Flow: f, Seq: 110, IsTCP: true, Payload: []byte(".html\r\n"), Flags: TCPFlags{PSH: true}},
	}
	var last Stream
	for _, s := range segs {
		last = ra.Push(s)
	}
	// Bytes() returns the contiguous buffer from the base; after all in-order
	// segments it must equal the full application payload.
	want := []byte("GET /index.html\r\n")
	if !bytes.Equal(last.Bytes(), want) {
		t.Fatalf("got %q want %q", last.Bytes(), want)
	}
}

// TestReassembler_StreamingConsume exercises the documented parse/Consume loop:
// feed segments, then drain complete space-delimited messages.
func TestReassembler_StreamingConsume(t *testing.T) {
	ra := NewReassembler()
	f := tcpFlow()
	var emitted []string
	feed := func(seg Segment) {
		s := ra.Push(seg)
		for {
			b := s.Bytes()
			idx := bytes.IndexByte(b, ' ')
			if idx < 0 {
				break
			}
			emitted = append(emitted, string(b[:idx+1]))
			s.Consume(idx + 1)
		}
	}
	feed(Segment{Flow: f, Seq: 1, IsTCP: true, Payload: []byte("one two ")})
	feed(Segment{Flow: f, Seq: 9, IsTCP: true, Payload: []byte("thr")})
	feed(Segment{Flow: f, Seq: 12, IsTCP: true, Payload: []byte("ee four ")})

	want := []string{"one ", "two ", "three ", "four "}
	if len(emitted) != len(want) {
		t.Fatalf("emitted %v want %v", emitted, want)
	}
	for i := range want {
		if emitted[i] != want[i] {
			t.Fatalf("msg %d = %q want %q (all: %v)", i, emitted[i], want[i], emitted)
		}
	}
}

func TestReassembler_OutOfOrderGapFill(t *testing.T) {
	ra := NewReassembler()
	f := tcpFlow()
	// SYN seeds the base sequence.
	ra.Push(Segment{Flow: f, Seq: 0, IsTCP: true, Flags: TCPFlags{SYN: true}})
	// High segment arrives first, leaving a gap; nothing contiguous yet.
	sHi := ra.Push(Segment{Flow: f, Seq: 4, IsTCP: true, Payload: []byte("WXY")})
	if len(sHi.Bytes()) != 0 {
		t.Fatalf("expected empty (gap unfilled), got %q", sHi.Bytes())
	}
	// Low segment fills the gap and releases both.
	sLo := ra.Push(Segment{Flow: f, Seq: 1, IsTCP: true, Payload: []byte("ABC")})
	if string(sLo.Bytes()) != "ABCWXY" {
		t.Fatalf("got %q want ABCWXY", sLo.Bytes())
	}
}

func TestReassembler_RetransmitDedup(t *testing.T) {
	ra := NewReassembler()
	f := tcpFlow()
	ra.Push(Segment{Flow: f, Seq: 1, IsTCP: true, Payload: []byte("abc")})
	// Exact retransmit of already-buffered bytes must not double them.
	ra.Push(Segment{Flow: f, Seq: 1, IsTCP: true, Payload: []byte("abc")})
	s := ra.Push(Segment{Flow: f, Seq: 4, IsTCP: true, Payload: []byte("def")})
	if string(s.Bytes()) != "abcdef" {
		t.Fatalf("got %q want abcdef", s.Bytes())
	}
}

func TestReassembler_SeqWraparound(t *testing.T) {
	ra := NewReassembler()
	f := tcpFlow()
	ra.Push(Segment{Flow: f, Seq: 0xFFFFFFF0, IsTCP: true, Payload: []byte("ab")})
	s := ra.Push(Segment{Flow: f, Seq: 0xFFFFFFF2, IsTCP: true, Payload: []byte("cd")})
	if string(s.Bytes()) != "abcd" {
		t.Fatalf("got %q want abcd (wraparound handling failed)", s.Bytes())
	}
}

func TestReassembler_FINRetires(t *testing.T) {
	ra := NewReassembler()
	f := tcpFlow()
	s := ra.Push(Segment{Flow: f, Seq: 1, IsTCP: true, Payload: []byte("data")})
	s.Consume(4) // caller drains the stream
	// FIN at seq 5 (1+4) with no payload retires the flow once drained.
	ra.Push(Segment{Flow: f, Seq: 5, IsTCP: true, Flags: TCPFlags{FIN: true}})
	// A new connection reusing the same 5-tuple starts fresh.
	s2 := ra.Push(Segment{Flow: f, Seq: 1, IsTCP: true, Payload: []byte("NEW")})
	if string(s2.Bytes()) != "NEW" {
		t.Fatalf("after FIN reuse, got %q want NEW", s2.Bytes())
	}
}

func TestReassembler_RSTClears(t *testing.T) {
	ra := NewReassembler()
	f := tcpFlow()
	ra.Push(Segment{Flow: f, Seq: 1, IsTCP: true, Payload: []byte("abc")})
	ra.Push(Segment{Flow: f, Seq: 1, IsTCP: true, Flags: TCPFlags{RST: true}})
	s := ra.Push(Segment{Flow: f, Seq: 1, IsTCP: true, Payload: []byte("xyz")})
	if string(s.Bytes()) != "xyz" {
		t.Fatalf("after RST, got %q want xyz", s.Bytes())
	}
}

func TestReassembler_MultipleFlowsIndependent(t *testing.T) {
	ra := NewReassembler()
	a := FlowKey{Src: netip.MustParseAddrPort("10.0.0.1:1111"), Dst: netip.MustParseAddrPort("10.0.0.2:80"), Protocol: "tcp"}
	b := FlowKey{Src: netip.MustParseAddrPort("10.0.0.3:2222"), Dst: netip.MustParseAddrPort("10.0.0.2:80"), Protocol: "tcp"}
	ra.Push(Segment{Flow: a, Seq: 1, IsTCP: true, Payload: []byte("AAAA")})
	ra.Push(Segment{Flow: b, Seq: 1, IsTCP: true, Payload: []byte("BBBB")})
	sa := ra.Push(Segment{Flow: a, Seq: 5, IsTCP: true, Payload: []byte("aaaa")})
	sb := ra.Push(Segment{Flow: b, Seq: 5, IsTCP: true, Payload: []byte("bbbb")})
	if string(sa.Bytes()) != "AAAAaaaa" {
		t.Fatalf("flow a: got %q want AAAAaaaa", sa.Bytes())
	}
	if string(sb.Bytes()) != "BBBBbbbb" {
		t.Fatalf("flow b: got %q want BBBBbbbb", sb.Bytes())
	}
}

// TestReassembler_UDPDatagramsIndependent verifies UDP payloads are passed
// through whole, never concatenated with the next datagram.
func TestReassembler_UDPDatagramsIndependent(t *testing.T) {
	ra := NewReassembler()
	f := FlowKey{
		Src:      netip.MustParseAddrPort("10.0.0.1:53"),
		Dst:      netip.MustParseAddrPort("10.0.0.2:53"),
		Protocol: "udp",
	}
	s := ra.Push(Segment{Flow: f, IsTCP: false, Payload: []byte("query")})
	if string(s.Bytes()) != "query" {
		t.Fatalf("got %q want query", s.Bytes())
	}
	s.Consume(100) // no effect on shared state for non-TCP inputs
	s2 := ra.Push(Segment{Flow: f, IsTCP: false, Payload: []byte("answer")})
	if string(s2.Bytes()) != "answer" {
		t.Fatalf("UDP datagrams must not concatenate, got %q", s2.Bytes())
	}
}

func TestIntegration_ExtractThenReassemble(t *testing.T) {
	ra := NewReassembler()
	payload := []byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
	l3l4, _, _ := buildL3L4(t, payload, false, 100)
	raw := wrapFrame(t, event.LinkTypeEthernet, l3l4)
	seg, ok := ExtractL7(raw, int32(event.LinkTypeEthernet))
	if !ok || !seg.IsTCP {
		t.Fatalf("ExtractL7 failed: ok=%v IsTCP=%v", ok, seg.IsTCP)
	}
	if string(seg.Payload) != string(payload) {
		t.Fatalf("ExtractL7 payload mismatch: got %q", seg.Payload)
	}
	s := ra.Push(seg)
	if string(s.Bytes()) != string(payload) {
		t.Fatalf("reassembled bytes mismatch: got %q", s.Bytes())
	}
}
