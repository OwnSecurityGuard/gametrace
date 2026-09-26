package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PGControlStore 是 PostgreSQL 后端下的控制元数据存储，满足 ControlStoreBackend 接口。
//
// 与 SQLiteControlStore 的差异：
//   - 一个 PG 库承载全部会话的 sessions / plugin_debug_access；
//   - 占位符用 $N；plugin_debug_access.id 为 IDENTITY，RETURNING 取回（pgx 不支持 LastInsertId）；
//   - started_at/stopped_at 用 TIMESTAMP（driver 直接处理 time.Time），无 AUTOINCREMENT / 无 ALTER 迁移；
//   - 共享连接池按 DSN 缓存，Close 为 no-op。
type PGControlStore struct {
	db *sql.DB
}

// Close 为 no-op：PG 连接池在进程内按 DSN 共享缓存，不应随单个控制库关闭。
func (cs *PGControlStore) Close() error { return nil }

// DB 返回底层共享连接池。
func (cs *PGControlStore) DB() *sql.DB { return cs.db }

// CreateSession 插入一条新会话元数据。
func (cs *PGControlStore) CreateSession(ctx context.Context, meta SessionMeta) error {
	var extraJSON sql.NullString
	if len(meta.Extra) > 0 {
		b, err := json.Marshal(meta.Extra)
		if err != nil {
			return fmt.Errorf("marshal extra: %w", err)
		}
		extraJSON = sql.NullString{String: string(b), Valid: true}
	}
	var stoppedAt sql.NullTime
	if meta.StoppedAt != nil {
		stoppedAt = sql.NullTime{Time: *meta.StoppedAt, Valid: true}
	}
	_, err := cs.db.ExecContext(ctx, `
INSERT INTO sessions(owner, tenant_id, project_id, session_id, started_at, stopped_at, status, port, plugin, interface, pcap_file,
                     raw_packets, events, metrics, decode_errors, duration_sec, db_path, extra, manifest_snapshot)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		meta.Owner, normalizeTenant(meta.TenantID), meta.ProjectID, meta.SessionID, meta.StartedAt, stoppedAt, meta.Status, meta.Port, meta.Plugin,
		meta.Interface, meta.PCAPFile, meta.RawPackets, meta.Events, meta.Metrics,
		meta.DecodeErrors, meta.DurationSec, meta.DBPath, extraJSON, meta.ManifestSnapshot,
	)
	return err
}

// GetSession 查询单个会话元数据（不过滤 owner，行为与引入 owner 前一致）。
func (cs *PGControlStore) GetSession(ctx context.Context, sessionID string) (*SessionMeta, error) {
	return cs.GetSessionFor(ctx, sessionID, SessionOwnerFilter{AllOwners: true})
}

// GetSessionFor 按 owner 过滤地查询单个会话；不可见时按未找到处理。
func (cs *PGControlStore) GetSessionFor(ctx context.Context, sessionID string, f SessionOwnerFilter) (*SessionMeta, error) {
	row := cs.db.QueryRowContext(ctx, `SELECT `+sessionSelectCols+` FROM sessions WHERE session_id=$1`, sessionID)
	meta, err := scanSession(row)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", sessionID, err)
	}
	if !f.Matches(*meta) {
		// 不可见按未找到处理，避免向非归属者泄露会话存在性。
		return nil, fmt.Errorf("get session %s: %w", sessionID, sql.ErrNoRows)
	}
	return meta, nil
}

// ListSessions 列出所有会话元数据（不过滤 owner），按 started_at 降序。
func (cs *PGControlStore) ListSessions(ctx context.Context) ([]SessionMeta, error) {
	return cs.ListSessionsFor(ctx, SessionOwnerFilter{AllOwners: true})
}

// ListSessionsFor 按可见性过滤地列出会话元数据，按 started_at 降序。
// 可见性 = owner 匹配 OR 归属 ProjectIDs 中的项目（与 SQLite 版一致）。
func (cs *PGControlStore) ListSessionsFor(ctx context.Context, f SessionOwnerFilter) ([]SessionMeta, error) {
	query := `SELECT ` + sessionSelectCols + ` FROM sessions`
	args := []any{}
	if !f.AllOwners {
		query += ` WHERE (owner=$1`
		args = append(args, f.Owner)
		for _, id := range f.ProjectIDs {
			query += fmt.Sprintf(` OR project_id=$%d`, len(args)+1)
			args = append(args, id)
		}
		query += `)`
	}
	query += ` ORDER BY started_at DESC`
	rows, err := cs.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionMeta
	for rows.Next() {
		meta, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *meta)
	}
	return result, rows.Err()
}

// ListSessionsForProject 列出某项目下的全部会话元数据，按 started_at 降序。
// 不做 owner 过滤：项目是协作边界，调用方必须先做 ActionProjectRead 鉴权。
func (cs *PGControlStore) ListSessionsForProject(ctx context.Context, projectID string, _ SessionOwnerFilter) ([]SessionMeta, error) {
	query := `SELECT ` + sessionSelectCols + ` FROM sessions WHERE project_id=$1`
	args := []any{projectID}
	query += ` ORDER BY started_at DESC`
	rows, err := cs.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionMeta
	for rows.Next() {
		meta, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *meta)
	}
	return result, rows.Err()
}

// FinishSession 回写会话终态与统计列，不触碰归属列（与 SQLite 版一致）。
func (cs *PGControlStore) FinishSession(ctx context.Context, sessionID string, fin SessionFinish) error {
	res, err := cs.db.ExecContext(ctx, `
UPDATE sessions SET stopped_at=$1, status=$2, port=$3, plugin=$4, pcap_file=$5,
                    raw_packets=$6, events=$7, metrics=$8, decode_errors=$9, duration_sec=$10, db_path=$11
WHERE session_id=$12`,
		fin.StoppedAt, fin.Status, fin.Port, fin.Plugin, fin.PCAPFile,
		fin.RawPackets, fin.Events, fin.Metrics, fin.DecodeErrors, fin.DurationSec, fin.DBPath,
		sessionID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %s not found", sessionID)
	}
	return nil
}

// DeleteSession 删除会话元数据。
func (cs *PGControlStore) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := cs.db.ExecContext(ctx, "DELETE FROM sessions WHERE session_id=$1", sessionID)
	return err
}

// ReconcileRunningSessions 将上一进程残留的 running 会话标记为 stopped。
func (cs *PGControlStore) ReconcileRunningSessions(ctx context.Context, stoppedAt time.Time) (int64, error) {
	res, err := cs.db.ExecContext(ctx, `
UPDATE sessions SET status='stopped', stopped_at=$1 WHERE status='running'`, stoppedAt)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MoveSessionToProject 是 move_session_to_project 的原子落点（带租户 CAS，与 SQLite 版一致）。
func (cs *PGControlStore) MoveSessionToProject(ctx context.Context, sessionID, projectID, expectTenant string) error {
	res, err := cs.db.ExecContext(ctx,
		`UPDATE sessions SET project_id=$1 WHERE session_id=$2 AND tenant_id=$3`,
		projectID, sessionID, expectTenant)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %s not found or tenant changed", sessionID)
	}
	return nil
}

// RecordDebugAccess 追加一条 plugin_debug_access 审计行（PG 用 RETURNING id 取回主键）。
func (cs *PGControlStore) RecordDebugAccess(ctx context.Context, d DebugAccess) (int64, error) {
	if d.At.IsZero() {
		d.At = time.Now()
	}
	var id int64
	err := cs.db.QueryRowContext(ctx, `
INSERT INTO plugin_debug_access
    (at, actor, tool, plugin, session_id, requested_packets, returned_packets, returned_bytes, truncated)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		d.At, d.Actor, d.Tool, d.Plugin, d.SessionID,
		d.RequestedPackets, d.ReturnedPackets, d.ReturnedBytes, boolToInt(d.Truncated),
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// DebugAccesses 返回某会话的审计行（最新在前）。
func (cs *PGControlStore) DebugAccesses(ctx context.Context, sessionID string) ([]DebugAccess, error) {
	rows, err := cs.db.QueryContext(ctx, `
SELECT id, at, actor, tool, plugin, session_id, requested_packets, returned_packets, returned_bytes, truncated
FROM plugin_debug_access
WHERE session_id=$1
ORDER BY at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DebugAccess
	for rows.Next() {
		var d DebugAccess
		var truncated int
		if err := rows.Scan(&d.ID, &d.At, &d.Actor, &d.Tool, &d.Plugin,
			&d.SessionID, &d.RequestedPackets, &d.ReturnedPackets, &d.ReturnedBytes, &truncated); err != nil {
			return nil, err
		}
		d.Truncated = truncated != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpsertPluginValidation 覆盖写入一个插件实例的验证证明（PG 实现）。
func (cs *PGControlStore) UpsertPluginValidation(ctx context.Context, v PluginValidation) error {
	if v.Owner == "" || v.Name == "" {
		return fmt.Errorf("plugin validation: owner and name are required")
	}
	if v.At.IsZero() {
		v.At = time.Now()
	}
	_, err := cs.db.ExecContext(ctx, `
INSERT INTO plugin_validations (owner, name, verify_run_id, session_id, verdict, at)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT(owner, name) DO UPDATE SET
    verify_run_id=excluded.verify_run_id,
    session_id=excluded.session_id,
    verdict=excluded.verdict,
    at=excluded.at`,
		v.Owner, v.Name, v.VerifyRunID, v.SessionID, v.Verdict, v.At,
	)
	return err
}

// GetPluginValidation 读取某 owner 下某插件的验证证明；不存在返回 (nil, nil)。
func (cs *PGControlStore) GetPluginValidation(ctx context.Context, owner, name string) (*PluginValidation, error) {
	var v PluginValidation
	err := cs.db.QueryRowContext(ctx, `
SELECT owner, name, verify_run_id, session_id, verdict, at
FROM plugin_validations
WHERE owner=$1 AND name=$2`, owner, name,
	).Scan(&v.Owner, &v.Name, &v.VerifyRunID, &v.SessionID, &v.Verdict, &v.At)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ClearPluginValidation 删除某插件的验证证明（PG 实现）。
func (cs *PGControlStore) ClearPluginValidation(ctx context.Context, owner, name string) error {
	_, err := cs.db.ExecContext(ctx, `DELETE FROM plugin_validations WHERE owner=$1 AND name=$2`, owner, name)
	return err
}
