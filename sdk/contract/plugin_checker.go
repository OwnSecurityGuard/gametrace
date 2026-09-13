package contract

import (
	"fmt"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
)

// PluginChecker 是 Protocol Semantic Rule 的校验器。
//
// 它是 SDK 现在唯一保留的契约检查层：插件声明语义规则（rule 包），
// 平台在注册期与运行期执行这些规则。调用方接口（Check / CheckEvent）保持不变。
// schema / states 声明层已随插件侧剔除移除，这里只做 semantic_rules 校验。
//
// 各层产出 *Report 后由上层 Merge，单份 Report 内可按 Layer 聚合（ByLayer / Stats）。
type PluginChecker struct {
	c *Contract
}

// NewPluginChecker 构造一个以 embed 契约为基准的校验器。
func NewPluginChecker() *PluginChecker {
	return &PluginChecker{c: Default()}
}

// Check 对解析后的 manifest 跑声明期检查，返回完整 Report：
//   - semantic 层：semantic_rules 声明自洽
//
// 这是 plugin.verify 在"声明期"的入口。
func (pc *PluginChecker) Check(m *sdk.Manifest) *Report {
	r := &Report{}
	if len(m.SemanticRules) > 0 {
		r.Merge(pc.checkRules(m))
	}
	return r
}

// CheckEvent 对单条解码事件跑语义规则运行期检查：
// 规则评估失败即契约违规（payload 无法转 JSON 等情况）。
// annotate / pair / extract 的命中是平台执行事实，不在 verify 层判定对错。
func (pc *PluginChecker) CheckEvent(m *sdk.Manifest, d *event.Draft) *Report {
	r := &Report{}
	if len(m.SemanticRules) > 0 {
		if _, err := rule.Evaluate(m.SemanticRules, d.Value); err != nil {
			r.Add(mkViolation(LayerSemantic, rule.EvaluateFailed,
				fmt.Sprintf("semantic rule evaluation failed: %v", err), SevError))
		}
	}
	return r
}

// checkRules 校验 manifest semantic_rules 声明（semantic 层，声明期）。
// 覆盖：规则 ID 格式/唯一性、when 谓词形状、effect 闭集与各自必填字段。
func (pc *PluginChecker) checkRules(m *sdk.Manifest) *Report {
	r := &Report{}
	for _, iss := range rule.RulesReport(m.SemanticRules) {
		r.Add(fromRuleIssue(iss))
	}
	return r
}

// fromRuleIssue 把 rule 包的内部 Issue 转为分层 Violation。
// semantic 层规则 ID（gt.semantic.*）由 rule 包持有，contract.yaml 已收录对应原文，
// 供 verify/explain 回查。
func fromRuleIssue(iss rule.Issue) Violation {
	v := Violation{
		RuleID:   iss.RuleID,
		Message:  iss.Message,
		Layer:    LayerSemantic,
		Severity: Severity(iss.Severity),
		Path:     iss.Path,
	}
	if spec, ok := Default().RuleByID(iss.RuleID); ok {
		v.DocRef = spec.DocRef
	}
	return v
}

// mkViolation 构造一条违规，并尽力回查 DocRef（contract.yaml 未收录的规则 DocRef 留空）。
func mkViolation(layer Layer, ruleID, msg string, sev Severity) Violation {
	v := Violation{RuleID: ruleID, Message: msg, Layer: layer, Severity: sev}
	if r, ok := Default().RuleByID(ruleID); ok {
		v.DocRef = r.DocRef
	}
	return v
}
