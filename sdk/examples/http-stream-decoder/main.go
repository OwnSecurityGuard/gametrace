// Command http-stream-decoder is a complete, runnable GameTrace decoder plugin for
// HTTP over TCP. It demonstrates the full intended pipeline on a real protocol:
//
//	capture frame -> framing.ExtractL7 -> framing.Reassembler -> HTTP message -> event
//
// and carries a Protocol Semantic Rule declaration (semantic_rules) in plugin.yaml
// next to this file.
//
// Wire format handled:
//
//	HTTP requests  (client -> server)  -> http.request  event
//	HTTP responses (server -> client)  -> http.response event
//
// Requests and responses of the same TCP connection are correlated by the
// `http.pair_request_response` semantic rule (see plugin.yaml), not by
// CorrelationKey: connection identity belongs to the host-derived ConnID,
// CorrelationKey is reserved for business session/operation ids.
package main

import (
	"fmt"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"

	"github.com/OwnSecurityGuard/gametrace/sdk"
	"github.com/OwnSecurityGuard/gametrace/sdk/framing"
)

// maxStreamBytes caps per-flow reassembly memory. A flow whose bytes never
// parse as HTTP is dropped instead of growing without bound.
const maxStreamBytes = 4 << 20

func main() {
	sdk.RunRegisterLoop(newDecoder().decode)
}

// decoder holds all state that must survive across Decode calls: TCP
// reassembly is inherently cross-packet, and the per-flow message counters
// back the per-flow counters exposed in the event payload.
type decoder struct {
	reasm  *framing.Reassembler
	counts map[string]flowCount // key: FlowKey.Canonical()
}

type flowCount struct {
	requests  int64
	responses int64
}

func newDecoder() *decoder {
	return &decoder{
		reasm:  framing.NewReassembler(),
		counts: make(map[string]flowCount),
	}
}

// decode implements sdk.DecodeFuncV2 for one captured frame. The reassembler
// and counters persist across calls via the decoder receiver.
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
		msg, n, ok := parseMessage(buf)
		if !ok {
			// Incomplete message: wait for more segments on a later call.
			break
		}
		if msg != nil {
			if err := d.emit(stream, inputID, seg.Flow.Canonical(), msg); err != nil {
				return err
			}
		}
		st.Consume(n)
	}

	// Every input must be terminated with done=true, even when nothing decoded.
	return stream.Send(&pb.DecodeResponseV2{InputId: inputID, Done: true})
}
