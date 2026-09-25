package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 解码失败的原因过去只在日志里，前端只能看到一个计数。这里把「按指纹聚合后的
// 失败分组」落库，使会话**停止之后**仍能回答"为什么解不开"——只在内存里活着的
// 原因等于没有原因（用户看的多半是已经结束的会话）。
//
// 与 state_changes 一样，SQLite / PG 两份实现只允许存在占位符差异：
// 列清单、行构造、写入语义全部收敛在上面，避免改一处漏一处。

// decodeErrorInsertColumns 是 INSERT 的列清单，顺序与 buildDecodeErrorRows 一一对应。
// 用 error_count 而非 count：后者在 PG 里是聚合函数名，当列名要处处加引号。
const decodeErrorInsertColumns = `session_id, fingerprint, kind, template, sample, sample_raw_id, sample_src, sample_dst, error_count, first_seen, last_seen`

// decodeErrorInsertArity 是上面的列数。
const decodeErrorInsertArity = 11

// DecodeErrorRow 是一类解码失败的落库形态。
// 聚合语义（怎么算一类、种类上限、样本取哪条）在 pkg/decode.ErrorCollector，
// 这里只负责存取。
type DecodeErrorRow struct {
	// SessionID 由写入方给出（查询结果里会回填）。
	SessionID string
	// Fingerprint 是归一化模板的短哈希，会话内唯一。
	Fingerprint string
	// Kind 见 decode.ErrKindPlugin / ErrKindTransport / ErrKindBinding（存字符串，保持解耦）。
	Kind string
	// Template 是归一化后的错误模板，例如 "unexpected EOF at offset <n>"。
	Template string
	// Sample 是首条原始错误文本（截断），用于还原真实原因。
	Sample      string
	SampleRawID string
	SampleSrc   string
	SampleDst   string
	// Count 是该类失败累计次数。
	Count     int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// buildDecodeErrorRows 把分组编码为待插入的行值。
func buildDecodeErrorRows(sessionID string, groups []DecodeErrorRow) [][]any {
	rows := make([][]any, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, []any{
			sessionID,
			g.Fingerprint,
			g.Kind,
			g.Template,
			g.Sample,
			g.SampleRawID,
			g.SampleSrc,
			g.SampleDst,
			g.Count,
			unixNanoOrZero(g.FirstSeen),
			unixNanoOrZero(g.LastSeen),
		})
	}
	return rows
}

// unixNanoOrZero 避免零值时间变成一个巨大的负数时间戳。
func unixNanoOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// timeFromUnixNano 把落库的纳秒还原成时间；0 视为零值时间（不伪造 1970 年）。
func timeFromUnixNano(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// ReplaceDecodeErrorGroups 用当前快照整体替换该会话的解码失败分组。
//
// 用「删旧插新」而非累加 upsert：传入的 groups 是 collector 的全量真值，
// 累加会在「停止时写一次、之后又写一次」时把同一次失败算两遍。
// 传空切片即清空（离线重解码需要先清掉上一轮的结果）。
func (s *SQLiteStore) ReplaceDecodeErrorGroups(ctx context.Context, sessionID string, groups []DecodeErrorRow) error {
	return replaceDecodeErrorGroups(ctx, s.db, sessionID, groups, placeholders)
}

// ReplaceDecodeErrorGroups 见 SQLiteStore 的同名方法。
func (s *PGStore) ReplaceDecodeErrorGroups(ctx context.Context, sessionID string, groups []DecodeErrorRow) error {
	return replaceDecodeErrorGroups(ctx, s.db, sessionID, groups, func(n int) string {
		return pgPlaceholders(n, 1)
	})
}

// replaceDecodeErrorGroups 是两方言共用的实现，placeholder 只负责生成占位符串
// （SQLite 的 "?" 与 PG 的 "$1"），其余逻辑完全一致。
func replaceDecodeErrorGroups(ctx context.Context, db *sql.DB, sessionID string, groups []DecodeErrorRow, placeholder func(int) string) error {
	rows := buildDecodeErrorRows(sessionID, groups)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM decode_error_groups WHERE session_id = `+placeholder(1), sessionID); err != nil {
		return fmt.Errorf("clear decode error groups: %w", err)
	}
	if len(rows) == 0 {
		return tx.Commit()
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO decode_error_groups(`+decodeErrorInsertColumns+`)
		VALUES (`+placeholder(decodeErrorInsertArity)+`)`)
	if err != nil {
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row...); err != nil {
			return fmt.Errorf("insert decode error group: %w", err)
		}
	}
	return tx.Commit()
}

// QueryDecodeErrorGroups 返回该会话的解码失败分组，按次数降序（同次数按首次出现）。
func (s *SQLiteStore) QueryDecodeErrorGroups(ctx context.Context, sessionID string) ([]DecodeErrorRow, error) {
	return queryDecodeErrorGroups(ctx, s.db, sessionID, "?")
}

// QueryDecodeErrorGroups 见 SQLiteStore 的同名方法。
func (s *PGStore) QueryDecodeErrorGroups(ctx context.Context, sessionID string) ([]DecodeErrorRow, error) {
	return queryDecodeErrorGroups(ctx, s.db, sessionID, "$1")
}

func queryDecodeErrorGroups(ctx context.Context, db *sql.DB, sessionID, ph string) ([]DecodeErrorRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, fingerprint, kind, template, sample, sample_raw_id, sample_src, sample_dst,
		       error_count, first_seen, last_seen
		FROM decode_error_groups
		WHERE session_id = `+ph+`
		ORDER BY error_count DESC, first_seen ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query decode error groups: %w", err)
	}
	defer rows.Close()

	var out []DecodeErrorRow
	for rows.Next() {
		var (
			r           DecodeErrorRow
			sample      sql.NullString
			sampleRaw   sql.NullString
			sampleSrc   sql.NullString
			sampleDst   sql.NullString
			first, last int64
		)
		if err := rows.Scan(&r.SessionID, &r.Fingerprint, &r.Kind, &r.Template,
			&sample, &sampleRaw, &sampleSrc, &sampleDst, &r.Count, &first, &last); err != nil {
			return nil, fmt.Errorf("scan decode error group: %w", err)
		}
		r.Sample, r.SampleRawID = sample.String, sampleRaw.String
		r.SampleSrc, r.SampleDst = sampleSrc.String, sampleDst.String
		r.FirstSeen, r.LastSeen = timeFromUnixNano(first), timeFromUnixNano(last)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate decode error groups: %w", err)
	}
	return out, nil
}

// pgPlaceholders 生成 "$start,$start+1,..." 形式的占位符串。
func pgPlaceholders(n, start int) string {
	s := make([]byte, 0, n*4)
	for i := 0; i < n; i++ {
		if i > 0 {
			s = append(s, ',')
		}
		s = append(s, fmt.Sprintf("$%d", start+i)...)
	}
	return string(s)
}
