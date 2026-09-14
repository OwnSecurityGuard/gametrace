// Command lp-decoder is a GameTrace decoder plugin for the examples/lp traffic:
// a length-prefixed framing (LPF) template protocol carrying JSON envelopes.
//
// Pipeline: capture frame -> framing.ExtractL7 -> framing.Reassembler ->
// per-flow length-prefix parse (1/2/4-byte length fields, big or little
// endian, chosen per frame) -> event. It emits lp.request / lp.response
// events with Payload / Meta / Analysis channels separated.
//
// ⚠️ Template intent: this decoder demonstrates ONE stream-framing technique
// (length-prefixed) and is NOT a universal model. Direction is derived from
// the message identity (cmd 1001 = request, 1002/2001 = response-side), which
// is only valid because the template protocol has distinct request/response
// command ids — see example directives in http-decoder (HTTP method line) and
// ws-decoder (MASK bit) for two other direction sources. Adapt the framing AND
// the direction rule to the real protocol before use.
package main

import (
	"github.com/OwnSecurityGuard/gametrace/sdk"
)

func main() {
	sdk.RunRegisterLoop(newDecoder().decode)
}
