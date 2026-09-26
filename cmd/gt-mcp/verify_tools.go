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
		"checks":        checksView(resp.GetChecks()),
	}
	// 分层结论：applicability（会话是否适用）与 decode/semantic（插件两条校验轴）
	// 分开呈现，用户才能区分「这次没抓到该协议的包」和「插件真有问题」。
	// quality 仅在会话适用时给出；不适用时为 null —— 对非本协议流量算出的统计
	// 会被误读成插件质量差。
	if app := resp.GetApplicability(); app != nil {
		out["session_profile"] = map[string]any{
			"packets":          app.GetTotalPackets(),
			"target_port":      app.GetTargetPort(),
			"target_port_hits": app.GetMatchedPackets(),
		}
		out["applicability"] = map[string]any{
			"result":          app.GetResult(),
			"reason":          app.GetReason(),
			"target_port":     app.GetTargetPort(),
			"total_packets":   app.GetTotalPackets(),
			"matched_packets": app.GetMatchedPackets(),
		}
		if !app.GetApplicable() {
			out["ok"] = false
			out["status"] = "not_applicable"
			out["quality"] = nil
			out["failure"] = map[string]any{
				"code":    "not_applicable",
				"message": fmt.Sprintf("session %s carries no traffic the plugin is expected to decode (reason=%s, matched=%d/%d); this is a wrong-session problem, not a plugin quality problem", sessionID, app.GetReason(), app.GetMatchedPackets(), app.GetTotalPackets()),
			}
			out["next_action"] = map[string]any{
				"tool":     "list_all_sessions",
				"why":      "pick a session carrying the plugin's protocol traffic (match target_port / protocol), then re-run verify_plugin",
				"requires": "session_with_matching_traffic",
			}
			return successResult(out), nil
		}
	}
	out["quality"] = qualityView(resp.GetQuality())
	return successResult(out), nil
}

// qualityView 把平面结构的 gRPC 统计渲染成 input/decode 分组的 JSON：
//
//	{"input":{"raw":1000,"candidate":200},
//	 "decode":{"success":180,"unknown":20,"unknown_ratio":0.1,"errors":0},
//	 "correlated_inputs":12,"entropy_estimate":5.2}
//
// 分组不是装饰：input 是语料规模，decode 是插件在它该解的流量上的表现，两者
// 分开才能让人一眼看出分母是谁。nil（会话不适用）时返回 nil。
func qualityView(q *pb.VerifyQuality) map[string]any {
	if q == nil {
		return nil
	}
	return map[string]any{
		"input": map[string]any{
			"raw":       q.GetInputRaw(),
			"candidate": q.GetInputCandidate(),
		},
		"decode": map[string]any{
			"success":       q.GetDecodeSuccess(),
			"unknown":       q.GetDecodeUnknown(),
			"unknown_ratio": q.GetDecodeUnknownRatio(),
			"errors":        q.GetDecodeErrors(),
		},
		"correlated_inputs":  q.GetCorrelatedInputs(),
		"long_packet_errors": q.GetLongPacketErrors(),
		"entropy_estimate":   q.GetEntropyEstimate(),
	}
}

// checksView 渲染分层校验结论；nil（旧响应）时给两轴 not_run，避免出现空对象。
func checksView(c *pb.VerifyChecks) map[string]any {
	if c == nil {
		return map[string]any{"decode": "not_run", "semantic": "not_run"}
	}
	return map[string]any{"decode": c.GetDecode(), "semantic": c.GetSemantic()}
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
