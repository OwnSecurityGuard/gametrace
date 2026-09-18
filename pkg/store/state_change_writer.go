package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
)

// state_changes 的写入在 SQLite / Postgres 两份实现之间只应存在「占位符方言」的差异。
// 列清单、校验、编码、非法条目跳过策略全部收敛到 stateChangeInsertColumns 与
// buildStateChangeRows —— 之前两份复制粘贴的实现改一处必漏另一处（seq 列的加入就是
// 一次这样的机会：漏改一侧会让该后端的 seq 永久为 0，而不报任何错）。

// stateChangeInsertColumns 是 INSERT 的列清单，顺序与 buildStateChangeRows 返回的值一一对应。
const stateChangeInsertColumns = `id, event_id, session_id, flow_id, timestamp, subject_type, subject_id, op, path, before_value, after_value, version, before_resolved, after_resolved, seq, metadata`

// stateChangeInsertArity 是上面的列数，两个方言各自的占位符串按它书写。
const stateChangeInsertArity = 16

// buildStateChangeRows 把一批变更校验并编码为待插入的行值（[]any，长度与列清单一致）。
//
// 非法条目逐条跳过并计入 skipped：一条脏数据不能带走整批（调用方拿不到任何行会更糟）。
func buildStateChangeRows(sessionID string, changes []EnrichedStateChange) (rows [][]any, skipped int) {
	rows = make([][]any, 0, len(changes))
	for _, esc := range changes {
		if err := esc.Validate(); err != nil {
			slog.Warn("skip invalid enriched state change", "event_id", esc.EventID, "error", err)
			skipped++
			continue
		}
		beforeJSON, _ := json.Marshal(esc.Before.ToAny())
		afterJSON, _ := json.Marshal(esc.After.ToAny())
		metaJSON, _ := json.Marshal(esc.Metadata.ToAny())

		var flowID sql.NullString
		if esc.FlowID != "" {
			flowID = sql.NullString{String: esc.FlowID, Valid: true}
		}
		rows = append(rows, []any{
			uuid.NewString(),
			string(esc.EventID),
			sessionID,
			flowID,
			esc.Timestamp.UnixNano(),
			esc.SubjectType,
			esc.SubjectID,
			esc.Op,
			esc.Path,
			string(beforeJSON),
			string(afterJSON),
			esc.Version,
			esc.BeforeResolved,
			esc.AfterResolved,
			esc.Seq,
			string(metaJSON),
		})
	}
	return rows, skipped
}

// WriteEnrichedStateChanges 写入经过语义基线解析的 StateChange。
func (s *SQLiteStore) WriteEnrichedStateChanges(ctx context.Context, sessionID string, changes []EnrichedStateChange) error {
	if len(changes) == 0 {
		return nil
	}
	rows, skipped := buildStateChangeRows(sessionID, changes)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO state_changes(`+stateChangeInsertColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row...); err != nil {
			return err
		}
	}
	if len(rows) > 0 || skipped > 0 {
		slog.Debug("wrote enriched state changes", "count", len(rows), "skipped", skipped)
	}
	return tx.Commit()
}
