// Package rule 实现 Protocol Semantic Rule（协议语义规则）。
//
// 定位：让插件告诉 GameTrace "对于我解析出来的 Event JSON，应该怎样判断、
// 提取和解释它的语义"。插件定义规则，平台执行规则：
//
//	Event JSON → GJSON 取值 → Predicate 判断 → Effect 产生语义
//
// 第一版 Effect 闭集只有四个：
//   - pair     配对（Request ↔ Response 等键相等关系）
//   - extract  提取（一个网络 Event 内的多个逻辑子事件）
//   - annotate 标注（request/response/notification/error 语义属性）
//   - name     命名（从 payload 经 GJSON 提取消息名称，写 meta.msg_name）
//
// 设计原则：Predicate 只做判断不做业务逻辑；不引入 Expr、脚本或自定义函数；
// Group / 高级因果关系（caused-by 等）在真实协议事实出现之前不做。
package rule

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Semantic 是 annotate 效果的语义标签闭集。
// Error 与 Notification 是事件的语义属性，不是 Relation（§8）。
type Semantic string

const (
	SemRequest      Semantic = "request"
	SemResponse     Semantic = "response"
	SemNotification Semantic = "notification"
	SemError        Semantic = "error"
)

// semClosed 是合法 Semantic 闭集。
var semClosed = map[Semantic]bool{
	SemRequest: true, SemResponse: true, SemNotification: true, SemError: true,
}

// EffectType 是 Effect 种类闭集（§5 / §6）。
type EffectType string

const (
	EffectPair     EffectType = "pair"
	EffectExtract  EffectType = "extract"
	EffectAnnotate EffectType = "annotate"
	EffectName     EffectType = "name"
)

// ChildSpec 描述 extract 效果产出的子事件声明。
type ChildSpec struct {
	EventType string `yaml:"event_type"`
	SchemaID  string `yaml:"schema_id"`
}

// PairSide 描述 pair 效果的一侧。
// Key 是该侧事件中配对键的 GJSON path —— 两侧可指向不同字段
// （协议两侧把关联值放在结构或字段名不同的位置时，各自声明自己的 path）。
// 其余字段（path/op/value/all/any，即 Predicate）用于判断事件是否属于这一侧。
type PairSide struct {
	Predicate
	Key string `yaml:"key"`
}

// MarshalYAML 输出扁平形式，与声明（手写 manifest）一致：
//
//	sides:
//	  - { path: direction, op: eq, value: client_to_server, key: seqId }
//
// yaml.v3 对嵌入结构体默认不 inline，会把谓词字段包一层 predicate: 键，
// 产生嵌套形式（register 序列化旧输出），导致读回时 PairSide.UnmarshalYAML
// 只认扁平键而静默丢弃角色判定。这里显式输出扁平字段列表（不依赖 inline
// 嵌入的运行时行为），保证序列化/反序列化往返无损。
func (s PairSide) MarshalYAML() (any, error) {
	type flatPairSide struct {
		Key   string      `yaml:"key"`
		All   []Predicate `yaml:"all,omitempty"`
		Any   []Predicate `yaml:"any,omitempty"`
		Path  string      `yaml:"path,omitempty"`
		Op    Op          `yaml:"op,omitempty"`
		Value any         `yaml:"value,omitempty"`
	}
	return flatPairSide{
		Key: s.Key, All: s.All, Any: s.Any, Path: s.Path, Op: s.Op, Value: s.Value,
	}, nil
}

// UnmarshalYAML 手工拆分 sides[i] 节点：key 字段归 PairSide.Key，
// 其余字段（path/op/value/all/any）按 Predicate 解码；组合器 all/any
// 支持与 when 一致（side 也可以是组合条件）。
// 兼容两种形式：
//   - 扁平：   { path: ..., op: ..., key: ... }
//   - 嵌套：   { predicate: { path: ..., op: ... }, key: ... }（旧序列化输出）
func (s *PairSide) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("pair side must be a mapping, got kind %d", value.Kind)
	}
	var rest []*yaml.Node
	for i := 0; i+1 < len(value.Content); i += 2 {
		k, v := value.Content[i], value.Content[i+1]
		switch k.Value {
		case "key":
			if err := v.Decode(&s.Key); err != nil {
				return err
			}
		case "predicate":
			// 嵌套形式：整个子节点就是谓词。
			if err := v.Decode(&s.Predicate); err != nil {
				return err
			}
		default:
			rest = append(rest, k, v)
		}
	}
	if len(rest) == 0 {
		return nil // 仅声明 key 或仅有嵌套 predicate：嵌套已解码，扁平无剩余
	}
	sub := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: rest}
	return s.Predicate.UnmarshalYAML(sub)
}

// Effect 是规则成立之后"意味着什么"。
//
// 字段按 Type 取用：
//   - pair:     Sides（恰好 2 个；每侧自带 key 与角色判定）
//   - extract:  Source（必填）+ Child（必填）
//   - annotate: Semantic（必填，闭集）
//   - name:     Key（必填，消息名称的 GJSON path）
type Effect struct {
	Type EffectType `yaml:"type"`

	// name：Key 是 GJSON path，其取值（字符串化）为该事件的消息名称，
	// 宿主写入 meta.msg_name。pair 不再使用规则级 Key —— 配对键下沉到
	// 每个 side 自己的 key（两侧字段结构可以不同）。
	Key   string     `yaml:"key,omitempty"`
	Sides []PairSide `yaml:"sides,omitempty"`

	// extract：Source 是 GJSON path，指向数组（每个元素一个子事件）
	// 或对象（单个子事件）。子事件保留父事件来源关系，由宿主挂接。
	Source string     `yaml:"source,omitempty"`
	Child  *ChildSpec `yaml:"child,omitempty"`

	// annotate：命中时给事件打上的语义标签。
	Semantic Semantic `yaml:"semantic,omitempty"`
}

// Rule 是一条语义规则：Predicate + Effect（§5）。
type Rule struct {
	ID     string     `yaml:"id"`
	When   *Predicate `yaml:"when"`
	Effect Effect     `yaml:"effect"`
}

// ruleIDPattern 约束规则 ID：点分小写段，段内仅小写字母/数字/下划线，
// 与 schema id 的段约束（gt.schema.id-format）同族。不带版本后缀。
var ruleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// Validate 校验单条规则声明的合法性。
// 返回空切片表示合法；问题以 Issue 形式返回（RuleID 为 gt.semantic.* 命名空间），
// 由上层 checker 转换为分层 Violation。
func (r Rule) Validate() []Issue {
	var issues []Issue

	if r.ID == "" {
		issues = append(issues, Issue{RuleID: RuleIDRequired, Message: "rule id is required", Severity: SeverityError})
	} else if !ruleIDPattern.MatchString(r.ID) {
		issues = append(issues, Issue{RuleID: RuleIDFormat,
			Message:  fmt.Sprintf("rule id %q must be dot-separated lowercase segments matching [a-z][a-z0-9_]*", r.ID),
			Severity: SeverityError})
	}

	if r.When == nil {
		issues = append(issues, Issue{RuleID: WhenRequired,
			Message: "when predicate is required (Semantic Rule = Predicate + Effect)", Severity: SeverityError})
	} else {
		for _, iss := range r.When.validate() {
			iss.Path = "when." + iss.Path
			issues = append(issues, iss)
		}
	}

	switch r.Effect.Type {
	case EffectPair:
		issues = append(issues, r.validatePair()...)
	case EffectExtract:
		issues = append(issues, r.validateExtract()...)
	case EffectAnnotate:
		issues = append(issues, r.validateAnnotate()...)
	case EffectName:
		issues = append(issues, r.validateName()...)
	case "":
		issues = append(issues, Issue{RuleID: EffectRequired, Message: "effect.type is required", Severity: SeverityError})
	default:
		issues = append(issues, Issue{RuleID: EffectUnknown,
			Message: fmt.Sprintf("effect.type %q not in pair|extract|annotate|name", r.Effect.Type), Severity: SeverityError})
	}
	return issues
}

func (r Rule) validateName() []Issue {
	var issues []Issue
	if r.Effect.Key == "" {
		issues = append(issues, Issue{RuleID: NameKeyRequired, Path: "effect",
			Message: "name effect requires key (GJSON path of the message name)", Severity: SeverityError})
	}
	return issues
}

func (r Rule) validatePair() []Issue {
	var issues []Issue
	// pair 必须恰好 2 个 sides：每侧自带 key（配对键来源）与角色判定，
	// 两个事件键相等且各匹配一个不同 side 时配对。不再支持"仅按键配对"的
	// 0-side 退化形态（没有每侧 key 就没有可比对的键）。
	if len(r.Effect.Sides) != 2 {
		issues = append(issues, Issue{RuleID: PairSidesLimit, Path: "effect.sides",
			Message: fmt.Sprintf("pair effect requires exactly 2 sides (one per role), got %d", len(r.Effect.Sides)), Severity: SeverityError})
	}
	for i, s := range r.Effect.Sides {
		if s.Key == "" {
			issues = append(issues, Issue{RuleID: PairKeyRequired, Path: fmt.Sprintf("effect.sides[%d]", i),
				Message: fmt.Sprintf("pair side %d requires key (GJSON path of the pairing value on this side)", i), Severity: SeverityError})
		}
		for _, iss := range s.Predicate.validate() {
			iss.Path = fmt.Sprintf("effect.sides[%d].%s", i, iss.Path)
			issues = append(issues, iss)
		}
	}
	return issues
}

func (r Rule) validateExtract() []Issue {
	var issues []Issue
	if r.Effect.Source == "" {
		issues = append(issues, Issue{RuleID: ExtractSourceRequired, Path: "effect",
			Message: "extract effect requires source (GJSON path to array/object)", Severity: SeverityError})
	}
	if r.Effect.Child == nil {
		issues = append(issues, Issue{RuleID: ExtractChildRequired, Path: "effect",
			Message: "extract effect requires child (event_type + schema_id)", Severity: SeverityError})
	} else {
		if r.Effect.Child.EventType == "" {
			issues = append(issues, Issue{RuleID: ExtractChildRequired, Path: "effect.child",
				Message: "extract child.event_type is required", Severity: SeverityError})
		}
		if r.Effect.Child.SchemaID == "" {
			issues = append(issues, Issue{RuleID: ExtractChildRequired, Path: "effect.child",
				Message: "extract child.schema_id is required", Severity: SeverityError})
		}
	}
	return issues
}

func (r Rule) validateAnnotate() []Issue {
	var issues []Issue
	if r.Effect.Semantic == "" {
		issues = append(issues, Issue{RuleID: AnnotateSemanticRequired, Path: "effect",
			Message: "annotate effect requires semantic", Severity: SeverityError})
	} else if !semClosed[r.Effect.Semantic] {
		issues = append(issues, Issue{RuleID: AnnotateSemanticUnknown, Path: "effect.semantic",
			Message:  fmt.Sprintf("semantic %q not in request|response|notification|error", r.Effect.Semantic),
			Severity: SeverityError})
	}
	return issues
}

// String 返回规则的可读描述，用于 Violation Message 与日志。
func (r Rule) String() string {
	return fmt.Sprintf("semantic rule %s (%s)", r.ID, r.Effect.Type)
}

// RulesReport 是一组规则声明校验的总入口：逐条 Validate + 全局唯一性。
// 供 contract.PluginChecker 与宿主在注册期调用。
func RulesReport(rules []Rule) []Issue {
	var issues []Issue
	seen := make(map[string]bool, len(rules))
	for i, r := range rules {
		prefix := fmt.Sprintf("semantic_rules[%d]", i)
		if r.ID != "" {
			if seen[r.ID] {
				issues = append(issues, Issue{RuleID: RuleIDDuplicate, Path: prefix,
					Message: fmt.Sprintf("semantic rule id %q declared more than once", r.ID), Severity: SeverityError})
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
