// Package framing turns a captured DecodeRequest.payload into L7 application bytes.
//
// # Why this package exists
//
// For every pcap-based capture source the pipeline hands a plugin the *complete
// link-layer frame*, untouched:
//
//	DecodeRequest.payload = link-layer header + IP + TCP/UDP + application bytes
//
// The capture layer parses L2/L3/L4 only to fill in the context fields
// (link_type, src, dst, protocol_hint); it never trims the payload. Only the two
// proxy-class link types — ProxyPayload (1001) and TLSPlaintext (1002) — deliver
// bare L7.
//
// Every decoder therefore needs the same two things before it can parse a single
// business field: strip the encapsulation selected by link_type, and reassemble
// the TCP byte stream so that messages spanning multiple segments survive. Both
// are protocol-independent, easy to get subtly wrong, and were historically
// re-implemented (or forgotten) by each plugin. They live here instead.
//
// # Usage
//
//	var ra = framing.NewReassembler()
//
//	func decode(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
//	    seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
//	    if !ok || len(seg.Payload) == 0 {
//	        return done(stream, req) // pure ACK, handshake, or non-IP traffic
//	    }
//	    s := ra.Push(seg)
//	    for {
//	        msg, n := parseOneMessage(s.Bytes())
//	        if n == 0 {
//	            break // incomplete; wait for the next segment
//	        }
//	        s.Consume(n)
//	        emit(msg)
//	    }
//	    return done(stream, req)
//	}
//
// See contract.yaml rules payload-framing-by-link-type, link-type-selects-framing
// and tcp-reassembly-required.
package framing

import (
	"fmt"
	"net/netip"
	"sync"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
)

// TCPFlags carries the TCP control bits of the segment a payload came from.
// All fields are false for UDP and proxy-class inputs.
type TCPFlags struct {
	FIN bool
	SYN bool
	RST bool
	PSH bool
	ACK bool
	URG bool
}

// FlowKey identifies one directional byte stream. Client→server and
// server→client are deliberately distinct keys: a TCP sequence space is
// per-direction, so reassembly state must not be shared between them.
//
// The zero value is the "no flow" key, used for proxy-class inputs where the
// pipeline supplies addressing out of band.
type FlowKey struct {
	Src      netip.AddrPort
	Dst      netip.AddrPort
	Protocol string // "tcp" | "udp" | "" when unknown
}

// Reverse returns the key of the opposite direction.
func (f FlowKey) Reverse() FlowKey {
	return FlowKey{Src: f.Dst, Dst: f.Src, Protocol: f.Protocol}
}

// String renders the flow as "proto src>dst", stable enough to use as a map key
// in a plugin's own bookkeeping or as a correlation_key component.
func (f FlowKey) String() string {
	if f.Protocol == "" && !f.Src.IsValid() && !f.Dst.IsValid() {
		return "-"
	}
	return fmt.Sprintf("%s %s>%s", f.Protocol, f.Src, f.Dst)
}

// Canonical returns a direction-independent identifier: both halves of a
// conversation map to the same string. Use it to pair a request with its
// response; use FlowKey itself when you need per-direction state.
func (f FlowKey) Canonical() string {
	a, b := f.Src, f.Dst
	// Order the endpoints so either direction produces the same string.
	if b.String() < a.String() {
		a, b = b, a
	}
	return fmt.Sprintf("%s %s=%s", f.Protocol, a, b)
}

// Segment is one L7 chunk extracted from a captured frame, together with the
// transport facts a reassembler or a correlator needs.
type Segment struct {
	// Payload is the application-layer bytes. It may be empty for a pure ACK,
	// a handshake packet, or a FIN — that is normal, not an error.
	Payload []byte

	// Flow identifies the directional stream this chunk belongs to.
	Flow FlowKey

	// Seq is the TCP sequence number of Payload's first byte. Zero for UDP and
	// proxy-class inputs.
	Seq uint32

	// Flags are the TCP control bits. Zero value for UDP and proxy inputs.
	Flags TCPFlags

	// IsTCP reports whether this segment came from a TCP stream and therefore
	// participates in reassembly.
	IsTCP bool

	// LinkType echoes the link_type the segment was extracted with, so a
	// decoder can record framing provenance on the event it emits.
	LinkType int32
}

// ExtractL7 strips the encapsulation indicated by linkType and returns the
// application-layer bytes.
//
// ok is false when raw carries no transport layer this package understands —
// ARP, ICMP, a truncated frame, or an unrecognised link type. That is a normal
// outcome for a mixed capture, not a decode error: respond done=true and move on.
//
// ok is true with an empty Segment.Payload for TCP packets that carry no data
// (SYN, pure ACK, FIN). Pass those to a Reassembler anyway — it uses the flags
// to reset and retire per-flow state.
//
// The returned Payload aliases raw; copy it if you need to retain it past the
// current Decode call.
func ExtractL7(raw []byte, linkType int32) (Segment, bool) {
	seg := Segment{LinkType: linkType}
	if len(raw) == 0 {
		return seg, false
	}

	// Proxy-class inputs are already L7; there is nothing to strip and no
	// transport header to read addressing from.
	switch event.LinkType(linkType) {
	case event.LinkTypeProxyPayload, event.LinkTypeTLSPlaintext:
		seg.Payload = raw
		return seg, true
	}

	base, ok := baseLayer(linkType, raw)
	if !ok {
		return seg, false
	}

	pkt := gopacket.NewPacket(raw, base, gopacket.DecodeOptions{Lazy: true, NoCopy: true})
	if pkt == nil {
		return seg, false
	}

	srcIP, dstIP, ok := networkAddrs(pkt)
	if !ok {
		return seg, false
	}

	if l := pkt.Layer(layers.LayerTypeTCP); l != nil {
		tcp, _ := l.(*layers.TCP)
		if tcp == nil {
			return seg, false
		}
		seg.Payload = tcp.LayerPayload()
		seg.Seq = tcp.Seq
		seg.IsTCP = true
		seg.Flags = TCPFlags{FIN: tcp.FIN, SYN: tcp.SYN, RST: tcp.RST, PSH: tcp.PSH, ACK: tcp.ACK, URG: tcp.URG}
		seg.Flow = FlowKey{
			Src:      netip.AddrPortFrom(srcIP, uint16(tcp.SrcPort)),
			Dst:      netip.AddrPortFrom(dstIP, uint16(tcp.DstPort)),
			Protocol: "tcp",
		}
		return seg, true
	}

	if l := pkt.Layer(layers.LayerTypeUDP); l != nil {
		udp, _ := l.(*layers.UDP)
		if udp == nil {
			return seg, false
		}
		seg.Payload = udp.LayerPayload()
		seg.Flow = FlowKey{
			Src:      netip.AddrPortFrom(srcIP, uint16(udp.SrcPort)),
			Dst:      netip.AddrPortFrom(dstIP, uint16(udp.DstPort)),
			Protocol: "udp",
		}
		return seg, true
	}

	// IP packet with a transport this package does not handle (ICMP, SCTP...).
	return seg, false
}

// baseLayer maps a link_type to the gopacket layer the frame starts with.
//
// The mapping is the authoritative one from contract.yaml link_types, which in
// turn mirrors event/event.go. Both loopback encodings (Null=0, host byte order;
// Loop=108, network byte order) decode through gopacket's Loopback layer, which
// probes both orderings — this is why a hand-written "skip 4 bytes" works on one
// machine and fails on another.
func baseLayer(linkType int32, raw []byte) (gopacket.LayerType, bool) {
	switch event.LinkType(linkType) {
	case event.LinkTypeNull, event.LinkTypeLoop:
		return layers.LayerTypeLoopback, true
	case event.LinkTypeEthernet:
		return layers.LayerTypeEthernet, true
	case event.LinkTypeLinuxSLL:
		return layers.LayerTypeLinuxSLL, true
	case event.LinkTypeIEEE80211:
		return layers.LayerTypeDot11, true
	case event.LinkTypeIPv4:
		return layers.LayerTypeIPv4, true
	case event.LinkTypeIPv6:
		return layers.LayerTypeIPv6, true
	case event.LinkTypeRaw, event.LinkTypeRawIP:
		// No link header: the first nibble of the IP header selects the version.
		if raw[0]>>4 == 6 {
			return layers.LayerTypeIPv6, true
		}
		return layers.LayerTypeIPv4, true
	default:
		return 0, false
	}
}

// networkAddrs pulls the source/destination IP out of whichever network layer
// the packet carries.
func networkAddrs(pkt gopacket.Packet) (src, dst netip.Addr, ok bool) {
	if l := pkt.Layer(layers.LayerTypeIPv4); l != nil {
		ip, _ := l.(*layers.IPv4)
		if ip == nil {
			return src, dst, false
		}
		s, sok := netip.AddrFromSlice(ip.SrcIP.To4())
		d, dok := netip.AddrFromSlice(ip.DstIP.To4())
		return s, d, sok && dok
	}
	if l := pkt.Layer(layers.LayerTypeIPv6); l != nil {
		ip, _ := l.(*layers.IPv6)
		if ip == nil {
			return src, dst, false
		}
		s, sok := netip.AddrFromSlice(ip.SrcIP.To16())
		d, dok := netip.AddrFromSlice(ip.DstIP.To16())
		return s, d, sok && dok
	}
	return src, dst, false
}

// maxOOB caps the number of out-of-order segments a flow buffers while waiting
// for a gap to be filled. Beyond this, the oldest pending segment is dropped —
// a deliberate trade: we prefer to make forward progress over perfectly
// reconstructing a stream with a large, persistent hole (rare in captures).
const maxOOB = 4096

// seqLess reports whether a sorts before b in the modulo-2^32 TCP sequence
// space. Plain uint32 subtraction produces the correct signed difference on
// wraparound, so the cast to int32 is the canonical comparison.
func seqLess(a, b uint32) bool { return int32(a-b) < 0 }

// flowState is the reassembly buffer for one directional stream. The bytes held
// in buf are contiguous: buf[0] is sequence base, and buf[len(buf)] is the next
// sequence number the peer is expected to send. Segments that arrive beyond a
// gap are parked in oob until the frontier reaches them.
type flowState struct {
	base    uint32 // sequence number of buf[0]
	buf     []byte // contiguous, in-order bytes from base
	baseSet bool   // base has been established by SYN or first data segment
	oob     []pendingSeg
	finSeen bool
	finSeq  uint32
}

// pendingSeg is an out-of-order segment waiting for an earlier gap to close.
type pendingSeg struct {
	seq  uint32
	data []byte
}

// place inserts data that starts at sequence seq into the buffer, trimming any
// part that falls before base and de-duplicating overlaps. Segments that start
// beyond the current frontier are parked in oob.
func (fs *flowState) place(seq uint32, data []byte) {
	if len(data) == 0 {
		return
	}
	if !fs.baseSet {
		fs.base = seq
		fs.baseSet = true
	}
	if seqLess(seq, fs.base) {
		behind := fs.base - seq
		if behind >= uint32(len(data)) {
			return // entirely behind base: already consumed or a stale retransmit
		}
		data = data[behind:]
		seq = fs.base
	}
	off := seq - fs.base
	if off > uint32(len(fs.buf)) {
		fs.oob = append(fs.oob, pendingSeg{seq: seq, data: data})
		if len(fs.oob) > maxOOB {
			fs.oob = fs.oob[1:] // drop oldest pending to bound memory
		}
		return
	}
	if off == uint32(len(fs.buf)) {
		fs.buf = append(fs.buf, data...)
		return
	}
	// partial overlap: keep buf, append only the suffix past the frontier
	beyond := uint32(len(fs.buf)) - off
	if beyond >= uint32(len(data)) {
		return // fully duplicate
	}
	fs.buf = append(fs.buf, data[beyond:]...)
}

// drain re-admits any parked out-of-order segments that the current frontier now
// reaches. It loops until a full pass makes no progress, so several queued
// segments can slot in behind one newly arrived one.
func (fs *flowState) drain() {
	for {
		advanced := false
		kept := fs.oob[:0]
		for _, seg := range fs.oob {
			if seqLess(seg.seq, fs.base) {
				continue // behind base: stale, drop
			}
			off := seg.seq - fs.base
			if off > uint32(len(fs.buf)) {
				kept = append(kept, seg)
				continue
			}
			if off == uint32(len(fs.buf)) {
				fs.buf = append(fs.buf, seg.data...)
				advanced = true
				continue
			}
			beyond := uint32(len(fs.buf)) - off
			if beyond < uint32(len(seg.data)) {
				fs.buf = append(fs.buf, seg.data[beyond:]...)
				advanced = true
				continue
			}
			advanced = true // fully duplicate, drop
		}
		fs.oob = kept
		if !advanced {
			return
		}
	}
}

// consume advances the frontier by n bytes, dropping the consumed prefix. It is
// called by Stream.Consume after the caller has parsed and emitted n bytes.
func (fs *flowState) consume(n int) {
	if n <= 0 {
		return
	}
	if n >= len(fs.buf) {
		fs.base += uint32(len(fs.buf))
		fs.buf = fs.buf[:0]
		return
	}
	fs.base += uint32(n)
	fs.buf = fs.buf[n:]
}

// Reassembler reassembles the per-flow TCP byte stream across captured segments
// so that application messages spanning multiple packets (or delivered
// out-of-order, or retransmitted) survive intact.
//
// A single Reassembler is safe to share across the whole plugin: it keys state
// by FlowKey, one buffer per directional stream. UDP and proxy-class inputs are
// passed through untouched, since they carry self-contained payloads.
//
// Concurrency: Push, Stream.Bytes, Stream.Consume, Forget and Reset are
// goroutine-safe, but the parse loop for one flow must stay sequential — call
// Bytes, parse, Consume, repeat on the same goroutine that feeds Push, because
// Consume acts on the bytes Bytes just handed you.
type Reassembler struct {
	mu    sync.Mutex
	flows map[FlowKey]*flowState
}

// NewReassembler returns an empty reassembler ready to receive segments.
func NewReassembler() *Reassembler {
	return &Reassembler{flows: make(map[FlowKey]*flowState)}
}

// Stream is the in-order bytes currently available for one directional flow,
// plus the ability to mark bytes the caller has consumed.
//
// Bytes returns the contiguous prefix that has been reassembled so far. It
// returns nil for proxy-class / UDP inputs that carried no payload. The slice is
// valid until the next Push for this flow or the next Consume, so parse it
// immediately and do not retain it.
//
// Consume(n) drops the first n bytes from the front of the stream, advancing the
// reassembly frontier. Pass the number of bytes your parser emitted.
type Stream struct {
	ra  *Reassembler
	key FlowKey // zero FlowKey means a detached (non-TCP) stream
	fs  *flowState
}

// Push folds one Segment into the reassembly state and returns the Stream for
// that flow.
//
//   - A pure ACK, SYN, or FIN (no data) returns a Stream whose Bytes is empty;
//     its flags are still applied (SYN seeds the sequence base, FIN retires the
//     flow once drained).
//   - A TCP segment with data returns the contiguously reassembled prefix, which
//     may be empty if a preceding gap has not yet been filled.
//   - UDP and proxy-class inputs bypass reassembly: Bytes returns the whole
//     payload and Consume is a no-op on shared state.
func (ra *Reassembler) Push(seg Segment) Stream {
	if !seg.IsTCP {
		cp := append([]byte(nil), seg.Payload...)
		return Stream{ra: ra, fs: &flowState{base: 0, buf: cp, baseSet: true}}
	}

	ra.mu.Lock()
	defer ra.mu.Unlock()

	fs := ra.flows[seg.Flow]
	if fs == nil || fs.finSeen {
		// New flow, or a new connection reusing a 5-tuple after the previous
		// one's FIN was fully drained.
		fs = &flowState{}
		ra.flows[seg.Flow] = fs
	}

	if seg.Flags.RST {
		delete(ra.flows, seg.Flow)
		return Stream{ra: ra, key: seg.Flow, fs: &flowState{baseSet: true}}
	}

	if seg.Flags.SYN {
		// SYN consumes one sequence number; the first data byte is seq+1.
		fs.base = seg.Seq + 1
		fs.baseSet = true
		if len(seg.Payload) > 0 {
			fs.place(seg.Seq+1, seg.Payload) // TCP Fast Open
			fs.drain()
		}
		return Stream{ra: ra, key: seg.Flow, fs: fs}
	}

	if len(seg.Payload) > 0 {
		fs.place(seg.Seq, seg.Payload)
		fs.drain()
	}
	if seg.Flags.FIN {
		fs.finSeen = true
		if len(seg.Payload) > 0 {
			fs.finSeq = seg.Seq + uint32(len(seg.Payload))
		} else {
			fs.finSeq = seg.Seq
		}
	}
	if fs.finSeen && len(fs.buf) == 0 {
		delete(ra.flows, seg.Flow)
	}
	return Stream{ra: ra, key: seg.Flow, fs: fs}
}

// Bytes returns the contiguous, reassembled prefix available for this flow.
func (s Stream) Bytes() []byte {
	if s.fs == nil {
		return nil
	}
	s.ra.mu.Lock()
	defer s.ra.mu.Unlock()
	return s.fs.buf
}

// Consume drops the first n bytes from the front of the stream, advancing the
// reassembly frontier. A finished flow (FIN, nothing left) is retired.
func (s Stream) Consume(n int) {
	if s.fs == nil || s.ra == nil {
		return
	}
	s.ra.mu.Lock()
	defer s.ra.mu.Unlock()
	s.fs.consume(n)
	if s.key != (FlowKey{}) && s.fs.finSeen && len(s.fs.buf) == 0 {
		delete(s.ra.flows, s.key)
	}
}

// Forget drops the reassembly state for one flow without waiting for a FIN. Use
// it when the application layer tells you a connection closed (e.g. an HTTP
// "Connection: close" response) so a reused 5-tuple starts clean.
func (ra *Reassembler) Forget(key FlowKey) {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	delete(ra.flows, key)
}

// Reset clears all reassembly state. Useful when a capture run ends and a new
// one begins on the same plugin instance.
func (ra *Reassembler) Reset() {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	ra.flows = make(map[FlowKey]*flowState)
}
