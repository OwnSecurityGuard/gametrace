package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PluginValidation is the platform's proof that a plugin instance passed verify.
//
// 它取代了旧的磁盘证明（plugins/<name>/.gametrace/validation.json）：平台不再持有
// 用户插件目录，所以「该插件实例已验证」这条跨平面证据存放在控制库中，按
// (owner, name) 唯一。gt-pipeline 的 verify 写入/清除，gt-mcp 的 status_plugin 读取。
type PluginValidation struct {
	Owner       string
	Name        string
	VerifyRunID string
	SessionID   string
	Verdict     string
	At          time.Time
}

// UpsertPluginValidation 覆盖写入一个插件实例的验证证明（同一 owner+name 只有一条）。
func (cs *ControlStore) UpsertPluginValidation(ctx context.Context, v PluginValidation) error {
	if v.Owner == "" || v.Name == "" {
		return fmt.Errorf("plugin validation: owner and name are required")
	}
	if v.At.IsZero() {
		v.At = time.Now()
	}
	_, err := cs.db.ExecContext(ctx, `
INSERT INTO plugin_validations (owner, name, verify_run_id, session_id, verdict, at)
VALUES (?,?,?,?,?,?)
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
func (cs *ControlStore) GetPluginValidation(ctx context.Context, owner, name string) (*PluginValidation, error) {
	var v PluginValidation
	err := cs.db.QueryRowContext(ctx, `
SELECT owner, name, verify_run_id, session_id, verdict, at
FROM plugin_validations
WHERE owner=? AND name=?`, owner, name,
	).Scan(&v.Owner, &v.Name, &v.VerifyRunID, &v.SessionID, &v.Verdict, &v.At)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ClearPluginValidation 删除某插件的验证证明（非 pass 的 verify 会作废旧证明）。
func (cs *ControlStore) ClearPluginValidation(ctx context.Context, owner, name string) error {
	_, err := cs.db.ExecContext(ctx, `DELETE FROM plugin_validations WHERE owner=? AND name=?`, owner, name)
	return err
}