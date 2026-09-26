package main

import (
	"os"
	"strings"

	"github.com/OwnSecurityGuard/gametrace/sdk"
	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

func main() {
	loadDotEnv(".env")
	// 平台统一隧道模式：GT_TUNNEL / GT_AUTH_TOKEN 由运行方注入，代码只透传不判断。
	sdk.RunRegisterLoopWithOptions(dec.decodePacket, sdk.RegisterOptions{
		AuthToken: os.Getenv("GT_AUTH_TOKEN"),
	})
}

// decodePacket 是核心解码函数，签名匹配 sdk.DecodeFuncV2。
// 事件统一经 event.Draft.ToResponse 发送——五个通道（payload/meta/analysis/
// correlation/causation）由 SDK 一次带全，不手拼 DecodeResponseV2。
// 不写 defer recover：SDK 外层已有 panic 边界，插件内吞 panic 会让 done 漏发。
func (d *decoder) decodePacket(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
	drafts, err := d.Decode(req)
	if err != nil {
		return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true, Error: err.Error()})
	}
	for _, draft := range drafts {
		resp, err := draft.ToResponse(req.GetInputId())
		if err != nil {
			return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true, Error: "draft: " + err.Error()})
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
	return stream.Send(event.Done(req.GetInputId()))
}

// loadDotEnv 轻量加载 .env：不覆盖已存在的环境变量，无文件时静默跳过。
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
