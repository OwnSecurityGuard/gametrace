package main

import (
	"fmt"

	"github.com/OwnSecurityGuard/gametrace/sdk/framing"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// maxStreamBytes caps per-flow reassembly memory. A flow whose bytes never
// parse (e.g. a non-WebSocket protocol on the port) is dropped instead of
// growing without bound.
const maxStreamBytes = 4 << 20

// flowState is the per-flow WebSocket state machine: the HTTP Upgrade
// handshake is parsed first, then RFC 6455 frames (with continuation
// reassembly and per-frame counters backing the analysis channel).
type flowState struct {
	handshaken bool     // 101 响应已消费，进入帧解析
	fragment   []byte   // 分片重组缓冲区（continuation 帧拼接）
	fragOpcode byte     // 分片起始 opcode（text/binary）
	counts     flowCount
}

type flowCount struct {
	text    int64
	binary  int64
	control int64
}

func (c *flowCount) total() int64 { return c.text + c.binary + c.control }

// decoder holds all state that must survive across Decode calls: TCP
// reassembly and the per-flow handshake/frame phase machine are inherently
// cross-packet.
type decoder struct {
	reasm *framing.Reassembler
	flows map[string]*flowState // key: FlowKey.Canonical()
}

func newDecoder() *decoder {
	return &decoder{
		reasm: framing.NewReassembler(),
		flows: make(map[string]*flowState),
	}
}

// decode implements sdk.DecodeFuncV2 for one captured frame. The reassembler
// and flow states persist across calls via the decoder receiver.
func (d *decoder) decode(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
	inputID := req.GetInputId()
	defer func() {
		if r := recover(); r != nil {
			_ = stream.Send(&pb.DecodeResponseV2{
				InputId: inputID, Done: true,
				Error: fmt.Sprintf("decoder panic: %v", r),
			})
		}
	}()

	// req.Payload is a FULL link-layer frame, not L7 bytes. ExtractL7 uses
	// link_type as the framing selector. ok=false (ARP/ICMP/truncated) is a
	// normal mixed-capture outcome, not an error.
	seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
	if !ok {
		return stream.Send(&pb.DecodeResponseV2{InputId: inputID, Done: true})
	}

	// Push even empty payloads: SYN/ACK/FIN flags drive per-flow state.
	st := d.reasm.Push(seg)
	flowID := seg.Flow.Canonical()
	fs := d.flows[flowID]
	if fs == nil {
		fs = &flowState{}
		d.flows[flowID] = fs
	}

	for {
		buf := st.Bytes()
		if len(buf) == 0 {
			break
		}
		if len(buf) > maxStreamBytes {
			// Unparseable flow; drop the buffer to protect memory.
			st.Consume(len(buf))
			break
		}

		if !fs.handshaken {
			hs, n, ok := parseHandshake(buf)
			if !ok {
				// Incomplete handshake message: wait for more segments.
				break
			}
			if err := d.emitHandshake(stream, inputID, flowID, hs); err != nil {
				return err
			}
			if !hs.isRequest && hs.status == 101 {
				fs.handshaken = true // 握手完成，切换为帧解析
			}
			st.Consume(n)
			continue
		}

		f, n, ok := parseFrame(buf)
		if !ok {
			// Incomplete frame: wait for more segments on a later call.
			break
		}
		if err := d.emitFrame(stream, inputID, flowID, f); err != nil {
			return err
		}
		st.Consume(n)
	}

	// Every input must be terminated with done=true, even when nothing decoded.
	return stream.Send(&pb.DecodeResponseV2{InputId: inputID, Done: true})
}
