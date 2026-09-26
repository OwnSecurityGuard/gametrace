// Package sdk 是 GameTrace 解码插件 SDK：为外部插件承载 plugin.yaml 清单的
// 解析与校验、注册心跳、DecodeV2 gRPC 服务与隧道，使插件只需提供解码函数。
package sdk

import (
	"fmt"
	"io"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
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

// NewDecoder 返回承载给定解码回调的 pb.DecoderServer。
// 常规插件路径是 RunRegisterLoop（注册 + 心跳 + 服务由 SDK 托管）；
// 本构造函数供插件作者在自己的集成测试里把解码实现挂到真实的 gRPC 服务器上
// （含 DecodeV2 的 panic 恢复与 done 语义），或自行组装 grpc.Server。
func NewDecoder(decodeFuncV2 DecodeFuncV2) *Decoder {
	return &Decoder{decodeFuncV2: decodeFuncV2}
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
