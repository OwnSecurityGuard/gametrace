package sdk

import (
	"fmt"
	"os"
)

// ReadManifest 读取当前目录的 plugin.yaml 并校验，返回原始 bytes。
// 用于 Register RPC 的 manifest 参数。
// 返回 error 当 plugin.yaml 不存在、格式非法或字段校验不通过时。
//
// 直接使用本 SDK 内置的 ParseManifest/ValidateManifest，不依赖 gametrace 根模块。
func ReadManifest() ([]byte, error) {
	data, err := os.ReadFile("plugin.yaml")
	if err != nil {
		return nil, fmt.Errorf("read plugin.yaml: %w", err)
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("parse plugin.yaml: %w", err)
	}
	if err := ValidateManifest(m); err != nil {
		return nil, fmt.Errorf("validate plugin.yaml: %w", err)
	}
	return data, nil
}

// ResolveRegistryAddr 返回 registry 端点地址。
// 优先级：GT_REGISTRY_ADDR 环境变量 > --registry= 命令行参数 > 默认 :9091（TCP）。
func ResolveRegistryAddr() string {
	if addr := os.Getenv("GT_REGISTRY_ADDR"); addr != "" {
		return addr
	}
	for _, arg := range os.Args {
		if len(arg) >= 11 && arg[:11] == "--registry=" {
			return arg[11:]
		}
	}
	return ":9091"
}
