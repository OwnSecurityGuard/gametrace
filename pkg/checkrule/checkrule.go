// Package checkrule 定义项目级「检查规则」(check rule) 的共享数据模型。
//
// 一条检查规则 = 一个对解码后事件字段的条件 (rule.Predicate) + 命中后的通知文案
// + 冷却/上下文窗口参数。规则由用户在项目内配置（gt-mcp 持久化到 projects.rules
// JSON 列），抓包启动时由 gt-mcp 解析并随 StartCapture 下发到 gt-pipeline；流水线
// 在每条解码事件上求值，命中则打包「触发记录 + 各方向触发前最近 N 条」成一个
// AlertBundle，下发给抓到该数据的探针本地展示。
//
// 本包是叶子包：只依赖 stdlib 与 sdk/rule，可被 gt-mcp / gt-pipeline / gt-agent
// 三个二进制共享，不引入任何 gametrace 内部包（避免环、避免 gt-agent 拖入 pkg/event）。
package checkrule

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tidwall/gjson"

	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
)

// 校验与截断相关的上下限常量。
const (
	// DefaultCooldown 是规则未显式配置 cooldown_sec 时的冷却窗口（同规则·同会话去重）。
	DefaultCooldown = 30 * time.Second
	// MaxCooldown 是冷却窗口上限。
	MaxCooldown = 24 * time.Hour

	// DefaultContext 是每方向上下文记录的默认条数（context_per_direction 未配置时）。
	DefaultContext = 3
	// MaxContext 是每方向上下文记录条数上限。
	MaxContext = 50

	// MaxRulesPerProject 是单项目检查规则数量上限。
	MaxRulesPerProject = 50

	// MaxTitleLen / MaxMessageLen 是通知文案长度上限（rune）。
	MaxTitleLen   = 200
	MaxMessageLen = 500

	// MaxRecordDataBytes 是单条记录 data 字段的 JSON 字节上限，超出截断为字符串。
	MaxRecordDataBytes = 2048
	// MaxBundleBytes 是整个 AlertBundle 序列化后的字节上限（下发前兜底保护）。
	MaxBundleBytes = 256 << 10
)

// 校验问题的 RuleID（gt.check.* 命名空间，与 sdk 的 gt.semantic.* 区分）。
const (
	IDRequired     = "gt.check.id-required"
	IDDuplicate    = "gt.check.id-duplicate"
	NameRequired   = "gt.check.name-required"
	WhenRequired   = "gt.check.when-required"
	WhenInvalid    = "gt.check.when-invalid"
	CooldownRange  = "gt.check.cooldown-range"
	ContextRange   = "gt.check.context-range"
	TitleTooLong   = "gt.check.title-too-long"
	MessageTooLong = "gt.check.message-too-long"
	TooManyRules   = "gt.check.too-many-rules"
)

// CheckRule 是项目内的一条检查规则。
//
// 向后兼容：历史「关联规则」只有 {id, name} 两个字段。这类旧条目反序列化后
// Enabled=false 且 When 为空，会被 Active 过滤掉，且空 Predicate 求值恒 false，
// 因此存量数据无需迁移即静默失活。
type CheckRule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	// When 是命中条件（对解码事件 payload+_meta 的 GJSON 视图求值）。
	When rule.Predicate `json:"when"`

	// Title / Message 为命中后桌面通知文案；空则回退到 NotifyTitle/NotifyMessage。
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`

	// CooldownSec 是「同规则·同会话」的冷却秒数；0 = 用 DefaultCooldown。
	CooldownSec int `json:"cooldown_sec,omitempty"`
	// ContextPerDirection 是随通知下发的「每方向触发前最近 N 条」记录数；0 = 用 DefaultContext。
	ContextPerDirection int `json:"context_per_direction,omitempty"`
}

// Cooldown 返回生效的冷却窗口。
func (r CheckRule) Cooldown() time.Duration {
	if r.CooldownSec > 0 {
		return time.Duration(r.CooldownSec) * time.Second
	}
	return DefaultCooldown
}

// Context 返回生效的每方向上下文条数。
func (r CheckRule) Context() int {
	if r.ContextPerDirection > 0 {
		return r.ContextPerDirection
	}
	return DefaultContext
}

// NotifyTitle 返回桌面通知标题（Title 空则回退 Name，再回退固定文案）。
func (r CheckRule) NotifyTitle() string {
	if r.Title != "" {
		return r.Title
	}
	if r.Name != "" {
		return r.Name
	}
	return "检查规则命中"
}

// NotifyMessage 返回桌面通知正文（Message 空则给一句默认描述）。
func (r CheckRule) NotifyMessage() string {
	if r.Message != "" {
		return r.Message
	}
	return fmt.Sprintf("检查规则 %q 命中", r.Name)
}

// whenEmpty 报告 When 是否为空（无任何叶子条件与组合器）。
func (r CheckRule) whenEmpty() bool {
	w := r.When
	return len(w.All) == 0 && len(w.Any) == 0 && w.Path == "" && w.Op == "" && w.Value == nil
}

// Validate 校验单条规则。返回空切片表示合法。
//
// 启用 (Enabled) 的规则必须有合法的命中条件；禁用规则允许 When 为空（占位/草稿），
// 这样 Web 端「先建条目再填条件」或临时停用一条规则不会被校验拦下。
func (r CheckRule) Validate() []rule.Issue {
	var issues []rule.Issue
	if r.ID == "" {
		issues = append(issues, rule.Issue{RuleID: IDRequired, Path: "id",
			Message: "rule id is required", Severity: rule.SeverityError})
	}
	if r.Name == "" {
		issues = append(issues, rule.Issue{RuleID: NameRequired, Path: "name",
			Message: "rule name is required", Severity: rule.SeverityError})
	}

	switch {
	case r.whenEmpty():
		if r.Enabled {
			issues = append(issues, rule.Issue{RuleID: WhenRequired, Path: "when",
				Message: "enabled rule requires a non-empty 'when' condition", Severity: rule.SeverityError})
		}
	default:
		for _, iss := range r.When.Validate() {
			iss.Path = "when." + iss.Path
			iss.RuleID = WhenInvalid
			issues = append(issues, iss)
		}
	}

	if r.CooldownSec < 0 || time.Duration(r.CooldownSec)*time.Second > MaxCooldown {
		issues = append(issues, rule.Issue{RuleID: CooldownRange, Path: "cooldown_sec",
			Message:  fmt.Sprintf("cooldown_sec must be within [0, %d]", int(MaxCooldown/time.Second)),
			Severity: rule.SeverityError})
	}
	if r.ContextPerDirection < 0 || r.ContextPerDirection > MaxContext {
		issues = append(issues, rule.Issue{RuleID: ContextRange, Path: "context_per_direction",
			Message:  fmt.Sprintf("context_per_direction must be within [0, %d]", MaxContext),
			Severity: rule.SeverityError})
	}
	if len([]rune(r.Title)) > MaxTitleLen {
		issues = append(issues, rule.Issue{RuleID: TitleTooLong, Path: "title",
			Message:  fmt.Sprintf("title must be at most %d characters", MaxTitleLen),
			Severity: rule.SeverityError})
	}
	if len([]rune(r.Message)) > MaxMessageLen {
		issues = append(issues, rule.Issue{RuleID: MessageTooLong, Path: "message",
			Message:  fmt.Sprintf("message must be at most %d characters", MaxMessageLen),
			Severity: rule.SeverityError})
	}
	return issues
}

// RulesReport 是一组检查规则的总校验入口：逐条 Validate + ID 唯一性 + 数量上限。
// 供 gt-mcp 在 set_project_rules 保存期调用。
func RulesReport(rules []CheckRule) []rule.Issue {
	var issues []rule.Issue
	if len(rules) > MaxRulesPerProject {
		issues = append(issues, rule.Issue{RuleID: TooManyRules, Path: "rules",
			Message:  fmt.Sprintf("at most %d check rules per project, got %d", MaxRulesPerProject, len(rules)),
			Severity: rule.SeverityError})
	}
	seen := make(map[string]bool, len(rules))
	for i, r := range rules {
		prefix := fmt.Sprintf("rules[%d]", i)
		if r.ID != "" {
			if seen[r.ID] {
				issues = append(issues, rule.Issue{RuleID: IDDuplicate, Path: prefix,
					Message:  fmt.Sprintf("check rule id %q declared more than once", r.ID),
					Severity: rule.SeverityError})
			}
			seen[r.ID] = true
		}
		for _, iss := range r.Validate() {
			iss.Path = joinPath(prefix, iss.Path)
			issues = append(issues, iss)
		}
	}
	return issues
}

// HasError 报告 issues 中是否含 error 级问题。
func HasError(issues []rule.Issue) bool {
	for _, iss := range issues {
		if iss.Severity == rule.SeverityError {
			return true
		}
	}
	return false
}

// Active 过滤出会被求值的规则：已启用且命中条件非空。
func Active(rules []CheckRule) []CheckRule {
	out := make([]CheckRule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled && !r.whenEmpty() {
			out = append(out, r)
		}
	}
	return out
}

// EncodeList 把规则列表逐条序列化为 JSON 字符串（用于 proto repeated string 传输）。
func EncodeList(rules []CheckRule) []string {
	if len(rules) == 0 {
		return nil
	}
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		raw, err := json.Marshal(r)
		if err != nil {
			continue
		}
		out = append(out, string(raw))
	}
	return out
}

// DecodeList 反序列化 EncodeList 的输出；坏条目跳过。
// 返回成功解码的规则数与被丢弃的条目数（调用方据此记日志）。
func DecodeList(encoded []string) (rules []CheckRule, dropped int) {
	if len(encoded) == 0 {
		return nil, 0
	}
	rules = make([]CheckRule, 0, len(encoded))
	for _, s := range encoded {
		var r CheckRule
		if err := json.Unmarshal([]byte(s), &r); err != nil {
			dropped++
			continue
		}
		rules = append(rules, r)
	}
	return rules, dropped
}

// AlertRecord 是告警详情页里的一条解码记录（镜像 list_decoded_data 的 per-record 形状）。
//
// Data 通常是 json.RawMessage（原样内嵌为 JSON 对象）；当原始 data 超过
// MaxRecordDataBytes 时退化为截断后的字符串。
type AlertRecord struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Direction string `json:"direction"`
	Data      any    `json:"data,omitempty"`
	Meta      any    `json:"meta,omitempty"`
}

// AlertBundle 是一次命中下发给探针展示的完整数据包。
type AlertBundle struct {
	AlertID     string `json:"alert_id"`
	RuleID      string `json:"rule_id"`
	RuleName    string `json:"rule_name"`
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	GeneratedAt string `json:"generated_at"`

	// Trigger 是触发本次命中的解码记录（始终包含）。
	Trigger AlertRecord `json:"trigger"`
	// Context 是各方向「触发前最近 N 条」解码记录（方向 → 旧→新有序）。
	Context map[string][]AlertRecord `json:"context,omitempty"`
}

// TruncateData 把一段 JSON 字节按上限裁剪：未超限原样作为 json.RawMessage 返回
// （序列化时内嵌为 JSON），超限则返回带省略标记的字符串。
func TruncateData(raw []byte, max int) any {
	if max <= 0 {
		max = MaxRecordDataBytes
	}
	if len(raw) <= max {
		if len(raw) == 0 {
			return nil
		}
		// 复制一份，避免调用方复用底层数组。
		cp := make(json.RawMessage, len(raw))
		copy(cp, raw)
		return cp
	}
	return string(raw[:max]) + fmt.Sprintf("…(truncated, %d bytes total)", len(raw))
}

// Match 报告一条谓词是否命中给定的事件 JSON 视图（payload+_meta 合并后的字节）。
// 把 gjson 依赖收敛在本包内：流水线只负责构造事件 JSON，求值细节不外泄。
// 空谓词恒 false（与 sdk/rule 的缺失语义一致）。
func Match(pred rule.Predicate, eventJSON []byte) bool {
	if len(eventJSON) == 0 {
		return false
	}
	return pred.Evaluate(gjson.ParseBytes(eventJSON))
}

// joinPath 拼接定位路径，空段跳过。
func joinPath(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "." + b
}
