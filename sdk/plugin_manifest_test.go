package sdk

import (
	"strings"
	"testing"
)

// 本文件的用例自 gametrace/pkg/plugin/manifest_test.go 迁入。
// Manifest 类型与校验逻辑现由 SDK 单一持有，其单元测试也随之落在 SDK。
// gametrace 侧只保留宿主特有行为（版本协商、schema registry 投影）的测试。

func TestParseManifest_Valid(t *testing.T) {
	yaml := `api_version: gt.decoder/v2
name: my_game_decoder
protocol: my_game
type: decoder
protocol_version: game/v3
hints:
  - tcp
  - "port:7000"
event:
  fields:
    direction: { type: string, enum: [client_to_server, server_to_client, unknown] }
    msg_name:  { type: string }
  data:
    schema:
      fields:
        cmd_id: { type: number }
        body:   { type: object }
meta:
  author: example
  description: "My game protocol decoder"
`
	m, err := ParseManifest([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	if m.APIVersion != "gt.decoder/v2" {
		t.Errorf("APIVersion = %q, want gt.decoder/v2", m.APIVersion)
	}
	// ParseManifest 刻意不校验：name 用下划线也应能解析出来，
	// 格式判定是 ValidateManifest 的职责。
	if m.Name != "my_game_decoder" {
		t.Errorf("Name = %q, want my_game_decoder", m.Name)
	}
	if m.Protocol != "my_game" {
		t.Errorf("Protocol = %q, want my_game", m.Protocol)
	}
	if m.Type != "decoder" {
		t.Errorf("Type = %q, want decoder", m.Type)
	}
	if m.ProtocolVersion != "game/v3" {
		t.Errorf("ProtocolVersion = %q, want game/v3", m.ProtocolVersion)
	}
	if len(m.Hints) != 2 || m.Hints[0] != "tcp" || m.Hints[1] != "port:7000" {
		t.Errorf("Hints = %v, want [tcp port:7000]", m.Hints)
	}
	if m.Event.Fields["direction"].Type != "string" {
		t.Errorf("Event.Fields[direction].Type = %q, want string", m.Event.Fields["direction"].Type)
	}
	if m.Event.Data.Schema.Fields["cmd_id"].Type != "number" {
		t.Errorf("Event.Data.Schema.Fields[cmd_id].Type = %q, want number", m.Event.Data.Schema.Fields["cmd_id"].Type)
	}
	if m.Meta.Author != "example" {
		t.Errorf("Meta.Author = %q, want example", m.Meta.Author)
	}
}

func TestParseManifest_InvalidYAML(t *testing.T) {
	_, err := ParseManifest([]byte("not: valid: yaml:"))
	if err == nil {
		t.Fatal("expected error for invalid yaml, got nil")
	}
	if !strings.Contains(err.Error(), "parse manifest") {
		t.Errorf("error %q should contain 'parse manifest'", err.Error())
	}
}

func TestValidateManifest_Valid(t *testing.T) {
	m := &Manifest{
		APIVersion: "gt.decoder/v2",
		Name:       "my-game-decoder",
		Protocol:   "my_game",
		Type:       "decoder",
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("ValidateManifest: %v", err)
	}
}

func TestValidateManifest_MissingRequired(t *testing.T) {
	tests := []struct {
		name     string
		manifest *Manifest
		errSub   string
	}{
		{"missing api_version", &Manifest{Name: "x", Protocol: "p", Type: "decoder"}, "api_version"},
		{"missing name", &Manifest{APIVersion: "gt.decoder/v2", Protocol: "p", Type: "decoder"}, "name"},
		{"missing protocol", &Manifest{APIVersion: "gt.decoder/v2", Name: "x", Type: "decoder"}, "protocol"},
		{"missing type", &Manifest{APIVersion: "gt.decoder/v2", Name: "x", Protocol: "p"}, "type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManifest(tt.manifest)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.errSub)
			}
			if !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.errSub)
			}
		})
	}
}

func TestValidateManifest_InvalidNameFormat(t *testing.T) {
	for _, name := range []string{
		"My_Game", // 大写
		"my game", // 空格
		"1game",   // 数字开头
		"my_game", // 下划线（非 kebab-case）
		"-game",   // 连字符开头
	} {
		t.Run(name, func(t *testing.T) {
			m := &Manifest{APIVersion: "gt.decoder/v2", Name: name, Protocol: "p", Type: "decoder"}
			err := ValidateManifest(m)
			if err == nil {
				t.Fatalf("expected error for invalid name %q, got nil", name)
			}
			if !strings.Contains(err.Error(), "name") {
				t.Fatalf("error %q does not contain 'name'", err.Error())
			}
		})
	}
}

func TestValidateManifest_InvalidType(t *testing.T) {
	m := &Manifest{APIVersion: "gt.decoder/v2", Name: "x", Protocol: "p", Type: "encoder"}
	err := ValidateManifest(m)
	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}
	if !strings.Contains(err.Error(), "type") {
		t.Fatalf("error %q does not contain 'type'", err.Error())
	}
}

func TestParseManifest_Transports(t *testing.T) {
	yaml := `api_version: gt.decoder/v2
name: udp-game
protocol: custom_dgram
type: decoder
transports:
  - tcp
  - udp
`
	m, err := ParseManifest([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(m.Transports) != 2 || m.Transports[0] != "tcp" || m.Transports[1] != "udp" {
		t.Fatalf("Transports = %v, want [tcp udp]", m.Transports)
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("ValidateManifest: %v", err)
	}
}

func TestValidateManifest_Transports(t *testing.T) {
	base := &Manifest{APIVersion: "gt.decoder/v2", Name: "x", Protocol: "p", Type: "decoder"}

	// 合法取值：tcp / udp / 两者组合。
	for _, transports := range [][]string{
		nil, // 未声明可选
		{"tcp"},
		{"udp"},
		{"tcp", "udp"},
		{"udp", "tcp"},
	} {
		m := *base
		m.Transports = transports
		if err := ValidateManifest(&m); err != nil {
			t.Fatalf("transports %v: unexpected error %v", transports, err)
		}
	}

	// 非法取值：非 tcp/udp 条目。
	for _, transports := range [][]string{
		{"http"},
		{"sctp"},
		{"UDP"}, // 大小写敏感
		{"tcp", "udp", "icmp"},
	} {
		m := *base
		m.Transports = transports
		err := ValidateManifest(&m)
		if err == nil {
			t.Fatalf("transports %v: expected error, got nil", transports)
		}
		if !strings.Contains(err.Error(), "transports") {
			t.Fatalf("transports %v: error %q does not mention transports", transports, err.Error())
		}
	}

	// 重复条目。
	dup := *base
	dup.Transports = []string{"tcp", "tcp"}
	if err := ValidateManifest(&dup); err == nil {
		t.Fatal("duplicate transports: expected error, got nil")
	}
}

func TestValidateManifest_InvalidAPIVersion(t *testing.T) {
	for _, apiVersion := range []string{
		"v1",           // 缺少 gt.decoder/ 前缀
		"gt.decoder/",  // 缺版本号
		"gt.other/v2",  // 非 decoder
		"gt.decoder/2", // 缺 v
	} {
		t.Run(apiVersion, func(t *testing.T) {
			m := &Manifest{APIVersion: apiVersion, Name: "x", Protocol: "p", Type: "decoder"}
			err := ValidateManifest(m)
			if err == nil {
				t.Fatalf("expected error for api_version %q, got nil", apiVersion)
			}
			if !strings.Contains(err.Error(), "api_version") {
				t.Fatalf("error %q does not contain 'api_version'", err.Error())
			}
		})
	}
}

// ValidateManifest 只管形态，v1 也能过——major 兼容性判定交给宿主版本门槛
// （gametrace 的 CheckManifestVersion）。这条边界如果被误改，插件会在注册阶段才暴露
// 问题，故显式钉住。
func TestValidateManifest_AcceptsAnyMajorFormat(t *testing.T) {
	m := &Manifest{APIVersion: "gt.decoder/v1", Name: "x", Protocol: "p", Type: "decoder"}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("ValidateManifest should only check shape, got %v", err)
	}
}

// TestLegacyManifestStillValid 钉住：不含任何语义声明块的“传统” plugin.yaml
// （只有 api_version/name/protocol/type + 可选 hints/meta）仍能正确解析并通过校验。
// 这是向后兼容的硬约束——既有插件与新 SDK 混用时不能因为多了新字段就解析失败。
func TestLegacyManifestStillValid(t *testing.T) {
	yaml := `api_version: gt.decoder/v2
name: legacy-game
protocol: legacy_game
type: decoder
hints:
  - tcp
meta:
  author: someone
`
	m, err := ParseManifest([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseManifest(legacy): %v", err)
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("ValidateManifest(legacy): %v", err)
	}
	if m.Contract != nil {
		t.Errorf("legacy manifest should leave Contract nil, got %+v", m.Contract)
	}
	if len(m.SemanticRules) != 0 {
		t.Errorf("legacy manifest should have no semantic rules, got %d", len(m.SemanticRules))
	}
}

// TestParseManifest_SemanticRules 钉住 semantic_rules 声明能正确解析。
func TestParseManifest_SemanticRules(t *testing.T) {
	yaml := `api_version: gt.decoder/v2
name: rules-game
protocol: rules_game
type: decoder
semantic_rules:
  - id: rules_game.pair_request_response
    when:
      - { path: seqId, op: neq, value: 0 }
    effect:
      type: pair
      sides:
        - { path: direction, op: eq, value: client_to_server, key: seqId }
        - { path: direction, op: eq, value: server_to_client, key: meta.req_seq }
  - id: rules_game.mark_error
    when: [ { path: error, op: exists } ]
    effect: { type: annotate, semantic: error }
`
	m, err := ParseManifest([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(m.SemanticRules) != 2 {
		t.Fatalf("SemanticRules = %d, want 2", len(m.SemanticRules))
	}
	if m.SemanticRules[0].Effect.Type != "pair" || len(m.SemanticRules[0].Effect.Sides) != 2 {
		t.Errorf("rule[0] = %+v", m.SemanticRules[0])
	}
	if m.SemanticRules[0].Effect.Sides[0].Key != "seqId" || m.SemanticRules[0].Effect.Sides[0].Path != "direction" {
		t.Errorf("rule[0] sides[0] = %+v", m.SemanticRules[0].Effect.Sides[0])
	}
	if m.SemanticRules[0].Effect.Sides[1].Key != "meta.req_seq" {
		t.Errorf("rule[0] sides[1] = %+v", m.SemanticRules[0].Effect.Sides[1])
	}
	if m.SemanticRules[1].Effect.Semantic != "error" {
		t.Errorf("rule[1] semantic = %q", m.SemanticRules[1].Effect.Semantic)
	}
}
