package sdk

import (
	"fmt"
	"io"

	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
)

// DecodeFuncV2 是 V2 解码回调的签名。
// 插件通过 stream.Send 发送零个或多个 DecodeResponseV2 结果消息，
// 最后发送一个 done=true 的消息标记该 input_id 的结果已全部发完。
// 如果完全没有结果，应至少发送一个 {input_id, done=true} 消息。
type DecodeFuncV2 func(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error

// Decoder 实现 pb.DecoderServer。
// decodeFuncV2 是可选的 V2 解码回调；如果为 nil，则 DecodeV2 返回 Unimplemented。
type Decoder struct {
	pb.UnimplementedDecoderServer
	decodeFuncV2 DecodeFuncV2
}

// DecodeV2 implements pb.DecoderServer.DecodeV2.
// If decodeFuncV2 is nil, returns Unimplemented.
func (d *Decoder) DecodeV2(stream pb.Decoder_DecodeV2Server) error {
	if d.decodeFuncV2 == nil {
		return fmt.Errorf("DecodeV2 not implemented")
	}

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		func() {
			defer func() {
				if r := recover(); r != nil {
					_ = stream.Send(&pb.DecodeResponseV2{
						InputId: req.GetInputId(),
						Error:   fmt.Sprintf("decoder panic: %v", r),
						Done:    true,
					})
				}
			}()
			if err := d.decodeFuncV2(req, stream); err != nil {
				_ = stream.Send(&pb.DecodeResponseV2{
					InputId: req.GetInputId(),
					Error:   err.Error(),
					Done:    true,
				})
			}
		}()
	}
}
