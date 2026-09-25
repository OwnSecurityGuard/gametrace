// Command http-decoder is a GameTrace decoder plugin for the examples/http traffic.
//
// Pipeline: capture frame -> framing.ExtractL7 -> framing.Reassembler ->
// HTTP message -> envelope semantics -> event. It decodes HTTP/1.1 requests
// and responses on TCP, extracts the JSON envelope semantics
// (header.cmd / body.seq / body.error_code) and emits http.request /
// http.response events with Payload / Meta / Analysis channels separated.
// Protocol semantics (pair by seq, annotate push/error) are declared in
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
