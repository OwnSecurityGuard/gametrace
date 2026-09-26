package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/state"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ===== query_state_changes =====

// handleQueryStateChanges 按锚点、时间窗口与维度聚合查询状态变更。
// 一次查询同时给出扁平变更流与 操作 / 实体 / 事件 / 时间段 四种分组，
// 三种视图共用同一份 Changes，因此切换视图不需要重新查询。
func (m *mcpCapture) handleQueryStateChanges(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessionID, err := m.resolveSessionIDForRead(ctx, req.GetString("session_id", ""))
	if err != nil {
		return errorResult(err), nil
	}

	q := state.Query{
		SessionID: sessionID,
		Anchor: state.Anchor{
			Kind: state.AnchorKind(strings.TrimSpace(req.GetString("anchor_type", ""))),
			ID:   strings.TrimSpace(req.GetString("anchor_id", "")),
		},
		BeforeMS: int64(req.GetInt("window_before_ms", 0)),
		AfterMS:  int64(req.GetInt("window_after_ms", 0)),
		GroupBy:  state.GroupBy(strings.TrimSpace(req.GetString("group_by", string(state.GroupByOperation)))),
		SortBy:   state.SortBy(strings.TrimSpace(req.GetString("sort_by", string(state.SortByFirstChange)))),
		Desc:     req.GetBool("desc", false),
		BucketMS: int64(req.GetInt("bucket_ms", 0)),
		Limit:    req.GetInt("limit", 0),
		Filter: state.Filter{
			SubjectTypes: stringListArg(req, "subject_types"),
			Paths:        stringListArg(req, "paths"),
			Ops:          stringListArg(req, "ops"),
		},
	}
	if v := strings.TrimSpace(req.GetString("from", "")); v != "" {
		t, err := parseTimeArg(v)
		if err != nil {
			return errorResult(fmt.Errorf("from: %w", err)), nil
		}
		q.From = t
	}
	if v := strings.TrimSpace(req.GetString("to", "")); v != "" {
		t, err := parseTimeArg(v)
		if err != nil {
			return errorResult(fmt.Errorf("to: %w", err)), nil
		}
		q.To = t
	}
	// 单值快捷参数：等价于数组里只有一个元素。
	if v := strings.TrimSpace(req.GetString("subject_type", "")); v != "" {
		q.Filter.SubjectTypes = append(q.Filter.SubjectTypes, v)
	}
	if v := strings.TrimSpace(req.GetString("path", "")); v != "" {
		q.Filter.Paths = append(q.Filter.Paths, v)
	}
	if v := strings.TrimSpace(req.GetString("op", "")); v != "" {
		q.Filter.Ops = append(q.Filter.Ops, v)
	}

	slog.Info("query_state_changes requested",
		"session_id", sessionID, "anchor_type", q.Anchor.Kind, "anchor_id", q.Anchor.ID,
		"before_ms", q.BeforeMS, "after_ms", q.AfterMS, "group_by", q.GroupBy, "sort_by", q.SortBy)

	reader, err := m.openReader(ctx, sessionID)
	if err != nil {
		return errorResult(err), nil
	}
	defer reader.Close()

	rs, err := state.Run(ctx, reader, q)
	if err != nil {
		return errorResult(fmt.Errorf("query state changes: %w", err)), nil
	}
	slog.Info("query_state_changes completed", "session_id", sessionID,
		"changes", rs.Summary.ChangeCount, "entities", rs.Summary.EntityCount, "operations", rs.Summary.OperationCount)
	return successResult(rs), nil
}

// ===== get_state_change_detail =====

// handleGetStateChangeDetail 返回一条变更 / 一条协议消息 / 一个实体的完整上下文：
// 协议链（操作 → 请求/响应/推送 → 实体 → 字段变化）与该实体/字段的完整变化历史。
func (m *mcpCapture) handleGetStateChangeDetail(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessionID, err := m.resolveSessionIDForRead(ctx, req.GetString("session_id", ""))
	if err != nil {
		return errorResult(err), nil
	}
	dq := state.DetailQuery{
		SessionID: sessionID,
		ChangeID:  strings.TrimSpace(req.GetString("change_id", "")),
		EventID:   strings.TrimSpace(req.GetString("event_id", "")),
		Entity:    strings.TrimSpace(req.GetString("entity", "")),
		Path:      strings.TrimSpace(req.GetString("path", "")),
		BeforeMS:  int64(req.GetInt("window_before_ms", 0)),
		AfterMS:   int64(req.GetInt("window_after_ms", 0)),
		Limit:     req.GetInt("limit", 0),
	}

	slog.Info("get_state_change_detail requested", "session_id", sessionID,
		"change_id", dq.ChangeID, "event_id", dq.EventID, "entity", dq.Entity, "path", dq.Path)

	reader, err := m.openReader(ctx, sessionID)
	if err != nil {
		return errorResult(err), nil
	}
	defer reader.Close()

	detail, err := state.LoadDetail(ctx, reader, dq)
	if err != nil {
		return errorResult(fmt.Errorf("get state change detail: %w", err)), nil
	}
	return successResult(detail), nil
}

// ===== 注册 =====

func registerStateTools(s *server.MCPServer, capture *mcpCapture) {
	s.AddTool(mcp.NewTool("query_state_changes",
		mcp.WithDescription("Query entity state changes by anchor, time window and aggregation dimension. Returns a flat change stream plus operation/entity/event/time-bucket groupings in ONE call: the three UI views (by operation / by entity / by time) share the same dataset. Each change carries its source message (request/response/push), a sequence number and an offset relative to the anchor (T+220ms)."),
		mcp.WithString("session_id", mcp.Description("Optional session ID to query; defaults to current session")),
		mcp.WithString("anchor_type", mcp.Description("Anchor kind: operation (a request with its response/pushes) | entity (subject_type:subject_id) | event (a single message). Empty = whole session")),
		mcp.WithString("anchor_id", mcp.Description("Anchor id: event id or correlation_id for operation; 'subject_type:subject_id' for entity; event id for event")),
		mcp.WithNumber("window_before_ms", mcp.Description("Observation window before the anchor, in ms. 0 = no lower bound")),
		mcp.WithNumber("window_after_ms", mcp.Description("Observation window after the anchor, in ms (e.g. 2000 = 'which entities changed within 2s of this operation'). 0 = no upper bound")),
		mcp.WithString("group_by", mcp.Description("Aggregation dimension: operation (default) | entity | event | time")),
		mcp.WithString("sort_by", mcp.Description("Group ordering: first_change (default, chronological) | time (latest change first) | change_count (most changed first)")),
		mcp.WithBoolean("desc", mcp.Description("Reverse the group ordering")),
		mcp.WithNumber("bucket_ms", mcp.Description("Time view bucket size in ms; 0 = auto (about 20 buckets)")),
		mcp.WithString("from", mcp.Description("Optional explicit window start (RFC3339Nano), overrides window_before_ms")),
		mcp.WithString("to", mcp.Description("Optional explicit window end (RFC3339Nano), overrides window_after_ms")),
		mcp.WithArray("subject_types", mcp.Description("Filter by entity type(s), e.g. [\"Building\"]"), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithArray("paths", mcp.Description("Filter by field path(s); matches the path or its sub-paths, e.g. [\"level\", \"cost\"]"), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithArray("ops", mcp.Description("Filter by change type(s): set | delete | merge"), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithString("subject_type", mcp.Description("Convenience single-value alias of subject_types")),
		mcp.WithString("path", mcp.Description("Convenience single-value alias of paths")),
		mcp.WithString("op", mcp.Description("Convenience single-value alias of ops")),
		mcp.WithNumber("limit", mcp.Description("Max changes to load (default 1000, hard cap 5000)")),
	), capture.handleQueryStateChanges)

	s.AddTool(mcp.NewTool("get_state_change_detail",
		mcp.WithDescription("Get the full protocol chain and field-level history for one state change, one protocol message, or one entity. Returns: the change itself (before/after), the chain (operation -> request/response/push -> entity -> field changes with T+offsets), and the complete change history of the entity grouped by field."),
		mcp.WithString("session_id", mcp.Description("Optional session ID to query; defaults to current session")),
		mcp.WithString("change_id", mcp.Description("State change id (from query_state_changes). Strongest locator: implies entity + field")),
		mcp.WithString("event_id", mcp.Description("Locate by the protocol message that produced the changes")),
		mcp.WithString("entity", mcp.Description("Locate by entity, formatted 'subject_type:subject_id'")),
		mcp.WithString("path", mcp.Description("Optional field path; returns that field's history only")),
		mcp.WithNumber("window_before_ms", mcp.Description("Protocol chain expansion before the anchor message (default 1000)")),
		mcp.WithNumber("window_after_ms", mcp.Description("Protocol chain expansion after the anchor message (default 2000)")),
		mcp.WithNumber("limit", mcp.Description("Max history rows (default 5000)")),
	), capture.handleGetStateChangeDetail)
}

// ===== 参数工具 =====

// resolveSessionIDForRead 解析并鉴权要读取的会话：空值时回退到当前会话。
func (m *mcpCapture) resolveSessionIDForRead(ctx context.Context, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sess, err := m.sessionMgr.readCurrent(auth.OwnerFrom(ctx))
		if err != nil {
			return "", fmt.Errorf("read current session: %w", err)
		}
		if sess == nil {
			return "", fmt.Errorf("no current session; pass session_id explicitly")
		}
		sessionID = sess.SessionID
	}
	// 显式传入时按 owner 鉴权（admin 全通过）；当前会话路径已隐含归属。
	if err := m.authorizeSession(ctx, sessionID); err != nil {
		return "", err
	}
	return sessionID, nil
}

// stringListArg 读取字符串数组参数，兼容 JSON 数组与逗号分隔字符串。
func stringListArg(req mcp.CallToolRequest, name string) []string {
	args := req.GetArguments()
	if args == nil {
		return nil
	}
	raw, ok := args[name]
	if !ok || raw == nil {
		return nil
	}
	var out []string
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		out = append(out, v...)
	case string:
		for _, part := range strings.Split(v, ",") {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// parseTimeArg 解析 RFC3339(Nano) 时间参数。
func parseTimeArg(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}
