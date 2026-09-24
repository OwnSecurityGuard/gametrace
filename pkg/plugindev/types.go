// Package plugindev 是解码插件的开发辅助库，纯进程内、无 gRPC、无 MCP，且
// **不写任何文件、不编译、不拉起进程**。它只做两件事：
//
//  1. 渲染插件脚手架模板（ScaffoldPlugin）：插件源码由调用方（Agent）在自己的
//     workspace 落盘，平台不保存、不持有用户插件源码；
//  2. 归因 verify 结果的解码类失败（Explain），供 plugin.explain 消费。
//
// 平台侧不再有「插件目录」：插件的构建与运行都在用户自己的机器上完成，插件启动后
// 主动注册到平台 registry；平台只负责注册/心跳/验证等运行期管理。
//
// 目录发现（ListPlugins）是唯一的磁盘扫描，且**只服务于用户侧的 gt-agent**——
// 它托管的是用户本机目录下的插件，与平台无关。
package plugindev

// ScaffoldResult 是脚手架渲染结果：只返回内容，不落盘。
//
// 调用方必须把 Contents 写入自己的 workspace，路径由 Files 给出（相对路径，
// 顺序稳定）。平台不返回、也不接受任何输出目录 —— 落盘位置完全由调用方决定。
type ScaffoldResult struct {
	// Template 是渲染所用的模板标识，便于调用方/用户知道文件来源。
	Template string
	// Files 是需要创建的文件（相对路径，顺序与 Contents 的语义顺序一致）。
	Files []string
	// Contents 是 文件相对路径 -> 渲染后的完整内容。
	Contents map[string]string
	// SDKVersion 是脚手架实际引用的 gametrace/sdk 版本。
	SDKVersion string
	// FramingAvailable 是生成代码是否依赖 framing 包（false 时生成代码已显式
	// 标注「framing 不可用」，不会引用缺失的导入）。
	FramingAvailable bool
}

// DiscoveredPlugin is a plugin found on disk by ListPlugins. Only the user-side
// gt-agent uses this: it hosts plugins from a directory on the user's own
// machine. The platform never scans for plugin sources.
type DiscoveredPlugin struct {
	Name   string
	Binary string
	Dir    string
}