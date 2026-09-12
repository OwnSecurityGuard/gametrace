package plugin

import (
	sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
)

// Manifest 及其附属类型的定义已迁入 SDK（github.com/OwnSecurityGuard/gt-plugin-sdk）。
//
// 迁移前 gametrace 与 SDK 各持有一份逐字段相同的定义，两边独立演进必然漂移：
// 插件按 SDK 的结构产出 plugin.yaml，宿主按自己那份解析，字段一旦不同步就是
// 注册期才暴露的静默错误。现在契约单向流动——SDK 定义，gametrace 消费。
//
// 这里用类型别名而非新类型，保证 plugin.Manifest 与 sdk.Manifest 完全等价：
// 宿主代码继续写 plugin.Manifest，插件侧传来的 sdk.Manifest 可直接赋值，无需转换。
type (
	Manifest     = sdk.Manifest
	EventSpec    = sdk.EventSpec
	FieldDecl    = sdk.FieldDecl
	DataSpec     = sdk.DataSpec
	DataSchema   = sdk.DataSchema
	ManifestMeta = sdk.ManifestMeta
)

// ParseManifest 解析 plugin.yaml 原文返回 Manifest。
// 不做字段校验，校验由 ValidateManifest 完成。
func ParseManifest(data []byte) (*Manifest, error) {
	return sdk.ParseManifest(data)
}

// ValidateManifest 校验 manifest 必填字段与格式约束。
//
// 只校验形态（api_version 匹配 gt.decoder/v<digit>、name 为 kebab-case 等），
// 不判定版本兼容性——major 是否与宿主一致由 CheckManifestVersion 负责。
func ValidateManifest(m *Manifest) error {
	return sdk.ValidateManifest(m)
}
