package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// capabilityCatalog 是 get_capabilities 的静态目录：按工作流分组列出全部
// 工具与推荐调用链。给 AI Agent 一个自描述入口，避免靠 README 或试错来
// 理解工具之间的关系。新增工具时同步维护本目录（与 main.go 的 AddTool 对齐）。
type toolGroup struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tools       []string `json:"tools"`
}

type capabilityDoc struct {
	Server      string      `json:"server"`
	Groups      []toolGroup `json:"groups"`
	TypicalFlow []string    `json:"typical_flow"`
	Notes       []string    `json:"notes"`
	// Skills 是 Skill Catalog：扫描 skills/ 目录（每个 SKILL.md 一个条目）得出，
	// 描述平台的技能方法论与触发场景，供 AI agent 接入初始化时了解。
	Skills []SkillInfo `json:"skills"`
}

func buildCapabilityCatalog() capabilityDoc {
	return capabilityDoc{
		Server: "game-debug-automation",
		Groups: []toolGroup{
			{
				Name:        "capture",
				Description: "抓包与会话生命周期",
				Tools: []string{
					"start_capture", "stop_capture", "get_session_status",
					"list_interfaces", "list_live_sessions", "set_session_plugin",
					"list_all_sessions", "delete_session",
				},
			},
			{
				Name:        "proxy",
				Description: "移动代理抓包租约（按用户/设备独立会话，多用户互不串流）",
				Tools:       []string{"create_proxy_lease", "list_proxy_leases", "get_proxy_lease", "release_proxy_lease"},
			},
			{
				Name:        "query",
				Description: "解码事件 / 协议目录 / 状态 / 执行链查询",
				Tools:       []string{"list_decoded_data", "get_protocol_catalog", "list_state_changes"},
			},
			{
				Name:        "plugin-dev",
				Description: "Developer Plane：脚手架 / 编译 / 拉起 / 归因",
				Tools: []string{
					"create_plugin", "build_plugin", "activate_plugin", "deactivate_plugin",
					"status_plugin", "explain_plugin",
				},
			},
			{
				Name:        "plugin-verify",
				Description: "Runtime Plane：契约校验与受限取样",
				Tools:       []string{"test_plugin", "verify_plugin", "sample_bytes_plugin"},
			},
			{
				Name:        "plugin-runtime",
				Description: "注册表观测与 manifest",
				Tools: []string{
					"list_plugins", "list_registered_plugins",
					"get_plugin_manifest", "deregister_plugin", "get_registry_addr",
					"get_plugin_env",
				},
			},
			{
				Name:        "plugin-knowledge",
				Description: "契约 SSOT 与开发指南（写插件前先读）",
				Tools:       []string{"get_plugin_contract", "get_plugin_dev_guide", "get_capabilities"},
			},
			{
				Name:        "raw-debug",
				Description: "原始包调试，需服务端 -enable-raw-debug，默认不注册",
				Tools:       []string{"list_raw_packets", "decode_raw_packets"},
			},
		},
		TypicalFlow: []string{
			"接入新协议: get_plugin_dev_guide -> create_plugin -> build_plugin -> start_capture(plugin=...) -> activate_plugin -> verify_plugin -> list_decoded_data",
			"定位解码为空: status_plugin -> get_registry_addr -> sample_bytes_plugin -> explain_plugin",
		},
		Notes:       []string{},
	}
}

func (m *mcpCapture) handleGetCapabilities(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	doc := buildCapabilityCatalog()
	doc.Skills = loadSkillCatalog()
	return successResult(doc), nil
}
