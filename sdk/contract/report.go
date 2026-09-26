package contract

import (
	"encoding/json"
	"fmt"
)

// Layer / Severity 定义见 types.go（从原 rule 包迁入）。
// 这里保留 SevError / SevWarn 作为调用点常用的短别名。
const (
	SevError = SeverityError
	SevWarn  = SeverityWarn
)

// Violation 表示一次契约违规。
//
// RuleID 取自 contract.yaml 的 rules 段（schema 层部分规则由 schema 包独立持有，
// 不在 contract.yaml）；拿到 Violation 的一方可用 Spec() 回查规则原文、严重级别与
// 文档位置，无需解析 Message 字符串。
type Violation struct {
	RuleID   string
	Message  string
	Layer    Layer
	Severity Severity
	Path     string // 定位路径，如 schemas[0].fields.hp / hp
	DocRef   string // 来自 contract.yaml 的 rule doc_ref
}

func (v Violation) Error() string {
	return fmt.Sprintf("[%s/%s] %s: %s", v.Layer, v.Severity, v.RuleID, v.Message)
}

// Spec 回查本次违规对应的规则定义。
// 返回 false 说明 RuleID 不在 contract.yaml 中——schema 层部分规则由 schema 包独立
// 持有，属正常情况，不代表漂移。
func (v Violation) Spec() (Rule, bool) {
	return Default().RuleByID(v.RuleID)
}

// Report 是分层校验的总报告。PluginChecker 各子检查器产出 *Report 后由上层 Merge。
type Report struct {
	Violations []Violation `json:"violations"`
}

// Add 追加一条违规。
func (r *Report) Add(v Violation) {
	r.Violations = append(r.Violations, v)
}

// Merge 把另一份报告（可空）的违规并入本报告。
func (r *Report) Merge(o *Report) {
	if o == nil {
		return
	}
	r.Violations = append(r.Violations, o.Violations...)
}

// HasErrors 报告是否含任意 error 级违规。
func (r *Report) HasErrors() bool {
	for _, v := range r.Violations {
		if v.Severity == SevError {
			return true
		}
	}
	return false
}

// ByLayer 按层聚合违规，便于分面板展示。
func (r *Report) ByLayer() map[Layer][]Violation {
	out := make(map[Layer][]Violation, len(r.Violations))
	for _, v := range r.Violations {
		out[v.Layer] = append(out[v.Layer], v)
	}
	return out
}

// LayerStat 是单层违规计数。
type LayerStat struct {
	Layer  Layer `json:"layer"`
	Errors int   `json:"errors"`
	Warns  int   `json:"warns"`
}

// Stats 计算每层 error/warn 计数，按稳定顺序返回。
func (r *Report) Stats() []LayerStat {
	counts := map[Layer]*LayerStat{}
	for _, v := range r.Violations {
		st, ok := counts[v.Layer]
		if !ok {
			st = &LayerStat{Layer: v.Layer}
			counts[v.Layer] = st
		}
		if v.Severity == SevError {
			st.Errors++
		} else {
			st.Warns++
		}
	}
	order := []Layer{LayerSemantic, LayerStream}
	out := make([]LayerStat, 0, len(counts))
	for _, l := range order {
		if st, ok := counts[l]; ok {
			out = append(out, *st)
		}
	}
	return out
}

// JSON 序列化为 plugin.verify 的机器可读输出。
func (r *Report) JSON() ([]byte, error) {
	payload := struct {
		HasErrors  bool        `json:"has_errors"`
		Stats      []LayerStat `json:"stats"`
		Violations []Violation `json:"violations"`
	}{
		HasErrors:  r.HasErrors(),
		Stats:      r.Stats(),
		Violations: r.Violations,
	}
	return json.MarshalIndent(payload, "", "  ")
}
