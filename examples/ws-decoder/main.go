// Command ws-decoder is a GameTrace decoder plugin for the examples/ws traffic.
//
// Pipeline: capture frame -> framing.ExtractL7 -> framing.Reassembler ->
// per-flow state machine (HTTP Upgrade handshake -> RFC 6455 frames) -> event.
// It emits ws.handshake events for the handshake phase, then ws.text /
// ws.binary data frames (with continuation reassembly) and ws.ping / ws.pong /
// ws.close control frames. Frame direction is derived from the MASK bit.
// Protocol semantics (pair echo by seq, annotate push/error) are declared in
// plugin.yaml as semantic_rules and executed by the platform.
package main

import (
	"os"

	"github.com/OwnSecurityGuard/gametrace/sdk"
)

func main() {
	// GT_AUTH_TOKEN 由运行方注入环境变量（token 模式平台必需；匿名模式留空即匿名）。
	// 注意：Go 不会自动读目录里的 .env，需要先 source 或在启动命令前置变量。
	sdk.RunRegisterLoopWithOptions(newDecoder().decode, sdk.RegisterOptions{
		AuthToken: os.Getenv("GT_AUTH_TOKEN"),
	})
}
