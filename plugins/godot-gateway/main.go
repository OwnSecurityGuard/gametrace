package main

import (
	"github.com/OwnSecurityGuard/gt-plugin-sdk"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/event"
	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
)

func main() {
	sdk.RunRegisterLoop(decodePacket)
}

// decodePacket implements sdk.DecodeFuncV2 (gt.decoder/v2).
// req.Payload is a complete link-layer frame on pcap paths; we strip it with
// framing.ExtractL7 and reassemble the per-flow TCP stream before parsing HTTP.
func decodePacket(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
	events, err := Decode(req)
	if err != nil {
		return stream.Send(&pb.DecodeResponseV2{
			InputId: req.GetInputId(),
			Done:    true,
			Error:   err.Error(),
		})
	}

	for _, e := range events {
		// v0.8.0 契约：Payload（纯业务）与 Meta 分开传输，宿主据此填充
		// Event.Meta，前端「元信息」独立展示，不再混入业务 payload。
		payloadVal := event.ValueFromMap(e.Payload)
		mp, mErr := payloadVal.MarshalMsgpack()
		if mErr != nil {
			return stream.Send(&pb.DecodeResponseV2{
				InputId: req.GetInputId(),
				Done:    true,
				Error:   "marshal: " + mErr.Error(),
			})
		}
		var metaData []byte
		if len(e.Meta) > 0 {
			metaVal := event.ValueFromMap(e.Meta)
			metaData, mErr = metaVal.MarshalMsgpack()
			if mErr != nil {
				return stream.Send(&pb.DecodeResponseV2{
					InputId: req.GetInputId(),
					Done:    true,
					Error:   "marshal meta: " + mErr.Error(),
				})
			}
		}
		if err := stream.Send(&pb.DecodeResponseV2{
			InputId:          req.GetInputId(),
			EventType:        e.EventType,
			SchemaId:         e.SchemaID,
			PayloadMsgpack:   mp,
			MetaMsgpack:      metaData,
			CorrelationKey:   e.CorrelationKey,
			CausationInputId: e.CausationInputID,
		}); err != nil {
			return err
		}
	}

	return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true})
}
