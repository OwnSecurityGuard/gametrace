package sdk

import (
	"fmt"
	"regexp"

	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
	"gopkg.in/yaml.v3"
)

// Manifest 是 plugin.yaml 的 Go 表示。
// 插件通过 Register RPC 传 plugin.yaml 原文，主程序 ParseManifest 后 ValidateManifest 校验。
// 字段语义参考 github.com/OwnSecurityGuard/gametrace/sdk 内置 contract 的 manifest_schema。
//
// 该类型定义与校验逻辑内置于 SDK，使插件模块无需依赖 gametrace 根模块即可完成
// manifest 解析与校验（SDK 的 ReadManifest 直接复用本文件函数）。
//
// 语义声明仅保留 Protocol Semantic Rule（semantic_rules，§10）：插件定义协议语义，平台执行。
// v1 遗留 event 块已废弃；schema/states 声明层已移除（插件只需 semantic_rules 的 pair 规则）。
// framing/time_authority/state_ordering 等其余各层声明保持移除，不再回归。
type Manifest struct {
	APIVersion      string    `yaml:"api_version"`          // gt.decoder/v2
	Name            string    `yaml:"name"`                 // kebab-case
	Protocol        string    `yaml:"protocol"`             // 主协议名（L7）
	Type            string    `yaml:"type"`                 // 固定 decoder
	ProtocolVersion string    `yaml:"protocol_version"`     // 可选，游戏协议版本
	Transports      []string  `yaml:"transports,omitempty"` // 可选，声明能解的 L4 传输层，取值 tcp|udp（缺省由 platform dispatcher 按既有语义处理）
	Hints           []string  `yaml:"hints"`                // 可选，匹配提示
	Event           EventSpec `yaml:"event"`                // 可选，遗留字段声明，见 EventSpec

	Contract      *ContractDecl `yaml:"contract,omitempty"`       // 语义契约版本块（§3）
	SemanticRules []rule.Rule   `yaml:"semantic_rules,omitempty"` // Protocol Semantic Rule（插件定义协议语义，平台执行）
	Capabilities  Capabilities  `yaml:"capabilities,omitempty"`   // 能力位（§6）
	Meta          ManifestMeta  `yaml:"meta"`                     // 可选，元数据
}

// Capability 是插件能力位闭集（§6.1）。
type Capability string

const (
	CapDecode Capability = "decode"
)

// Capabilities 是能力位集合。
type Capabilities map[Capability]bool

// ContractDecl 是 manifest 的语义契约版本声明（§3.2）。
type ContractDecl struct {
	Name    string `yaml:"name"`    // gt.plugin
	Version int    `yaml:"version"` // 1
}

// EventSpec 是 v1 遗留的事件结构声明。
//
// 【已废弃，仅为兼容旧 plugin.yaml 保留解析能力】
// v1 时期 payload 是 JSON，平台语义放 _fields 子对象、业务语义放 data 子对象，
// 本结构就是那套形状的声明。v2 起 payload 改为 MsgPack 编码的 event.Value，
// 业务字段直接挂根对象，data/_fields 两个约定均已不存在
// （见 contract.yaml 的 output_contract）。
//
// 新插件无需使用 EventSpec：业务语义请通过 Manifest.SemanticRules 声明
// （Protocol Semantic Rule，见 docs/plugin-semantic-rules.md）。
type EventSpec struct {
	Fields map[string]FieldDecl `yaml:"fields"` // 遗留：v1 _fields 内字段声明
	Data   DataSpec             `yaml:"data"`   // 遗留：v1 业务 data 子对象 schema
}

// FieldDecl 是 event.fields 内单个字段的声明。已随 EventSpec 一并废弃。
type FieldDecl struct {
	Type string   `yaml:"type"`
	Enum []string `yaml:"enum,omitempty"`
}

// DataSpec 描述 v1 业务 data 子对象的入口 schema。已随 EventSpec 一并废弃。
type DataSpec struct {
	Schema DataSchema `yaml:"schema"`
}

// DataSchema 描述 v1 data 子对象的字段结构。已随 EventSpec 一并废弃。
type DataSchema struct {
	Fields map[string]FieldDecl `yaml:"fields"`
}

// ManifestMeta 是 manifest 的元数据段。
type ManifestMeta struct {
	Author      string `yaml:"author"`
	Description string `yaml:"description"`
	Homepage    string `yaml:"homepage"`
}

// ParseManifest 解析 plugin.yaml 原文返回 Manifest。
// 不做字段校验，校验由 ValidateManifest 完成。
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

var (
	// apiVersionPattern 匹配 gt.decoder/v<数字> 格式。
	apiVersionPattern = regexp.MustCompile(`^gt\.decoder/v\d+$`)
	// namePattern 匹配 kebab-case：小写字母开头，仅含小写字母/数字/连字符。
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// ValidateManifest 校验 manifest 必填字段与格式约束。
// 返回 nil 表示合法，返回 error 描述具体违规字段。
func ValidateManifest(m *Manifest) error {
	if m.APIVersion == "" {
		return fmt.Errorf("manifest field api_version is required")
	}
	if !apiVersionPattern.MatchString(m.APIVersion) {
		return fmt.Errorf("manifest field api_version %q must match gt.decoder/v<digit>", m.APIVersion)
	}
	if m.Name == "" {
		return fmt.Errorf("manifest field name is required")
	}
	if !namePattern.MatchString(m.Name) {
		return fmt.Errorf("manifest field name %q must be kebab-case (lowercase letters, digits, hyphens; must start with letter)", m.Name)
	}
	if m.Protocol == "" {
		return fmt.Errorf("manifest field protocol is required")
	}
	if m.Type == "" {
		return fmt.Errorf("manifest field type is required")
	}
	if m.Type != "decoder" {
		return fmt.Errorf("manifest field type %q must be decoder", m.Type)
	}
	if err := validateTransports(m.Transports); err != nil {
		return err
	}
	return nil
}

// validTransports 是 plugin.yaml transports 字段允许的取值（L4 传输层）。
var validTransports = map[string]bool{"tcp": true, "udp": true}

// validateTransports 校验 transports 声明的取值集合，确保每个条目都是 tcp/udp 且不重复。
// 空列表（未声明）不视为违规：缺省行为由 platform dispatcher 按既有语义决定。
func validateTransports(transports []string) error {
	seen := make(map[string]bool, len(transports))
	for _, t := range transports {
		if !validTransports[t] {
			return fmt.Errorf("manifest field transports item %q must be one of [tcp udp]", t)
		}
		if seen[t] {
			return fmt.Errorf("manifest field transports has duplicate entry %q", t)
		}
		seen[t] = true
	}
	return nil
}
