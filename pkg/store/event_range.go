package store

import (
	"context"
	"fmt"
	"time"

	"gametrace/pkg/event"
)

// QueryEventsInRange 按时间窗口顺序返回事件（SQLite 实现）。
// from/to 为零值时不限制该侧；limit<=0 表示不限制。
func (s *SQLiteStore) QueryEventsInRange(ctx context.Context, sessionID string, from, to time.Time, limit int) ([]*event.Event, error) {
	query := `
		SELECT id, session_id, type, source, timestamp,
		       causation_id, correlation_id, origin_id, parent_id, context, payload` + s.eventSelectSuffix() + `
		FROM events
		WHERE session_id = ?
	`
	args := []any{sessionID}
	if !from.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, from.UnixNano())
	}
	if !to.IsZero() {
		query += " AND timestamp <= ?"
		args = append(args, to.UnixNano())
	}
	query += " ORDER BY timestamp ASC"
	query, args = applyLimitOffset(query, args, limit, 0)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events in range: %w", err)
	}
	defer rows.Close()

	var events []*event.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// QueryEventsInRange 按时间窗口顺序返回事件（PG 实现）。
func (s *PGStore) QueryEventsInRange(ctx context.Context, sessionID string, from, to time.Time, limit int) ([]*event.Event, error) {
	var a pgArgs
	q := `SELECT ` + eventColsPG + s.eventSelectSuffix() + `
	FROM events WHERE session_id = ` + a.next(sessionID)
	if !from.IsZero() {
		q += ` AND timestamp >= ` + a.next(from.UnixNano())
	}
	if !to.IsZero() {
		q += ` AND timestamp <= ` + a.next(to.UnixNano())
	}
	q += ` ORDER BY timestamp ASC` + a.limitOffset(limit, 0)

	rows, err := s.db.QueryContext(ctx, q, a.slice()...)
	if err != nil {
		return nil, fmt.Errorf("query events in range: %w", err)
	}
	defer rows.Close()

	var events []*event.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
