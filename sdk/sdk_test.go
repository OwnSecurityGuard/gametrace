package sdk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadManifest_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	yamlContent := `api_version: gt.decoder/v1
name: test-plugin
protocol: test_proto
type: decoder
`
	if err := os.WriteFile("plugin.yaml", []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write plugin.yaml: %v", err)
	}

	data, err := ReadManifest()
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ReadManifest returned empty data")
	}
	if string(data) != yamlContent {
		t.Errorf("ReadManifest returned unexpected content: %q", string(data))
	}
}

func TestReadManifest_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	_, err := ReadManifest()
	if err == nil {
		t.Fatal("expected error for missing plugin.yaml, got nil")
	}
	if !filepath.IsAbs(err.Error()) && err.Error() != "read plugin.yaml: open plugin.yaml: The system cannot find the file specified." {
		t.Logf("error message: %v", err)
	}
}

func TestReadManifest_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	if err := os.WriteFile("plugin.yaml", []byte("not: valid: yaml:{{"), 0644); err != nil {
		t.Fatalf("write plugin.yaml: %v", err)
	}

	_, err := ReadManifest()
	if err == nil {
		t.Fatal("expected error for invalid yaml, got nil")
	}
}

func TestReadManifest_InvalidManifest(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// 缺少必填字段 name
	yamlContent := `api_version: gt.decoder/v1
protocol: test_proto
type: decoder
`
	if err := os.WriteFile("plugin.yaml", []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write plugin.yaml: %v", err)
	}

	_, err := ReadManifest()
	if err == nil {
		t.Fatal("expected error for invalid manifest, got nil")
	}
}

func TestResolveRegistryAddr_Default(t *testing.T) {
	t.Setenv("GT_REGISTRY_ADDR", "")

	// 未设置 GT_REGISTRY_ADDR 且未传 --registry= 时默认回退 :9091（TCP）。
	addr := ResolveRegistryAddr()
	if addr != ":9091" {
		t.Errorf("ResolveRegistryAddr() = %q, want :9091", addr)
	}
}

func TestResolveRegistryAddr_EnvVar(t *testing.T) {
	t.Setenv("GT_REGISTRY_ADDR", "unix:/custom/registry.sock")

	addr := ResolveRegistryAddr()
	expected := "unix:/custom/registry.sock"
	if addr != expected {
		t.Errorf("ResolveRegistryAddr() = %q, want %q", addr, expected)
	}
}

func TestResolveRegistryAddr_Flag(t *testing.T) {
	// 环境变量优先级高于 flag
	t.Setenv("GT_REGISTRY_ADDR", "unix:/env/registry.sock")
	origArgs := os.Args
	os.Args = []string{"plugin", "--registry=unix:/flag/registry.sock"}
	defer func() { os.Args = origArgs }()

	addr := ResolveRegistryAddr()
	// 环境变量优先
	expected := "unix:/env/registry.sock"
	if addr != expected {
		t.Errorf("ResolveRegistryAddr() = %q, want %q", addr, expected)
	}
}

func TestResolveRegistryAddr_FlagOnly(t *testing.T) {
	t.Setenv("GT_REGISTRY_ADDR", "")
	origArgs := os.Args
	os.Args = []string{"plugin", "--registry=unix:/flag/registry.sock"}
	defer func() { os.Args = origArgs }()

	addr := ResolveRegistryAddr()
	expected := "unix:/flag/registry.sock"
	if addr != expected {
		t.Errorf("ResolveRegistryAddr() = %q, want %q", addr, expected)
	}
}
