package contract

import (
	"strings"
)

// Severity 是规则严重级别（契约合规词汇）。
// info 仅 domain 规则可用；平台规则只用 error | warn 两档。
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
	SeverityInfo  Severity = "info"
)

// Layer 标识一条违规或规则所属的契约层。
//
// SDK 现在仅承载 Protocol Semantic Rule 一层（semantic）；
// stream 仅作为分层报告的展示维度保留。
type Layer string

const (
	LayerSemantic Layer = "semantic"
	LayerStream   Layer = "stream"
)

// Rule 是一条机器可读的插件开发规则（契约合规词汇）。
//
// 它是 contract.yaml rules 段的 Go 表示，也是 plugin.brief / plugin.verify /
// plugin.explain 三个工具的共享词汇：brief 按 topic/severity 筛选返回，verify 用它
// 标注违规项，explain 用它引用归因依据。规则 ID 是唯一真源（contract.yaml），Go 端
// 只持有别名常量防拼错。
type Rule struct {
	ID        string   `yaml:"id" json:"id"`
	Topic     string   `yaml:"topic" json:"topic"`
	Layer     Layer    `yaml:"layer,omitempty" json:"layer,omitempty"`
	Severity  Severity `yaml:"severity" json:"severity"`
	Statement string   `yaml:"statement" json:"statement"`
	Detail    string   `yaml:"detail,omitempty" json:"detail,omitempty"`
	DocRef    string   `yaml:"doc_ref" json:"doc_ref"`

	Aliases    []string `yaml:"aliases,omitempty" json:"aliases,omitempty"`       // 旧裸 ID
	AppliesTo  []string `yaml:"applies_to,omitempty" json:"applies_to,omitempty"` // event/subject ID
	Since      string   `yaml:"since,omitempty" json:"since,omitempty"`
	Deprecated bool     `yaml:"deprecated,omitempty" json:"deprecated,omitempty"`
}

// Namespace 返回规则命名空间（ID 的首段），如 "gt" 或领域根 "game"。
// 由 ID 派生，不手填，避免 namespace 与 ID 不一致。
func (r Rule) Namespace() string {
	if i := strings.Index(r.ID, "."); i >= 0 {
		return r.ID[:i]
	}
	return r.ID
}

// IsPlatform 报告是否为平台规则（gt. 前缀）。
func (r Rule) IsPlatform() bool {
	return r.Namespace() == "gt"
}
