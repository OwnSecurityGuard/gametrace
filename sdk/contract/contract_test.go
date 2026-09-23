package contract

import (
	"regexp"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	if c.SpecVersion != 6 {
		t.Errorf("spec_version = %d, want 6", c.SpecVersion)
	}
	// semantic 组件（Protocol Semantic Rule）自 spec_version 5 起加入。
	if c.Contract.Components["semantic"] != 1 {
		t.Errorf("contract.components.semantic = %d, want 1", c.Contract.Components["semantic"])
	}
	if c.APIVersion != "gt.decoder/v2" {
		t.Errorf("api_version = %q, want gt.decoder/v2", c.APIVersion)
	}
	if c.Heartbeat.DefaultIntervalSec == 0 || c.Heartbeat.OfflineTimeoutSec == 0 {
		t.Errorf("heartbeat spec not parsed: %+v", c.Heartbeat)
	}
	if len(c.RegistryDiscovery) == 0 {
		t.Error("registry_discovery is empty")
	}
	if c.LinkTypes.Standard[1] != "Ethernet" {
		t.Errorf("link_types.standard[1] = %q, want Ethernet", c.LinkTypes.Standard[1])
	}
}

// TestOutputContractIsV2 是本次迁移的核心守卫。
//
// 迁移前 contract.yaml 的 decode_v2.response 已是 payload_msgpack，而
// output_contract 段仍在描述 v1 的 JSON 产出（data / _fields 两个子对象），
// 同一文件自相矛盾。契约与实现分处两仓库时这种漂移无人发现，
// 现在由本测试在 SDK 侧钉死。
func TestOutputContractIsV2(t *testing.T) {
	c := Default()

	if c.OutputContract.Encoding != "msgpack" {
		t.Errorf("output_contract.encoding = %q, want msgpack", c.OutputContract.Encoding)
	}
	if c.OutputContract.Builder != "event.Value" {
		t.Errorf("output_contract.builder = %q, want event.Value", c.OutputContract.Builder)
	}
	if c.OutputContract.Marshal == "" || c.OutputContract.Unmarshal == "" {
		t.Errorf("output_contract marshal/unmarshal incomplete: %+v", c.OutputContract)
	}

	// decode_v2 的 response 字段必须与 output_contract 一致。
	resp := c.RPC.Methods["decode_v2"].Response
	if _, ok := resp["payload_msgpack"]; !ok {
		t.Error("decode_v2.response missing payload_msgpack")
	}
	for _, dead := range []string{"data", "_fields", "payload_json"} {
		if _, ok := resp[dead]; ok {
			t.Errorf("decode_v2.response still declares v1 field %q", dead)
		}
	}
	if c.RPC.Methods["decode_v2"].Type != "bidi_stream" {
		t.Errorf("decode_v2.type = %q, want bidi_stream", c.RPC.Methods["decode_v2"].Type)
	}
	// v1 的 decode RPC 已从 proto 移除，契约不应再声明。
	if _, ok := c.RPC.Methods["decode"]; ok {
		t.Error("contract still declares removed v1 decode RPC")
	}
}

// TestRegisterIsTunnelOnly 守卫「宿主不回拨」这一契约事实，与 TestOutputContractIsV2
// 同属一类：契约不得比实现多声明字段。
//
// socket_path / tunnel 已从 RegisterRequest 删除（plugin.proto 保留号 1、3）：宿主统一按
// 隧道处理，解码走插件主动拨出的 Connect 双向流，插件没有可上报的本地端点。契约若重新
// 声明这两个字段、或不声明 Connect，说明非隧道回拨路径被回滚——本测试钉死它。
func TestRegisterIsTunnelOnly(t *testing.T) {
	c := Default()

	reg, ok := c.RPC.Methods["register"]
	if !ok {
		t.Fatal("contract declares no register method")
	}
	for _, dead := range []string{"socket_path", "tunnel"} {
		if _, ok := reg.Request[dead]; ok {
			t.Errorf("register.request still declares removed field %q", dead)
		}
	}
	if _, ok := reg.Request["manifest"]; !ok {
		t.Error("register.request missing manifest")
	}

	// Connect 是唯一的解码通道，契约必须声明它，否则读者会以为宿主仍按需拨号
	// DecodeV2（decode_v2 现在只经该流暴露）。
	conn, ok := c.RPC.Methods["connect"]
	if !ok {
		t.Fatal("contract does not declare the connect tunnel method")
	}
	if conn.Type != "bidi_stream" {
		t.Errorf("connect.type = %q, want bidi_stream", conn.Type)
	}
}


// reserved_payload_fields 是宿主真实消费的字段，迁移前只存在于 gametrace 代码里，
// 插件作者无从得知。这里确认它们被完整描述且标注了消费方。
func TestReservedPayloadFields(t *testing.T) {
	c := Default()

	meta, ok := c.ReservedPayloadFields["_meta"]
	if !ok {
		t.Fatal("reserved_payload_fields missing _meta")
	}
	dir, ok := meta.Fields["direction"]
	if !ok {
		t.Fatal("_meta missing direction")
	}
	if len(dir.Enum) == 0 {
		t.Error("_meta.direction should enumerate allowed values")
	}
	if dir.ConsumedBy == "" {
		t.Error("_meta.direction should record its consumer")
	}

	sc, ok := c.ReservedPayloadFields["_state_changes"]
	if !ok {
		t.Fatal("reserved_payload_fields missing _state_changes")
	}
	if sc.Items == nil || sc.Items.Spec == nil {
		t.Fatal("_state_changes.items should parse as a full field spec")
	}
	wantRequired := map[string]bool{"subject_type": true, "subject_id": true, "op": true, "path": true}
	for _, r := range sc.Items.Spec.Required {
		delete(wantRequired, r)
	}
	if len(wantRequired) != 0 {
		t.Errorf("_state_changes.items.required missing %v", wantRequired)
	}
}

// 规则 ID 命名空间：gt.<layer>.<name> 点分式（semantic 层规则即 gt.semantic.*）。
var (
	kebabPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	namespacedPattern = regexp.MustCompile(`^gt\.[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`)
)

func TestRulesWellFormed(t *testing.T) {
	c := Default()
	if len(c.Rules) == 0 {
		t.Fatal("rules section is empty")
	}

	seen := make(map[string]bool, len(c.Rules))
	for _, r := range c.Rules {
		if !kebabPattern.MatchString(r.ID) && !namespacedPattern.MatchString(r.ID) {
			t.Errorf("rule id %q must be kebab-case or gt.<layer>.<name> namespaced", r.ID)
		}
		if seen[r.ID] {
			t.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true

		if r.Topic == "" {
			t.Errorf("rule %q missing topic", r.ID)
		}
		if r.Severity != SeverityError && r.Severity != SeverityWarn && r.Severity != "info" {
			t.Errorf("rule %q severity = %q, want error|warn|info", r.ID, r.Severity)
		}
		if strings.TrimSpace(r.Statement) == "" {
			t.Errorf("rule %q missing statement", r.ID)
		}
		// doc_ref 是人读文档与机读规则的锚点，缺了就无法从 violation 跳回文档。
		if r.DocRef == "" {
			t.Errorf("rule %q missing doc_ref", r.ID)
		}
	}
}

func TestFilterRules(t *testing.T) {
	c := Default()

	all := c.FilterRules(RuleFilter{})
	if len(all) != len(c.Rules) {
		t.Errorf("empty filter returned %d rules, want %d", len(all), len(c.Rules))
	}
	// 结果需按 (topic, id) 稳定排序，保证 plugin.brief 输出可复现。
	for i := 1; i < len(all); i++ {
		prev, cur := all[i-1], all[i]
		if prev.Topic > cur.Topic || (prev.Topic == cur.Topic && prev.ID > cur.ID) {
			t.Fatalf("rules not sorted at %d: %s/%s before %s/%s", i, prev.Topic, prev.ID, cur.Topic, cur.ID)
		}
	}

	errs := c.FilterRules(RuleFilter{Severity: SeverityError})
	if len(errs) == 0 {
		t.Fatal("severity filter returned 0 rules")
	}
	for _, r := range errs {
		if r.Severity != SeverityError {
			t.Errorf("rule %q leaked into error filter with severity %q", r.ID, r.Severity)
		}
	}

	if got := c.FilterRules(RuleFilter{Topic: "nope"}); len(got) != 0 {
		t.Errorf("unknown topic returned %d rules", len(got))
	}
}

func TestTopics(t *testing.T) {
	c := Default()
	topics := Default().Topics()
	if len(topics) == 0 {
		t.Fatal("no topics")
	}
	for i := 1; i < len(topics); i++ {
		if topics[i-1] >= topics[i] {
			t.Fatalf("topics not sorted/deduped: %v", topics)
		}
	}
	// 每个 topic 都应能筛出规则，且总数等于全集。
	total := 0
	for _, tp := range topics {
		n := len(c.FilterRules(RuleFilter{Topic: tp}))
		if n == 0 {
			t.Errorf("topic %q has no rules", tp)
		}
		total += n
	}
	if total != len(c.Rules) {
		t.Errorf("topic partition covers %d rules, want %d", total, len(c.Rules))
	}
}

// checker 的 kebab-case 归因依赖 manifest_schema 里的 pattern，
// 该字段一旦缺失会静默退化成硬编码正则，这里钉死它必须存在。
func TestManifestSchemaParsed(t *testing.T) {
	c := Default()
	if got := c.ManifestSchema.Fields["name"].Pattern; got == "" {
		t.Error("manifest_schema.fields.name.pattern is empty")
	}
	if !c.ManifestSchema.Fields["hints"].Optional {
		t.Error("manifest_schema.fields.hints should be optional")
	}
	if c.ManifestSchema.Fields["hints"].Items == nil || c.ManifestSchema.Fields["hints"].Items.TypeName != "string" {
		t.Error("manifest_schema hints.items should parse as scalar type name")
	}
	wantRequired := map[string]bool{"api_version": true, "name": true, "protocol": true, "type": true}
	for _, r := range c.ManifestSchema.Required {
		delete(wantRequired, r)
	}
	if len(wantRequired) != 0 {
		t.Errorf("manifest_schema.required missing %v", wantRequired)
	}
}

func TestRawYAMLNotEmpty(t *testing.T) {
	if len(RawYAML()) == 0 {
		t.Fatal("embedded contract.yaml is empty")
	}
}

// TestContractComponentsLoad 保证语义契约版本块仅含 semantic 组件且版本 >= 1。
// runtime / event / schema / state 各层已随语义契约收敛移除（spec_version 6），
// 若重新出现说明契约被错误回滚。
func TestContractComponentsLoad(t *testing.T) {
	c := Default()
	if c.Contract.Name != "gt.plugin" {
		t.Errorf("contract.name = %q, want gt.plugin", c.Contract.Name)
	}
	if v, ok := c.Contract.Components["semantic"]; !ok || v < 1 {
		t.Errorf("contract block missing semantic component or version < 1: %v", c.Contract.Components)
	}
	for _, gone := range []string{"runtime", "event", "schema", "state"} {
		if _, ok := c.Contract.Components[gone]; ok {
			t.Errorf("component %q should have been removed", gone)
		}
	}
}
