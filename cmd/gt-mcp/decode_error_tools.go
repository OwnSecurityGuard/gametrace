package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ===== query_decode_errors =====
//
// 「解码失败 12,345 次」本身不构成答案：用户要知道的是解不开的是什么、为什么。
// 失败在采集时已按**归一化错误模板**聚合（见 pkg/decode/errorcol.go），所以
// 错误再多，这里也只返回有限几组，每组带一条原文样本与一个代表包。

// decodeErrorGroupView 是一类解码失败的对外形态。
type decodeErrorGroupView struct {
	// Kind: plugin（插件主动报"这条我解不了"）| transport（流断开/超时/包无法还原）
	Kind string `json:"kind"`
	// Template 是归一化模板，同类错误共用，例如 "unexpected EOF at offset <n>"。
	Template string `json:"template"`
	// Count 是该类失败的次数。
	Count int64 `json:"count"`
	// Sample 是首条原始错误文本，用于还原真实原因（模板已抹掉具体数字/地址）。
	Sample string `json:"sample,omitempty"`
	// SampleRawPacketID 是该组的代表包，可据此用 query_raw_packets 下钻。
	SampleRawPacketID string `json:"sample_raw_packet_id,omitempty"`
	SampleSrc         string `json:"sample_src,omitempty"`
	SampleDst         string `json:"sample_dst,omitempty"`
	FirstSeen         string `json:"first_seen,omitempty"`
	LastSeen          string `json:"last_seen,omitempty"`
}

// decodeErrorsView 是 query_decode_errors 的响应。
type decodeErrorsView struct {
	SessionID string `json:"session_id"`
	// TotalFailures 是失败总次数（各组之和），与 get_session_status 的 decode_errors 同口径。
	TotalFailures int64 `json:"total_failures"`
	// Kinds 是错误种类数。远小于 TotalFailures 时说明是同一类错误在反复发生。
	Kinds  int                    `json:"kinds"`
	Groups []decodeErrorGroupView `json:"groups"`
	// Note 说明数据来源与可能的空结果原因，避免把"没有记录"误读成"没有失败"。
	Note string `json:"note,omitempty"`
}

func (m *mcpCapture) handleQueryDecodeErrors(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessionID, err := m.resolveSessionIDForRead(ctx, req.GetString("session_id", ""))
	if err != nil {
		return errorResult(err), nil
	}
	limit := req.GetInt("limit", 0)

	slog.Info("query_decode_errors requested", "session_id", sessionID, "limit", limit)

	reader, err := m.openReader(ctx, sessionID)
	if err != nil {
		return errorResult(err), nil
	}
	defer reader.Close()

	rows, err := reader.QueryDecodeErrorGroups(ctx, sessionID)
	if err != nil {
		return errorResult(fmt.Errorf("query decode errors: %w", err)), nil
	}

	view := decodeErrorsView{SessionID: sessionID, Kinds: len(rows)}
	// 总数与种类数都描述全量，先算；limit 只裁剪展示用的 groups，
	// 否则 limit=1 时 total_failures 会缩水成单组次数，与会话状态口径打架。
	for _, r := range rows {
		view.TotalFailures += r.Count
	}

	display := rows
	if limit > 0 && len(display) > limit {
		display = display[:limit]
	}
	view.Groups = make([]decodeErrorGroupView, 0, len(display))
	for _, r := range display {
		view.Groups = append(view.Groups, decodeErrorGroupView{
			Kind:              r.Kind,
			Template:          r.Template,
			Count:             r.Count,
			Sample:            r.Sample,
			SampleRawPacketID: r.SampleRawID,
			SampleSrc:         r.SampleSrc,
			SampleDst:         r.SampleDst,
			FirstSeen:         fmtTime(r.FirstSeen),
			LastSeen:          fmtTime(r.LastSeen),
		})
	}
	if len(rows) == 0 {
		view.Note = "该会话没有记录到解码失败。若状态里 decode_errors > 0，说明失败由未记录原因的旧版本抓取，" +
			"可对原始包重新解码以补上原因。"
	} else {
		view.Note = "失败按归一化错误模板聚合：同一类错误（只差数字/地址/长度）只占一组，" +
			"每组保留首条原文样本；sample_raw_packet_id 可用于下钻原始包。"
	}

	slog.Info("query_decode_errors completed", "session_id", sessionID,
		"kinds", view.Kinds, "total", view.TotalFailures)
	return successResult(view), nil
}

// fmtTime 输出 RFC3339Nano；零值输出空串（不伪造 1970 年）。
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// ===== 注册 =====

func registerDecodeErrorTools(s *server.MCPServer, capture *mcpCapture) {
	s.AddTool(mcp.NewTool("query_decode_errors",
		mcp.WithDescription("Explain WHY decoding failed, not just how many times. Returns decode failures grouped by normalized error template (so thousands of failures collapse into a handful of distinct causes), each with a count, a first-seen raw error sample and a representative raw packet id for drill-down. Use it right after seeing a non-zero decode_errors in get_session_status or list_all_sessions. Failures are persisted when a capture session stops, so stopped sessions are queryable."),
		mcp.WithString("session_id", mcp.Description("Optional session ID to query; defaults to current session")),
		mcp.WithNumber("limit", mcp.Description("Max error groups to return (0 = all). Groups are ordered by failure count desc")),
	), capture.handleQueryDecodeErrors)
}
