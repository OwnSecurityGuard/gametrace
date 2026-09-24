package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/plugindev"
)

var pluginNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// handleScaffoldPlugin 渲染插件脚手架并**把内容返回给调用方**，由 Agent 在自己的
// workspace 落盘。
//
// 平台不写任何文件、不编译、也不持有插件目录：源码的生命周期完全属于用户侧。
// 返回 Contents 而不是写盘，是为了让 Agent 明确知道「这些文件要建在你自己那边」，
// 而不是以为平台远端替它建好了目录。
func (m *mcpCapture) handleScaffoldPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if name == "" {
		return errorResult(fmt.Errorf("name is required")), nil
	}
	if !pluginNameRe.MatchString(name) {
		return errorResult(fmt.Errorf("name must be kebab-case (lowercase letters, digits, hyphens; must start with a letter), got %q", name)), nil
	}
	protocol := req.GetString("protocol", "")
	if protocol == "" {
		return errorResult(fmt.Errorf("protocol is required")), nil
	}
	protocolVersion := req.GetString("protocol_version", "")

	var hints []string
	if raw := req.GetString("hints", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &hints); err != nil {
			// fall back to comma-separated
			for _, h := range strings.Split(raw, ",") {
				if h = strings.TrimSpace(h); h != "" {
					hints = append(hints, h)
				}
			}
		}
	}

	res, err := plugindev.ScaffoldPlugin(name, protocol, protocolVersion, hints)
	if err != nil {
		return errorResult(err), nil
	}

	slog.Info("scaffold_plugin completed",
		"name", name, "template", res.Template,
		"sdk_version", res.SDKVersion, "framing_available", res.FramingAvailable,
		"files", res.Files)
	out := map[string]any{
		"name":              name,
		"template":          res.Template,
		"files":             res.Files,
		"contents":          res.Contents,
		"sdk_version":       res.SDKVersion,
		"framing_available": res.FramingAvailable,
		"notes": []string{
			"Plugin source is created in YOUR workspace: GameTrace does not store plugin source code and never writes these files.",
			"把 contents 里的每个 key 作为相对路径写入你自己的插件目录，然后在本机 go build 出插件二进制。",
			"插件由你自己启动（本机进程），启动后用 connect_plugin 让平台确认它已注册上来。",
		},
	}
	return successResult(out), nil
}