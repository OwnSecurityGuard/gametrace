package decode

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestErrorTemplateNormalizesVolatileParts(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "数字",
			msg:  "unexpected EOF at offset 1234",
			want: "unexpected EOF at offset <n>",
		},
		{
			name: "IPv4 与端口",
			msg:  "dial tcp 10.0.0.7:8080: connection refused",
			want: "dial tcp <addr>: connection refused",
		},
		{
			name: "UUID",
			msg:  "session 01a0b4c6-930a-7464-89bc-b8a0c30bb6b6 not found",
			want: "session <id> not found",
		},
		{
			name: "十六进制",
			msg:  "bad magic 0xdeadbeef in frame",
			want: "bad magic <hex> in frame",
		},
		{
			name: "长 hex 串",
			msg:  "checksum a1b2c3d4e5f60718 mismatch",
			want: "checksum <hex> mismatch",
		},
		{
			name: "空白压缩",
			msg:  "decode\tfailed\n  badly",
			want: "decode failed badly",
		},
		{
			name: "空消息",
			msg:  "",
			want: "(empty error)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorTemplate(tc.msg); got != tc.want {
				t.Errorf("errorTemplate(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// 核心诉求：随包变化的片段不能把同一类错误拆成无数组。
func TestErrorCollectorGroupsSameKindOfError(t *testing.T) {
	c := NewErrorCollector()
	for i := 100; i < 130; i++ {
		c.Add(ErrKindPlugin, fmt.Sprintf("raw-%d", i), "10.0.0.1", "10.0.0.2",
			fmt.Sprintf("unexpected EOF at offset %d", i*7))
	}

	groups := c.Groups()
	if len(groups) != 1 {
		t.Fatalf("分组数 = %d, want 1（同一类错误只应占一组）", len(groups))
	}
	if groups[0].Count != 30 {
		t.Errorf("Count = %d, want 30", groups[0].Count)
	}
	if c.Total() != 30 {
		t.Errorf("Total = %d, want 30", c.Total())
	}
	// 样本保留首条原文（未归一化），这样才有排查价值。
	if !strings.Contains(groups[0].Sample, "offset 700") {
		t.Errorf("Sample = %q, want 首条原文含 offset 700", groups[0].Sample)
	}
	if groups[0].SampleRawID != "raw-100" {
		t.Errorf("SampleRawID = %q, want raw-100", groups[0].SampleRawID)
	}
	if groups[0].Count != 30 || groups[0].Sample == "" {
		t.Error("样本应只记一次且保留首条")
	}
}

func TestErrorCollectorSeparatesDifferentKinds(t *testing.T) {
	c := NewErrorCollector()
	c.Add(ErrKindPlugin, "r1", "a", "b", "stream ended early")
	c.Add(ErrKindTransport, "r2", "a", "b", "stream ended early")

	if c.Kinds() != 2 {
		t.Fatalf("种类数 = %d, want 2（同文案不同来源不应合并）", c.Kinds())
	}
}

func TestErrorCollectorCapsGroupCount(t *testing.T) {
	c := NewErrorCollector(WithMaxErrorGroups(3))
	// 注意每个错误必须"形状"不同：带编号的文案会被归一化进同一组。
	words := []string{"alpha", "beta", "gamma", "delta", "epsilon",
		"zeta", "eta", "theta", "iota", "kappa"}
	for _, w := range words {
		c.Add(ErrKindPlugin, "r", "a", "b", w+" failure")
	}

	groups := c.Groups()
	if len(groups) != 4 { // 3 个具名 + other
		t.Fatalf("分组数 = %d, want 4（3 个具名 + other 桶）", len(groups))
	}
	if c.Total() != int64(len(words)) {
		t.Errorf("Total = %d, want %d（超额错误必须仍然计数）", c.Total(), len(words))
	}

	var overflow *DecodeErrorGroup
	for i := range groups {
		if groups[i].Fingerprint == overflowFingerprint {
			overflow = &groups[i]
		}
	}
	if overflow == nil {
		t.Fatal("缺少 other 桶：种类超限后必须可见")
	}
	if overflow.Count != int64(len(words)-3) {
		t.Errorf("other 桶 Count = %d, want %d", overflow.Count, len(words)-3)
	}
	if overflow.Template != overflowTemplate {
		t.Errorf("other 桶 Template = %q，不应借用最后一条错误的模板", overflow.Template)
	}
}

func TestErrorCollectorOrdersByCount(t *testing.T) {
	c := NewErrorCollector()
	// 首见顺序：少见的在前；期望展示时按次数降序，不受首见顺序影响。
	c.Add(ErrKindPlugin, "r", "a", "b", "rare one")
	for i := 0; i < 5; i++ {
		c.Add(ErrKindPlugin, "r", "a", "b", "common one")
	}

	groups := c.Groups()
	if len(groups) != 2 {
		t.Fatalf("分组数 = %d, want 2", len(groups))
	}
	if groups[0].Count != 5 || groups[1].Count != 1 {
		t.Errorf("顺序 = (%d, %d), want (5, 1)：应按次数降序", groups[0].Count, groups[1].Count)
	}
}

func TestErrorCollectorTracksFirstAndLastSeen(t *testing.T) {
	c := NewErrorCollector()
	base := time.Unix(1700000000, 0)
	step := 0
	c.now = func() time.Time {
		step++
		return base.Add(time.Duration(step) * time.Second)
	}

	// 两条属于同一模板（"boom <n>"），所以是同一组的首末次出现。
	c.Add(ErrKindPlugin, "r1", "a", "b", "boom 1")
	c.Add(ErrKindPlugin, "r2", "a", "b", "boom 2")

	g := c.Groups()[0]
	if g.Count != 2 {
		t.Fatalf("Count = %d, want 2", g.Count)
	}
	if !g.FirstSeen.Equal(base.Add(time.Second)) {
		t.Errorf("FirstSeen = %v, want %v", g.FirstSeen, base.Add(time.Second))
	}
	if !g.LastSeen.Equal(base.Add(2 * time.Second)) {
		t.Errorf("LastSeen = %v, want %v", g.LastSeen, base.Add(2*time.Second))
	}
}

// nil 接收者必须是安全的：调用方（如无解码器路径）不该到处判空。
func TestErrorCollectorNilSafe(t *testing.T) {
	var c *ErrorCollector
	c.Add(ErrKindPlugin, "r", "a", "b", "boom")
	if c.Total() != 0 || c.Kinds() != 0 || c.Groups() != nil {
		t.Error("nil collector 应返回零值且不 panic")
	}
}

func TestErrorCollectorConcurrentAdd(t *testing.T) {
	c := NewErrorCollector()
	// 4 个形状不同的错误（带编号的会被归一化进同一组）。
	species := []string{"alpha", "beta", "gamma", "delta"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.Add(ErrKindPlugin, "r", "a", "b", species[j%len(species)]+" failure")
			}
		}(i)
	}
	wg.Wait()

	if c.Total() != 8*200 {
		t.Errorf("Total = %d, want %d", c.Total(), 8*200)
	}
	if c.Kinds() != len(species) {
		t.Errorf("种类数 = %d, want %d", c.Kinds(), len(species))
	}
	for _, g := range c.Groups() {
		if g.Count != 8*200/int64(len(species)) {
			t.Errorf("组 %q Count = %d, want %d", g.Template, g.Count, 8*200/len(species))
		}
	}
}
