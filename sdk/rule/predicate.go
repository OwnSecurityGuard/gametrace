package rule

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

// Op 是判断操作闭集（§4）。Predicate 只做判断，不做业务逻辑；
// 第一版不引入 Expr、脚本、自定义函数，避免 SDK 演变成新的 DSL。
type Op string

const (
	OpEq        Op = "eq"
	OpNeq       Op = "neq"
	OpExists    Op = "exists"
	OpNotExists Op = "not_exists"
	OpGt        Op = "gt"
	OpGte       Op = "gte"
	OpLt        Op = "lt"
	OpLte       Op = "lte"
	OpIn        Op = "in"
	OpNotIn     Op = "not_in"
	OpContains  Op = "contains"
	OpPrefix    Op = "prefix"
	OpSuffix    Op = "suffix"
)

// opClosed 是合法 Op 闭集。
var opClosed = map[Op]bool{
	OpEq: true, OpNeq: true, OpExists: true, OpNotExists: true,
	OpGt: true, OpGte: true, OpLt: true, OpLte: true,
	OpIn: true, OpNotIn: true, OpContains: true, OpPrefix: true, OpSuffix: true,
}

// needsValue 列出必须携带 value 字面量的操作。
var needsValue = map[Op]bool{
	OpEq: true, OpNeq: true, OpGt: true, OpGte: true, OpLt: true, OpLte: true,
	OpIn: true, OpNotIn: true, OpContains: true, OpPrefix: true, OpSuffix: true,
}

// needsArrayValue 列出 value 必须是数组的操作。
var needsArrayValue = map[Op]bool{OpIn: true, OpNotIn: true}

// Predicate 是最小判断单元：GJSON path 取值后做一次判断（§4）。
//
// 两种形态互斥：
//
//	单条件:  {path: seqId, op: neq, value: 0}
//	组合器:  {all: [ ... ]} / {any: [ ... ]}
//
// 语义约定：除 exists / not_exists 外，所有操作在 path 取值不存在时判 false
// （缺失检查交给 exists / not_exists 显式表达）。
type Predicate struct {
	// 组合器（与单条件互斥）
	All []Predicate `yaml:"all,omitempty"`
	Any []Predicate `yaml:"any,omitempty"`

	// 单条件
	Path  string `yaml:"path,omitempty"` // GJSON path，如 data.targetId
	Op    Op     `yaml:"op,omitempty"`
	Value any    `yaml:"value,omitempty"` // 字面量：标量或数组
}

// UnmarshalYAML 支持两种 when 写法：
//
//	when: [ {path: seqId, op: neq, value: 0}, ... ]   # 序列 → 隐式 all
//	when: { all: [...] } / { any: [...] } / 叶子映射
//
// 序列简写是插件作者最常用的形态（多个条件 AND 起来），不做隐式转换的话
// 每个 manifest 都要多包一层 all。
func (p *Predicate) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.SequenceNode {
		var list []Predicate
		if err := value.Decode(&list); err != nil {
			return err
		}
		p.All = list
		return nil
	}
	type rawPredicate Predicate // 阻断递归，走默认字段解码
	var raw rawPredicate
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*p = Predicate(raw)
	return nil
}

// validate 校验该谓词节点的声明合法性，Path 为相对定位（不含规则与 when 前缀）。
func (p *Predicate) validate() []Issue {
	var issues []Issue

	hasCombinator := len(p.All) > 0 || len(p.Any) > 0
	hasLeaf := p.Path != "" || p.Op != "" || p.Value != nil

	switch {
	case len(p.All) > 0 && len(p.Any) > 0:
		issues = append(issues, Issue{RuleID: CombinatorMixed,
			Message: "all and any must not be combined in one predicate node", Severity: SeverityError})
		return issues // 混用时无法确定评估顺序
	case hasCombinator && hasLeaf:
		issues = append(issues, Issue{RuleID: CombinatorMixed,
			Message: "combinator (all/any) must not be combined with leaf condition (path/op/value)", Severity: SeverityError})
		return issues
	case hasCombinator:
		kind, list := "all", p.All
		if len(p.Any) > 0 {
			kind, list = "any", p.Any
		}
		for i := range list {
			for _, iss := range list[i].validate() {
				iss.Path = fmt.Sprintf("%s[%d].%s", kind, i, iss.Path)
				issues = append(issues, iss)
			}
		}
		return issues
	}

	// 叶子节点
	if p.Op == "" {
		issues = append(issues, Issue{RuleID: OpRequired, Path: "op",
			Message: "op is required", Severity: SeverityError})
		return issues
	}
	if !opClosed[p.Op] {
		issues = append(issues, Issue{RuleID: OpUnknown, Path: "op",
			Message:  fmt.Sprintf("op %q not in eq|neq|exists|not_exists|gt|gte|lt|lte|in|not_in|contains|prefix|suffix", p.Op),
			Severity: SeverityError})
		return issues
	}
	if p.Path == "" {
		issues = append(issues, Issue{RuleID: PathRequired, Path: "path",
			Message: fmt.Sprintf("path is required for op %q", p.Op), Severity: SeverityError})
	}
	if needsValue[p.Op] && p.Value == nil {
		issues = append(issues, Issue{RuleID: ValueRequired, Path: "value",
			Message: fmt.Sprintf("value is required for op %q", p.Op), Severity: SeverityError})
	}
	if p.Value != nil && needsArrayValue[p.Op] && !p.literal().IsArray() {
		issues = append(issues, Issue{RuleID: ValueArrayRequired, Path: "value",
			Message: fmt.Sprintf("value must be an array for op %q", p.Op), Severity: SeverityError})
	}
	return issues
}

// Evaluate 对文档执行判断。doc 应为事件 JSON 的 gjson.Result。
// 声明非法的节点（调用方未先 validate）按 false 处理，不 panic。
func (p *Predicate) Evaluate(doc gjson.Result) bool {
	if len(p.All) > 0 && len(p.Any) == 0 {
		for i := range p.All {
			if !p.All[i].Evaluate(doc) {
				return false
			}
		}
		return true
	}
	if len(p.Any) > 0 {
		for i := range p.Any {
			if p.Any[i].Evaluate(doc) {
				return true
			}
		}
		return false
	}
	return p.evalLeaf(doc)
}

func (p *Predicate) evalLeaf(doc gjson.Result) bool {
	res := doc.Get(p.Path)
	lit := p.literal()

	switch p.Op {
	case OpExists:
		return res.Exists()
	case OpNotExists:
		return !res.Exists()
	}

	// 其余操作都要求取值存在；缺失即 false（§4 语义约定）。
	if !res.Exists() {
		return false
	}

	switch p.Op {
	case OpEq:
		return eqMatch(res, lit)
	case OpNeq:
		return !eqMatch(res, lit)
	case OpGt, OpGte, OpLt, OpLte:
		return orderMatch(p.Op, res, lit)
	case OpIn:
		return arrayContains(lit, res)
	case OpNotIn:
		return !arrayContains(lit, res)
	case OpContains:
		switch {
		case res.Type == gjson.String && lit.Type == gjson.String:
			return strings.Contains(res.Str, lit.Str)
		case res.IsArray():
			return arrayContains(res, lit)
		}
		return false
	case OpPrefix:
		return res.Type == gjson.String && lit.Type == gjson.String && strings.HasPrefix(res.Str, lit.Str)
	case OpSuffix:
		return res.Type == gjson.String && lit.Type == gjson.String && strings.HasSuffix(res.Str, lit.Str)
	}
	return false
}

// literal 把声明期字面量转为 gjson.Result（经 JSON 归一化）。
// YAML 解出的标量/数组/对象都可安全 json.Marshal。
func (p *Predicate) literal() gjson.Result {
	b, err := json.Marshal(p.Value)
	if err != nil {
		return gjson.Result{} // Type Null；声明期 validate 已拦截非法值
	}
	return gjson.ParseBytes(b)
}

// eqMatch 判断取值与字面量是否相等。kind 感知：数字/字符串/布尔/null 各自比较，
// kind 不一致判 false（不隐式转型，避免 "0" == 0 之类的歧义）。
func eqMatch(res, lit gjson.Result) bool {
	if lit.Type == gjson.Null {
		return res.Type == gjson.Null && res.Exists()
	}
	switch {
	case res.Type == gjson.Number && lit.Type == gjson.Number:
		return res.Num == lit.Num
	case res.Type == gjson.String && lit.Type == gjson.String:
		return res.Str == lit.Str
	case (res.Type == gjson.True || res.Type == gjson.False) && (lit.Type == gjson.True || lit.Type == gjson.False):
		return res.Bool() == lit.Bool()
	}
	return false
}

// arrayContains 判断 arr 数组中是否存在与 item eqMatch 的元素。
func arrayContains(arr, item gjson.Result) bool {
	if !arr.IsArray() {
		return false
	}
	for _, e := range arr.Array() {
		if eqMatch(e, item) {
			return true
		}
	}
	return false
}

// orderMatch 执行 gt/gte/lt/lte。数字按数值序，字符串按字典序，其他 kind 判 false。
func orderMatch(op Op, res, lit gjson.Result) bool {
	var cmp int
	switch {
	case res.Type == gjson.Number && lit.Type == gjson.Number:
		switch {
		case res.Num < lit.Num:
			cmp = -1
		case res.Num > lit.Num:
			cmp = 1
		}
	case res.Type == gjson.String && lit.Type == gjson.String:
		cmp = strings.Compare(res.Str, lit.Str)
	default:
		return false
	}
	switch op {
	case OpGt:
		return cmp > 0
	case OpGte:
		return cmp >= 0
	case OpLt:
		return cmp < 0
	case OpLte:
		return cmp <= 0
	}
	return false
}
