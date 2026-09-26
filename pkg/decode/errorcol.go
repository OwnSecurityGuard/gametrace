package decode

// 解码失败的原因过去只留在日志里 —— 插件侧的 r.Error 甚至完全静默（见
// convertResultsToEvents），前端只能看到一个计数。
//
// 这里把失败按「归一化后的错误模板」聚合成**有界**的分组：同类错误只占一组，
// 每组留一条原文样本 + 一个代表包。这样即使一个会话里几十万条包全解不开，
// 界面也只多出几行，而不是几十万行日志。
//
// 归一化（= 指纹）做的事：把随包变化的片段替换成占位符，只保留错误的"形状"。
//
//	unexpected EOF at offset 1234          ┐
//	unexpected EOF at offset 98765         ┘→ 同一组 "unexpected EOF at offset <n>"
//
// 分类的粒度是刻意选的：offset/长度/地址这些每次都不一样，但"错在哪一类"才是
// 用户要的答案。过度归并（把所有错并成一条）会让原因不可见，所以模板原文照存。

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// defaultMaxErrorGroups 是保留的错误种类上限。超过后新种类归入 other 桶，
	// 只累加计数（保证"有别的错"这件事本身可见，且内存有界）。
	defaultMaxErrorGroups = 32
	// errSampleMaxLen 限制进入指纹与样本的原始文本长度，防插件回吐超长串。
	errSampleMaxLen = 512
	// errTemplateMaxLen 限制模板长度（模板要回传前端展示）。
	errTemplateMaxLen = 240
	// overflowFingerprint 是"种类超限"桶的固定指纹。
	overflowFingerprint = "overflow"
	overflowTemplate    = "其他解码错误（种类已超上限，仅计数）"

	// ErrKindPlugin 表示插件主动报错（响应里带 Error），即"这条数据我解不了"。
	ErrKindPlugin = "plugin"
	// ErrKindTransport 表示链路层失败（流断开、请求发送失败、等待超时）。
	ErrKindTransport = "transport"
	// ErrKindBinding 表示解码器根本没接上：会话绑定的插件解析不到可用实例
	//（未启动 / 已离线 / owner 与项目不符）。它按**状态跳变**记一次而非每包，
	// 因为"0 事件 + 0 解码错误"曾是全链路最静默的失败形态。
	ErrKindBinding = "binding"
)

// 归一化规则按"从具体到宽泛"排列，顺序不能换：
// UUID 必须先于 hex（否则被 [0-9a-f]{8,} 吃掉），地址必须先于数字
// （否则 10.0.0.7:8080 会被拆成 <n>.<n>.<n>.<n>:<n>）。
var (
	reUUID = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reAddr = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d+)?\b`)
	reHex  = regexp.MustCompile(`\b0[xX][0-9a-fA-F]+\b|\b[0-9a-fA-F]{8,}\b`)
	reNum  = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
	reWS   = regexp.MustCompile(`\s+`)
)

// DecodeErrorGroup 是一类解码失败的聚合结果。
type DecodeErrorGroup struct {
	// Fingerprint 是模板的短哈希，作为稳定标识（同一类错误跨会话可比）。
	Fingerprint string
	// Kind 区分插件主动报错、链路层失败与解码器未接入，见 ErrKindPlugin /
	// ErrKindTransport / ErrKindBinding。
	Kind string
	// Template 是归一化后的错误模板，例如 "unknown message type <n>"。
	Template string
	// Sample 是首条原始错误文本（截断），用于定位真实原因。
	Sample string
	// SampleRawID / SampleSrc / SampleDst 是该组的代表包，前端可据此下钻 raw_packets。
	SampleRawID string
	SampleSrc   string
	SampleDst   string
	// Count 是该类错误出现的总次数。
	Count int64
	// FirstSeen / LastSeen 是首次与末次出现时间。
	FirstSeen time.Time
	LastSeen  time.Time
}

// ErrorCollector 并发安全地聚合解码失败。零值不可用，用 NewErrorCollector 构造。
type ErrorCollector struct {
	mu      sync.Mutex
	groups  map[string]*DecodeErrorGroup
	order   []string // 首见顺序，保证同次数时展示稳定
	total   int64
	maxSize int
	// now 可在测试里替换，避免依赖真实时钟。
	now func() time.Time
}

// ErrorCollectorOption 配置 ErrorCollector。
type ErrorCollectorOption func(*ErrorCollector)

// WithMaxErrorGroups 覆盖保留的错误种类上限（<=0 时用默认值）。
func WithMaxErrorGroups(n int) ErrorCollectorOption {
	return func(c *ErrorCollector) {
		if n > 0 {
			c.maxSize = n
		}
	}
}

// NewErrorCollector 创建错误聚合器。
func NewErrorCollector(opts ...ErrorCollectorOption) *ErrorCollector {
	c := &ErrorCollector{
		groups:  make(map[string]*DecodeErrorGroup),
		maxSize: defaultMaxErrorGroups,
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Add 记一次解码失败。nil 接收者是安全的空操作，调用方不必到处判空。
func (c *ErrorCollector) Add(kind, rawID, src, dst, msg string) {
	if c == nil {
		return
	}
	tpl := errorTemplate(msg)
	fp := errorFingerprint(kind, tpl)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.total++
	now := c.now()
	g, ok := c.groups[fp]
	if !ok && fp != overflowFingerprint && len(c.groups) >= c.maxSize {
		// 种类爆了：并进 other 桶。模板换成说明文案 —— 若把最后一条错误的模板
		// 当作桶名，会让人以为"所有其它错误"都是那一种。
		fp = overflowFingerprint
		tpl = overflowTemplate
		g, ok = c.groups[fp]
	}
	if !ok {
		g = &DecodeErrorGroup{
			Fingerprint: fp,
			Kind:        kind,
			Template:    tpl,
			FirstSeen:   now,
			LastSeen:    now,
		}
		c.groups[fp] = g
		c.order = append(c.order, fp)
	}
	g.Count++
	g.LastSeen = now
	// 每组只留首条样本：样本是给人看的线索，不是全量日志。
	if g.Sample == "" {
		g.Sample = truncateRunes(msg, errSampleMaxLen)
		g.SampleRawID = rawID
		g.SampleSrc = src
		g.SampleDst = dst
	}
}

// Groups 返回全部错误分组，按次数降序（同次数按首见顺序）。
func (c *ErrorCollector) Groups() []DecodeErrorGroup {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]DecodeErrorGroup, 0, len(c.groups))
	for _, fp := range c.order {
		if g, ok := c.groups[fp]; ok {
			out = append(out, *g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// Total 返回失败总次数（所有分组之和）。
func (c *ErrorCollector) Total() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// Kinds 返回已聚合的错误种类数（含 other 桶）。
func (c *ErrorCollector) Kinds() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.groups)
}

// errorTemplate 把错误文本归一化成只保留"形状"的模板。
func errorTemplate(msg string) string {
	s := truncateRunes(msg, errSampleMaxLen)
	s = reUUID.ReplaceAllString(s, "<id>")
	s = reAddr.ReplaceAllString(s, "<addr>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reNum.ReplaceAllString(s, "<n>")
	s = strings.TrimSpace(reWS.ReplaceAllString(s, " "))
	if s == "" {
		// 空错误文本也要可分组，否则所有空错误都算不同种类。
		return "(empty error)"
	}
	return truncateRunes(s, errTemplateMaxLen)
}

// errorFingerprint 对 (kind, 模板) 取短哈希。kind 参与计算，避免同文案的
// 插件错误与链路错误被并成一组（两者的处理动作完全不同）。
func errorFingerprint(kind, template string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + template))
	return hex.EncodeToString(sum[:8])
}

// truncateRunes 按 rune 截断，避免把 UTF-8 字符切成半个。
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
