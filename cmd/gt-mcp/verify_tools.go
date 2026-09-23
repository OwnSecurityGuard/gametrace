package main

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	pb "gametrace/pkg/internalipc/proto"
)

// handleVerifyPlugin 用指定插件对离线会话的 raw_packets 解码并做契约+质量校验，
// 产出 violations（引 SDK checker，带 rule_id）+ quality（gametrace 统计）+ verdict。
// 纯转发到 Runtime Plane（gt-pipeline）；MCP 自身零归因逻辑、零 exec、零 os.WriteFile。
func (m *mcpCapture) handleVerifyPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessionID := req.GetString("session_id", "")
	pluginName := req.GetString("plugin", "")
	protocol := req.GetString("protocol", "")
	src := req.GetString("src", "")
	dst := req.GetString("dst", "")
	limit := req.GetInt("limit", 0)

	if sessionID == "" {
		return errorResult(fmt.Errorf("session_id is required")), nil
	}
	if pluginName == "" {
		return errorResult(fmt.Errorf("plugin is required")), nil
	}
	if m.pipelineClient == nil {
		return errorResult(fmt.Errorf("pipeline client not available")), nil
	}

	resp, err := m.pipelineClient.Verify(ctx, &pb.VerifyRequest{
		SessionId: sessionID,
		Plugin:    pluginName,
		Protocol:  protocol,
		Src:       src,
		Dst:       dst,
		Limit:     int64(limit),
	})
	if err != nil {
		return errorResult(fmt.Errorf("verify: %w", err)), nil
	}

	out := map[string]any{
		"status":        "completed",
		"session_id":    sessionID,
		"plugin":        pluginName,
		"verdict":       resp.GetVerdict(),
		"verify_run_id": resp.GetVerifyRunId(),
		"violations":    resp.GetViolations(),
		"quality":       resp.GetQuality(),
	}
	// 会话适用性（P1-1）：not_applicable 表示「会话没带插件要解的流量」，直接给
	// 出换会话的机器可执行建议，而不是让 AI 去修插件。
	if app := resp.GetApplicability(); app != nil {
		out["applicability"] = map[string]any{
			"applicable":      app.GetApplicable(),
			"reason":          app.GetReason(),
			"target_port":     app.GetTargetPort(),
			"total_packets":   app.GetTotalPackets(),
			"matched_packets": app.GetMatchedPackets(),
		}
		if !app.GetApplicable() {
			out["ok"] = false
			out["failure"] = map[string]any{
				"code":    "not_applicable",
				"message": fmt.Sprintf("session %s carries no traffic the plugin is expected to decode (reason=%s, matched=%d/%d); this is a wrong-session problem, not a plugin quality problem", sessionID, app.GetReason(), app.GetMatchedPackets(), app.GetTotalPackets()),
			}
			out["next_action"] = map[string]any{
				"tool":     "list_all_sessions",
				"why":      "pick a session carrying the plugin's protocol traffic (match target_port / protocol), then re-run verify_plugin",
				"requires": "session_with_matching_traffic",
			}
		}
	}
	return successResult(out), nil
}

// handleSampleBytesPlugin 读取会话原始包前若干字节（事实），并在 plugin_debug_access
// 留审计。纯转发到 Runtime Plane；MCP 零读取、零落库、零归因。
func (m *mcpCapture) handleSampleBytesPlugin(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessionID := req.GetString("session_id", "")
	pluginName := req.GetString("plugin", "")
	limit := req.GetInt("limit", 0)
	maxBytes := req.GetInt("max_bytes", 0)

	if sessionID == "" {
		return errorResult(fmt.Errorf("session_id is required")), nil
	}
	if m.pipelineClient == nil {
		return errorResult(fmt.Errorf("pipeline client not available")), nil
	}

	resp, err := m.pipelineClient.SampleBytes(ctx, &pb.SampleBytesRequest{
		SessionId: sessionID,
		Plugin:    pluginName,
		Limit:     int64(limit),
		MaxBytes:  int32(maxBytes),
	})
	if err != nil {
		return errorResult(fmt.Errorf("sample_bytes: %w", err)), nil
	}

	out := map[string]any{
		"status":                  "sampled",
		"session_id":              sessionID,
		"requested_packets":       resp.GetRequestedPackets(),
		"returned_packets":        resp.GetReturnedPackets(),
		"returned_bytes":          resp.GetReturnedBytes(),
		"truncated":               resp.GetTruncated(),
		"mean_entropy":            resp.GetMeanEntropy(),
		"length_histogram":        resp.GetLengthHistogram(),
		"first_byte_distribution": resp.GetFirstByteDistribution(),
		"packets":                 resp.GetPackets(),
		"audit_id":                resp.GetAuditId(),
	}
	return successResult(out), nil
}
