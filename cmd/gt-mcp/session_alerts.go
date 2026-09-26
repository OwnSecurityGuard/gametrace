package main

// session_alerts.go — 「命中提醒」查询面：把 pipeline 落库的检查规则命中记录（
// session_alerts 表）暴露为 MCP 工具，供平台端 Web 在会话内回看「是哪些数据导致了通知」。
//
// 一行 = 一次命中。trigger/context 在写入时已是 checkrule.AlertRecord 的原样 JSON，
// 这里以 json.RawMessage 直通输出，避免二次编解码丢失 data/meta 结构。
//
// 注意 context 只含「触发前」的上下文（滑动窗口在 checkEngine 主循环侧裁剪，按方向分组）；
// 「触发后」的最近若干条由本工具按触发时刻现查（after 参数），避免命中落库要等后续事件、
// 会话末尾命中永远补不齐的问题。after 是时间正序的扁平列表，各自带 direction。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/auth"
	"gametrace/pkg/checkrule"
	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// alertReader 命中告警查询能力（由 *store.SQLiteStore / *store.PGStore 实现）。
type alertReader interface {
	QueryAlerts(ctx context.Context, q store.AlertQuery) ([]store.AlertRow, int, error)
}

// asAlertReader 把 captureReader 断言为 alertReader；后端不支持时返回错误。
func asAlertReader(r captureReader) (alertReader, error) {
	ar, ok := r.(alertReader)
	if !ok {
		return nil, fmt.Errorf("store backend does not support alert queries")
	}
	return ar, nil
}

// handleListSessionAlerts 返回某会话的命中告警（最新在前，分页）。
func (m *mcpCapture) handleListSessionAlerts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessionID := req.GetString("session_id", "")
	alertID := req.GetString("alert_id", "")
	limit := req.GetInt("limit", 50)
	offset := req.GetInt("offset", 0)
	after := req.GetInt("after", 0)
	if after > checkrule.MaxContext {
		after = checkrule.MaxContext
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	// 与 list_decoded_data 一致：空 session_id 解析为调用方当前会话。
	if sessionID == "" {
		owner := auth.OwnerFrom(ctx)
		if current, err := m.sessionMgr.readCurrent(owner); err == nil && current != nil && current.SessionID != "" {
			sessionID = current.SessionID
		}
	}

	dbPath, err := m.getDBPath(ctx, sessionID)
	if err != nil {
		return errorResult(err), nil
	}
	slog.Info("list_session_alerts requested",
		"session_id", sessionID, "alert_id", alertID, "limit", limit, "offset", offset, "after", after, "db_path", dbPath)
	if dbPath == "" {
		return errorResult(fmt.Errorf("no capture database available; start a capture first")), nil
	}

	reader, err := m.openReader(ctx, sessionID)
	if err != nil {
		return errorResult(err), nil
	}
	defer reader.Close()

	ar, err := asAlertReader(reader)
	if err != nil {
		return errorResult(err), nil
	}

	rows, total, err := ar.QueryAlerts(ctx, store.AlertQuery{SessionID: sessionID, AlertID: alertID, Limit: limit, Offset: offset})
	if err != nil {
		return errorResult(fmt.Errorf("query alerts: %w", err)), nil
	}

	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		item := map[string]any{
			"alert_id":     r.AlertID,
			"rule_id":      r.RuleID,
			"rule_name":    r.RuleName,
			"title":        r.Title,
			"message":      r.Message,
			"timestamp":    formatEventTime(r.Timestamp),
			"generated_at": r.GeneratedAt,
		}
		if raw := json.RawMessage(r.TriggerJSON); len(raw) > 0 && json.Valid(raw) {
			item["trigger"] = raw
		}
		if raw := json.RawMessage(r.ContextJSON); len(raw) > 0 && json.Valid(raw) {
			item["context"] = raw
		}
		if after > 0 {
			// 只需要触发记录的 id 来剔除它自身（同纳秒会重新查出来）。
			var trig checkrule.AlertRecord
			if err := json.Unmarshal([]byte(r.TriggerJSON), &trig); err == nil {
				if recs := queryAfterRecords(ctx, reader, sessionID, r.Timestamp, trig.ID, after); len(recs) > 0 {
					item["after"] = recs
				}
			}
		}
		out = append(out, item)
	}

	slog.Info("list_session_alerts completed", "session_id", sessionID, "total", total, "returned", len(out))
	return successResult(map[string]any{
		"session_id": sessionID,
		"count":      total,
		"alerts":     out,
		"limit":      limit,
		"offset":     offset,
	}), nil
}

// queryAfterRecords 取某次命中「之后」的最近 n 条解码记录（时间正序）。
//
// 锚点是落库的触发时刻；QueryEventsInRange 含端点，所以触发记录自身（同纳秒）会
// 回到结果里，按 id 剔除。查询失败非致命：返回 nil，前端只是少一段后续上下文。
func queryAfterRecords(ctx context.Context, reader captureReader, sessionID string, anchor time.Time, triggerID string, n int) []checkrule.AlertRecord {
	evs, err := reader.QueryEventsInRange(ctx, sessionID, anchor, time.Time{}, n+1)
	if err != nil {
		slog.Debug("list_session_alerts: after-context query failed",
			"session_id", sessionID, "error", err)
		return nil
	}
	out := make([]checkrule.AlertRecord, 0, n)
	for _, ev := range evs {
		if string(ev.Identity.ID) == triggerID {
			continue
		}
		out = append(out, alertRecordFromEvent(ev))
		if len(out) >= n {
			break
		}
	}
	return out
}

// alertRecordFromEvent 把一条事件渲染成与 pipeline 侧 toAlertRecord 同形状的记录，
// 使「触发前」（命中时落库）与「触发后」（现查）在前端共用一套渲染。
func alertRecordFromEvent(ev *event.Event) checkrule.AlertRecord {
	if ev == nil {
		return checkrule.AlertRecord{}
	}
	biz, flatMeta, _ := event.SplitReservedKeys(ev.Payload.Value)
	var data any
	if raw, err := biz.ToJSON(); err == nil {
		data = checkrule.TruncateData(raw, checkrule.MaxRecordDataBytes)
	}
	meta := ev.Meta.ToAny()
	if meta == nil {
		meta = flatMeta.ToAny()
	}
	direction := ev.Context.Direction
	if v, ok := ev.MetaValue("direction"); ok {
		if s, ok := v.AsString(); ok && s != "" {
			direction = s
		}
	}
	return checkrule.AlertRecord{
		ID:        string(ev.Identity.ID),
		Timestamp: formatEventTime(ev.Identity.Timestamp),
		Type:      string(ev.Identity.Type),
		Direction: direction,
		Data:      data,
		Meta:      meta,
	}
}
