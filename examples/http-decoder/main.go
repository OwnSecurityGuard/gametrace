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
	"github.com/OwnSecurityGuard/gametrace/sdk"
)

func main() {
	sdk.RunRegisterLoop(newDecoder().decode)
}
