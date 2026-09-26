package rule

// Issue 是语义规则层校验发现的一条问题。
// 与 contract.Violation 解耦：rule 包不依赖 contract，由上层 checker 转换。
type Issue struct {
	RuleID   string // gt.semantic.* 规则 ID
	Path     string // 定位，如 semantic_rules[0].when.path
	Message  string
	Severity string // SeverityError | SeverityWarn
}

const (
	SeverityError = "error"
	SeverityWarn  = "warn"
)

// 语义规则层的规则 ID（gt.semantic.* 命名空间）。
// 真源是 contract/contract.yaml 的 rules 段，这里只放常量防拼错。
const (
	RuleIDRequired           = "gt.semantic.rule-id-required"
	RuleIDFormat             = "gt.semantic.rule-id-format"
	RuleIDDuplicate          = "gt.semantic.rule-id-duplicate"
	WhenRequired             = "gt.semantic.when-required"
	EffectRequired           = "gt.semantic.effect-required"
	EffectUnknown            = "gt.semantic.effect-unknown"
	OpUnknown                = "gt.semantic.op-unknown"
	OpRequired               = "gt.semantic.op-required"
	PathRequired             = "gt.semantic.path-required"
	ValueRequired            = "gt.semantic.value-required"
	ValueArrayRequired       = "gt.semantic.value-array-required"
	CombinatorEmpty          = "gt.semantic.combinator-empty"
	CombinatorMixed          = "gt.semantic.combinator-mixed"
	PairKeyRequired          = "gt.semantic.pair-key-required"
	PairSidesLimit           = "gt.semantic.pair-sides-limit"
	AnnotateSemanticRequired = "gt.semantic.annotate-semantic-required"
	AnnotateSemanticUnknown  = "gt.semantic.annotate-semantic-unknown"
	NameKeyRequired          = "gt.semantic.name-key-required"
	EvaluateFailed           = "gt.semantic.evaluate-failed"
)
