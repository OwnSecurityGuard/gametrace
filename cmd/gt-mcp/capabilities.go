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
					"list_live_sessions", "set_session_plugin",
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
				Description: "解码事件 / 协议目录 / 状态 / 命中提醒 / 执行链查询",
				Tools:       []string{"list_decoded_data", "get_protocol_catalog", "list_state_changes", "list_session_alerts"},
			},
			{
				Name:        "plugin-dev",
				Description: "插件开发：脚手架渲染（不落盘）与实例接入 / 归因",
				Tools: []string{
					"scaffold_plugin", "connect_plugin", "status_plugin", "explain_plugin",
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
					"list_registered_plugins",
					"get_plugin_manifest", "deregister_plugin", "get_registry_addr",
					"get_plugin_env",
				},
			},
			{
				Name:        "plugin-knowledge",
				Description: "契约 SSOT 与开发指南（写插件前先读）",
				Tools:       []string{"get_plugin_contract", "get_plugin_dev_guide", "get_capabilities", "read_skill"},
			},
			{
				Name:        "raw-debug",
				Description: "原始包调试，默认注册（GT_MCP_ENABLE_RAW_DEBUG=0 才收回）",
				Tools:       []string{"list_raw_packets", "decode_raw_packets"},
			},
		},
		TypicalFlow: []string{
			"接入新协议: get_plugin_dev_guide -> scaffold_plugin(把返回的 contents 写进你自己的 workspace；平台不存插件源码) -> 本机编码 + 单测 + go build -> get_plugin_env(把 env_file 写进插件工作目录 .env) -> 在本机启动插件进程 -> connect_plugin(只看 status=ready|failed) -> test_plugin(看解码结果，不落库) -> verify_plugin(分层结论 session_profile/applicability/checks/verdict，不落库)",
			"真实落库验证: live capture 使用该插件 或 decode_raw_packets(需 -enable-raw-debug) -> list_decoded_data -> get_protocol_catalog",
			"定位解码为空: status_plugin -> get_registry_addr -> sample_bytes_plugin -> explain_plugin",
		},
		Notes: []string{
			"插件源码、二进制与进程都在用户自己的机器上：平台不编译、不拉起、也不保存插件源码；scaffold_plugin 只返回文件内容，落盘由 Agent 完成",
			"test_plugin / verify_plugin 都是对离线会话的隔离回放，结果不写 events 表；要看真实落库数据必须走 live capture 或 decode_raw_packets",
			"connect_plugin 的结论只有一个 status=ready|failed；failed 时看 stage(断点 auth|connection|manifest) + reason(原因) + next(该做什么)，机器字段 registered/online/manifest_present 仅供自查",
			"verify_plugin 的 verdict=not_applicable 表示会话里没有插件该解的流量（applicability.result=not_match，quality 为 null）——换会话重跑，不要去改插件",
			"status_plugin 只有两个视角：runtime（registry：offline|registered|active）与 validation（该插件实例是否通过过 verify，证据在平台数据库）；平台没有制品/二进制视角",
		},
	}
}

func (m *mcpCapture) handleGetCapabilities(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	doc := buildCapabilityCatalog()
	doc.Skills = loadSkillCatalog()
	return successResult(doc), nil
}
