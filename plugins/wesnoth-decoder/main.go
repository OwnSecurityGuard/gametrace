package main

import (
	"os"

	"github.com/OwnSecurityGuard/gt-plugin-sdk"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/event"
	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
)

func main() {
	d := newDecoder()
	// gt-agent 托管模式会注入 GT_TUNNEL=1 / GT_AUTH_TOKEN；手动启动需自行设置
	// 这两个变量（token 不内置任何回退值，避免凭据泄露）。
	sdk.RunRegisterLoopWithOptions(d.decodePacket, sdk.RegisterOptions{
		Tunnel:    os.Getenv("GT_TUNNEL") != "",
		AuthToken: "gt_tok_change_me",
	})
}

// decodePacket 实现 sdk.DecodeFuncV2（gt.decoder/v2）。
// req.Payload 在 pcap 路径下是完整链路层帧，先经 framing.ExtractL7 剥头、
// Reassembler 重组，再按 wesnoth 帧格式解码。每个 input 必须以 done=true
// 收尾，即使一条消息都没解出来（契约 done-required）。
func (d *decoder) decodePacket(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
	events, err := d.Decode(req)
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
		mp, mErr := event.ValueFromMap(e.Payload).MarshalMsgpack()
		if mErr != nil {
			return stream.Send(&pb.DecodeResponseV2{
				InputId: req.GetInputId(),
				Done:    true,
				Error:   "marshal: " + mErr.Error(),
			})
		}
		var metaData []byte
		if len(e.Meta) > 0 {
			md, me := event.ValueFromMap(e.Meta).MarshalMsgpack()
			if me != nil {
				return stream.Send(&pb.DecodeResponseV2{
					InputId: req.GetInputId(),
					Done:    true,
					Error:   "marshal meta: " + me.Error(),
				})
			}
			metaData = md
		}
		if err := stream.Send(&pb.DecodeResponseV2{
			InputId:        req.GetInputId(),
			EventType:      e.EventType,
			SchemaId:       e.SchemaID,
			PayloadMsgpack: mp,
			MetaMsgpack:    metaData,
			CorrelationKey: e.CorrelationKey,
		}); err != nil {
			return err
		}
	}

	return stream.Send(event.Done(req.GetInputId()))
}
