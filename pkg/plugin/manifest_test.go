package plugin

import (
	"strings"
	"testing"

	sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
)

// Manifest 的解析与字段校验现由 SDK 单一持有，其单元测试也在 SDK
// （plugin_manifest_test.go）。本文件只测宿主特有行为：
//   - 类型别名等价性（别名一旦被改成新类型，跨模块赋值会静默断裂）
//   - 版本协商 CheckManifestVersion 与 ValidateManifest 的职责边界

// 别名必须与 SDK 类型完全等价，否则插件侧传来的 sdk.Manifest 无法直接赋值。
func TestManifestIsSDKAlias(t *testing.T) {
	var m Manifest
	var s sdk.Manifest = m // 编译期即断言等价
	_ = s
}

func TestManifestE2E_FullFlow(t *testing.T) {
	// 模拟插件传给 Register RPC 的 plugin.yaml 原文
	yamlContent := `api_version: gt.decoder/v2
name: lol-decoder
protocol: lol
type: decoder
protocol_version: game/patch-15.6
hints:
  - tcp
  - "port:7000"
meta:
  author: riot
  description: "League of Legends protocol decoder"
`

	m, err := ParseManifest([]byte(yamlContent))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("ValidateManifest: %v", err)
	}
	if err := CheckManifestVersion(m); err != nil {
		t.Fatalf("CheckManifestVersion: %v", err)
	}

	if m.Name != "lol-decoder" || m.Protocol != "lol" {
		t.Errorf("Name/Protocol = %q/%q, want lol-decoder/lol", m.Name, m.Protocol)
	}
	if m.ProtocolVersion != "game/patch-15.6" {
		t.Errorf("ProtocolVersion = %q, want game/patch-15.6", m.ProtocolVersion)
	}
	if len(m.Hints) != 2 || m.Hints[1] != "port:7000" {
		t.Errorf("Hints = %v, want [tcp port:7000]", m.Hints)
	}
	if m.Meta.Author != "riot" {
		t.Errorf("Meta.Author = %q, want riot", m.Meta.Author)
	}
}

// 职责边界：api_version 形态合法但 major 不匹配时，
// ValidateManifest 必须放行，CheckManifestVersion 必须拦下。
func TestManifestE2E_VersionMismatch(t *testing.T) {
	yamlContent := `api_version: gt.decoder/v1
name: future-decoder
protocol: future
type: decoder
`
	m, err := ParseManifest([]byte(yamlContent))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("ValidateManifest should pass for v1 format: %v", err)
	}
	err = CheckManifestVersion(m)
	if err == nil {
		t.Fatal("CheckManifestVersion should fail for v1 vs v2")
	}
	if !strings.Contains(err.Error(), "version mismatch") {
		t.Fatalf("error %q should contain 'version mismatch'", err.Error())
	}
}
