package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/OwnSecurityGuard/gt-plugin-sdk/framing"
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func main() {
	// v6 frame hex from file (whitespace-stripped)
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	s := ""
	for _, c := range string(b) {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			s += string(c)
		}
	}
	run(mustHex(s), 0, "frame_linkType0(Null)")
	run(mustHex(s), 108, "frame_linkType108(Loop)")

	// also try loop(twice) family byte remap? no
	v6len := len(s)
	fmt.Printf("hex chars=%d -> bytes=%d\n", v6len, v6len/2)
}

func readFile() string {
	return ""
}

func run(raw []byte, linkType int32, tag string) {
	seg, ok := framing.ExtractL7(raw, linkType)
	if !ok {
		fmt.Printf("[%s] ExtractL7 ok=false\n", tag)
		return
	}
	fmt.Printf("[%s] ok=true IsTCP=%v Flow=%s PayloadLen=%d\n", tag, seg.IsTCP, seg.Flow.String(), len(seg.Payload))
	if len(seg.Payload) > 0 {
		fmt.Printf("[%s] payload first 24: %x\n", tag, seg.Payload[:min(24, len(seg.Payload))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}