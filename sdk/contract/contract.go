// Package contract 承载 GameTrace Decoder Plugin API 契约（contract.yaml）。
//
// contract.yaml 是插件与宿主之间线上协议的 SSOT，描述 RPC 契约、link_types、
// 产出编码、payload 保留字段、manifest schema 与机器可读的规则集。
//
// 该文件自 gametrace/pkg/plugin/contract/ 迁入本 SDK。契约现单向流动：SDK 定义，
// 宿主（gametrace）通过本包消费，杜绝两仓库各持一份导致的漂移。
package contract

import (
	_ "embed"
	"fmt"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed contract.yaml
var contractYAML []byte

// RawYAML 返回 embed 的 contract.yaml 原始字节。
// 供需要原文的调用方（如 MCP 的 plugin.contract 工具）直接读取，无需重新解析。
func RawYAML() []byte {
	return contractYAML
}

// Contract 是 contract.yaml 的 Go 表示。
type Contract struct {
	SpecVersion           int                  `yaml:"spec_version"`
	APIVersion            string               `yaml:"api_version"`
	Contract              ContractBlock        `yaml:"contract"` // 语义契约版本块（新增，§3）
	RPC                   RPCSpec              `yaml:"rpc"`
	LinkTypes             LinkTypesSpec        `yaml:"link_types"`
	RegistryDiscovery     []map[string]string  `yaml:"registry_discovery"` // 每项单键 map: {env|flag|default: 值}
	Heartbeat             HeartbeatSpec        `yaml:"heartbeat"`
	OutputContract        OutputContractSpec   `yaml:"output_contract"`
	ReservedPayloadFields map[string]FieldSpec `yaml:"reserved_payload_fields"`
	ManifestSchema        ManifestSchemaSpec   `yaml:"manifest_schema"`
	Rules                 []Rule               `yaml:"rules"`
}

// ContractBlock 是语义契约版本块（§3）。
type ContractBlock struct {
	Name       string         `yaml:"name"` // gt.plugin
	Version    int            `yaml:"version"`
	Components map[string]int `yaml:"components"` // semantic 组件的版本（SDK 现仅承载 Protocol Semantic Rule 一层）
}

// RPCSpec 描述 PluginRegistry 与 Decoder 两个 gRPC 服务的契约。
type RPCSpec struct {
	RegistryService string                `yaml:"registry_service"`
	DecoderService  string                `yaml:"decoder_service"`
	Methods         map[string]MethodSpec `yaml:"methods"`
}

// MethodSpec 描述单个 RPC 方法的请求/响应结构。
type MethodSpec struct {
	Type     string            `yaml:"type,omitempty"` // "bidi_stream" 等
	Request  map[string]string `yaml:"request"`
	Response map[string]string `yaml:"response"`
}

// LinkTypesSpec 描述标准与自定义链路层类型枚举。
type LinkTypesSpec struct {
	Standard map[int]string `yaml:"standard"`
	Custom   map[int]string `yaml:"custom"`
}

// HeartbeatSpec 描述心跳协议默认参数。
type HeartbeatSpec struct {
	DefaultIntervalSec int `yaml:"default_interval_sec"`
	OfflineTimeoutSec  int `yaml:"offline_timeout_sec"`
}

// OutputContractSpec 描述 v2 的事件产出编码约定。
// v1 曾用 JSON 字符串产出并区分 data / _fields 子对象，v2 起统一为 tagged MsgPack。
type OutputContractSpec struct {
	Encoding  string   `yaml:"encoding"`
	RootKind  string   `yaml:"root_kind"`
	Builder   string   `yaml:"builder"`
	Marshal   string   `yaml:"marshal"`
	Unmarshal string   `yaml:"unmarshal"`
	Notes     []string `yaml:"notes"`
}

// FieldSpec 描述字段的类型约束，用于 reserved_payload_fields 与 manifest_schema。
type FieldSpec struct {
	Type        string               `yaml:"type"`
	Description string               `yaml:"description,omitempty"`
	Pattern     string               `yaml:"pattern,omitempty"`  // 正则约束，如 name 的 kebab-case
	Optional    bool                 `yaml:"optional,omitempty"` // manifest_schema 中标记非必填
	Enum        []string             `yaml:"enum,omitempty"`
	Fallback    string               `yaml:"fallback,omitempty"`
	ConsumedBy  string               `yaml:"consumed_by,omitempty"`
	Validation  string               `yaml:"validation,omitempty"`
	Items       *ItemsSpec           `yaml:"items,omitempty"`
	Required    []string             `yaml:"required,omitempty"`
	Fields      map[string]FieldSpec `yaml:"fields,omitempty"`
}

// ItemsSpec 表示 type=array 字段的元素约束。
// YAML 中 items 有两种形态：
//   - 标量字符串（如 hints: { items: string }），仅声明元素类型名
//   - 映射对象（如 _state_changes.items 含 required/fields），描述完整字段约束
//
// 用 UnmarshalYAML 区分两种形态，避免用 any 绕过类型安全。
type ItemsSpec struct {
	TypeName string     // 当 items 是标量字符串时的元素类型名
	Spec     *FieldSpec // 当 items 是映射对象时的完整字段约束
}

// UnmarshalYAML 实现 yaml.Unmarshaler，按节点类型分派到 TypeName 或 Spec。
func (i *ItemsSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		i.TypeName = value.Value
		return nil
	}
	var fs FieldSpec
	if err := value.Decode(&fs); err != nil {
		return err
	}
	i.Spec = &fs
	return nil
}

// ManifestSchemaSpec 描述 plugin.yaml manifest 的校验规则。
type ManifestSchemaSpec struct {
	Required []string             `yaml:"required"`
	Fields   map[string]FieldSpec `yaml:"fields"`
}

// Rule 是一条机器可读的插件开发规则。定义见 types.go（从原 rule 包迁入，
// 使契约类型单一真源位于 contract 包，避免独立 rule 包与 contract 形成环依赖）。

// Load 解析 embed 的 contract.yaml 返回 Contract。
// 因为 contract.yaml 在编译时 embed，此函数不会因文件缺失失败。
func Load() (*Contract, error) {
	var c Contract
	if err := yaml.Unmarshal(contractYAML, &c); err != nil {
		return nil, fmt.Errorf("parse contract.yaml: %w", err)
	}
	return &c, nil
}

// MustLoad 与 Load 相同但 panic on error。用于程序启动时。
func MustLoad() *Contract {
	c, err := Load()
	if err != nil {
		panic(fmt.Sprintf("load contract.yaml: %v", err))
	}
	return c
}

// Default 返回进程内共享的 Contract 单例。
//
// contract.yaml 在编译期 embed 且运行期不变，解析结果可安全复用。
// 规则查询是热路径（每次 violation 都要回查 rule），不应每次重解析 YAML。
var Default = sync.OnceValue(MustLoad)

// RuleFilter 描述规则筛选条件。零值表示不过滤。
type RuleFilter struct {
	Topic    string   // 非空则只返回该 topic
	Severity Severity // 非空则只返回该 severity
}

// Rules 按条件返回规则集，结果按 (topic, id) 稳定排序。
//
// 典型用法：
//   - plugin.brief 默认档：RuleFilter{Severity: SeverityError}
//   - plugin.brief 按主题：RuleFilter{Topic: "framing"}
func (c *Contract) FilterRules(f RuleFilter) []Rule {
	out := make([]Rule, 0, len(c.Rules))
	for _, r := range c.Rules {
		if f.Topic != "" && r.Topic != f.Topic {
			continue
		}
		if f.Severity != "" && r.Severity != f.Severity {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Topic != out[j].Topic {
			return out[i].Topic < out[j].Topic
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RuleByID 按 ID 查找单条规则，供 verify/explain 回查引用。
func (c *Contract) RuleByID(id string) (Rule, bool) {
	for _, r := range c.Rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// Topics 返回全部规则主题，按字母序去重。供 plugin.brief 列出可选 topic。
func (c *Contract) Topics() []string {
	seen := make(map[string]struct{}, len(c.Rules))
	out := make([]string, 0, len(c.Rules))
	for _, r := range c.Rules {
		if _, ok := seen[r.Topic]; ok {
			continue
		}
		seen[r.Topic] = struct{}{}
		out = append(out, r.Topic)
	}
	sort.Strings(out)
	return out
}
