package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 检查规则命中告警的平台端落库。
//
// 一次命中 = 一行：规则在某会话触发时，pipeline 把 checkrule.AlertBundle（触发记录 +
// 各方向触发前最近 N 条快照）拆成标量列 + 两段 JSON 写这里，使 Web 能在会话内回看
// 「是哪些数据导致了通知」。SQLite 每会话一个文件（session_id 列仍写，便于与 PG 对齐），
// PG 全共享库靠 session_id 隔离。两份实现只允许占位符差异。

// alertInsertColumns 是 INSERT 列清单，顺序与 buildAlertArgs 一一对应。
const alertInsertColumns = `session_id, alert_id, rule_id, rule_name, title, message, timestamp, generated_at, trigger_json, context_json`

// AlertRow 是一条命中告警的落库形态。TriggerJSON / ContextJSON 是 checkrule 侧
// AlertRecord 与 map[direction][]AlertRecord 的原样序列化，store 不解析、只存取。
type AlertRow struct {
	SessionID   string
	AlertID     string
	RuleID      string
	RuleName    string
	Title       string
	Message     string
	Timestamp   time.Time // 排序 + 前端按时间补「触发后 N 条」上下文的锚点
	GeneratedAt string    // bundle 原始 RFC3339，仅展示
	TriggerJSON string
	ContextJSON string
}

// buildAlertArgs 把一行拼成 INSERT 参数（含 sessionID，忽略 row 内可能不同的字段）。
func buildAlertArgs(sessionID string, row AlertRow) []any {
	return []any{
		sessionID,
		row.AlertID,
		row.RuleID,
		row.RuleName,
		row.Title,
		row.Message,
		unixNanoOrZero(row.Timestamp),
		row.GeneratedAt,
		row.TriggerJSON,
		row.ContextJSON,
	}
}

// AlertQuery 是命中告警的分页查询条件。AlertID 非空时只取那一条命中
// （前端展开详情时按 id 单独问，不必为一条记录给整页都补后续上下文）。
type AlertQuery struct {
	SessionID string
	AlertID   string
	Limit     int
	Offset    int
}

// AppendAlert 写入一条命中告警，按 (session_id, alert_id) 幂等（重复写覆盖）。
func (s *SQLiteStore) AppendAlert(ctx context.Context, sessionID string, row AlertRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO session_alerts(`+alertInsertColumns+`) VALUES (`+placeholders(10)+`)`,
		buildAlertArgs(sessionID, row)...)
	if err != nil {
		return fmt.Errorf("append alert: %w", err)
	}
	return nil
}

// AppendAlert 见 SQLiteStore 的同名方法。
func (s *PGStore) AppendAlert(ctx context.Context, sessionID string, row AlertRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_alerts(`+alertInsertColumns+`) VALUES (`+pgPlaceholders(10, 1)+`)
		 ON CONFLICT (session_id, alert_id) DO UPDATE SET
		   rule_id=EXCLUDED.rule_id, rule_name=EXCLUDED.rule_name, title=EXCLUDED.title,
		   message=EXCLUDED.message, timestamp=EXCLUDED.timestamp, generated_at=EXCLUDED.generated_at,
		   trigger_json=EXCLUDED.trigger_json, context_json=EXCLUDED.context_json`,
		buildAlertArgs(sessionID, row)...)
	if err != nil {
		return fmt.Errorf("append alert: %w", err)
	}
	return nil
}

// QueryAlerts 返回某会话的命中告警，按时间倒序（最新在前）分页，并回 SQL 条件命中总数。
func (s *SQLiteStore) QueryAlerts(ctx context.Context, q AlertQuery) ([]AlertRow, int, error) {
	return queryAlerts(ctx, s.db, q, func(i int) string { return "?" })
}

// QueryAlerts 见 SQLiteStore 的同名方法。
func (s *PGStore) QueryAlerts(ctx context.Context, q AlertQuery) ([]AlertRow, int, error) {
	return queryAlerts(ctx, s.db, q, func(i int) string { return fmt.Sprintf("$%d", i) })
}

// queryAlerts 是两端共用实现：差异只在占位符写法（ph 负责编号）。
func queryAlerts(ctx context.Context, db *sql.DB, q AlertQuery, ph func(int) string) ([]AlertRow, int, error) {
	where := `WHERE session_id = ` + ph(1)
	args := []any{q.SessionID}
	if q.AlertID != "" {
		where += ` AND alert_id = ` + ph(len(args)+1)
		args = append(args, q.AlertID)
	}
	limitIdx, offsetIdx := len(args)+1, len(args)+2

	var total int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_alerts `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count alerts: %w", err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, alert_id, rule_id, rule_name, title, message, timestamp, generated_at, trigger_json, context_json
		FROM session_alerts
		`+where+`
		ORDER BY timestamp DESC, alert_id DESC
		LIMIT `+ph(limitIdx)+` OFFSET `+ph(offsetIdx),
		append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("query alerts: %w", err)
	}
	defer rows.Close()

	out := make([]AlertRow, 0, q.Limit)
	for rows.Next() {
		var (
			r        AlertRow
			ts       int64
			ruleName sql.NullString
			title    sql.NullString
			message  sql.NullString
			genAt    sql.NullString
			trig     sql.NullString
			ctxj     sql.NullString
		)
		if err := rows.Scan(&r.SessionID, &r.AlertID, &r.RuleID, &ruleName, &title, &message,
			&ts, &genAt, &trig, &ctxj); err != nil {
			return nil, 0, fmt.Errorf("scan alert: %w", err)
		}
		r.RuleName, r.Title, r.Message = ruleName.String, title.String, message.String
		r.GeneratedAt, r.TriggerJSON, r.ContextJSON = genAt.String, trig.String, ctxj.String
		r.Timestamp = timeFromUnixNano(ts)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate alerts: %w", err)
	}
	return out, total, nil
}
