package main

import "testing"

// BenchmarkParseFrame 是抓包热路径的帧解析基准（每帧至少一次）。
// 覆盖 server→client 裸帧与 client→server 掩码帧两种形态。
func BenchmarkParseFrame(b *testing.B) {
	cases := map[string][]byte{
		"unmasked_1k": rawFrame(wsOpText, true, false, make([]byte, 1024)),
		"masked_1k":   rawFrame(wsOpText, true, true, make([]byte, 1024)),
	}
	for name, buf := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			// 故意不调用 SetBytes：多出的 MB/s 列会破坏 benchstat 的列一致性。
			for b.Loop() {
				if _, n, ok := parseFrame(buf); !ok || n != len(buf) {
					b.Fatalf("parse: ok=%v n=%d", ok, n)
				}
			}
		})
	}
}
