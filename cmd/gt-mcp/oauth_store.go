// oauth_store.go — MCP OAuth 浏览器授权的持久化（oauth_clients / oauth_codes 两张表）。
//
// 设计（auxDB = sqlite 的 control.sqlite，与项目/用户等组织表同库）：
//   - oauth_clients：RFC 7591 动态注册的 agent 客户端身份（client_id + redirect 白名单）。
//     注意它是「AI 客户端」的身份，不是平台用户——真正的授权门在 approve 必须持有
//     有效用户 Bearer token，agent 拿不拿得到 token 完全取决于用户在授权页的同意。
//   - oauth_codes：一次性授权码（PKCE S256 强制，5 分钟时效）。码记录只存 owner
//     不存 token 值：兑换时经 tokensByOwner（env）/ users.TokenByOwner（表）反查
//     当前 token——密钥不进 OAuth 表，且 approve 与兑换之间用户被 revoke_user
//     撤销时兑换自然失败（invalid_grant），不存在"已发码即锁定旧 token"的窗口。
//   - Consume 用 `UPDATE ... WHERE consumed=0` 的原子消费语义（RowsAffected==1
//     才算成功），比 update-then-check 更严格，天然防并发双重兑换。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const oauthClientSchema = `
CREATE TABLE IF NOT EXISTS oauth_clients (
    client_id     TEXT PRIMARY KEY,
    client_name   TEXT NOT NULL DEFAULT '',
    redirect_uris TEXT NOT NULL DEFAULT '[]',
    created_at    DATETIME NOT NULL,
    last_used_at  DATETIME NOT NULL DEFAULT ''
);`

const oauthCodeSchema = `
CREATE TABLE IF NOT EXISTS oauth_codes (
    code           TEXT PRIMARY KEY,
    client_id      TEXT NOT NULL,
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    owner          TEXT NOT NULL,
    created_at     DATETIME NOT NULL,
    expires_at     DATETIME NOT NULL,
    consumed       INTEGER NOT NULL DEFAULT 0
);`

// oauthClient 是 oauth_clients 表的一行；RedirectURIs 在库里存 JSON 数组字符串。
type oauthClient struct {
	ClientID    string    `json:"client_id"`
	ClientName  string    `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	CreatedAt   time.Time `json:"created_at"`
	LastUsedAt  time.Time `json:"-"`
}

// oauthCode 是 oauth_codes 表的一行（不含任何 token 值）。
type oauthCode struct {
	Code         string    `json:"-"`
	ClientID     string    `json:"-"`
	RedirectURI  string    `json:"-"`
	CodeChallenge string   `json:"-"`
	Owner        string    `json:"-"`
	CreatedAt    time.Time `json:"-"`
	ExpiresAt    time.Time `json:"-"`
	Consumed     bool      `json:"-"`
}

// ===== oauthClientStore =====

type oauthClientStore struct{ db *sql.DB }

func newOAuthClientStore(db *sql.DB) *oauthClientStore { return &oauthClientStore{db: db} }

func (s *oauthClientStore) Init() error {
	if _, err := s.db.Exec(oauthClientSchema); err != nil {
		return fmt.Errorf("create oauth_clients table: %w", err)
	}
	return nil
}

func (s *oauthClientStore) Create(ctx context.Context, c *oauthClient) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return fmt.Errorf("marshal redirect_uris: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO oauth_clients(client_id, client_name, redirect_uris, created_at, last_used_at)
		 VALUES(?,?,?,?,?)`,
		c.ClientID, c.ClientName, string(uris),
		c.CreatedAt.Format(time.RFC3339), "")
	if err != nil {
		return fmt.Errorf("create oauth client: %w", err)
	}
	return nil
}

func (s *oauthClientStore) Get(ctx context.Context, clientID string) (*oauthClient, error) {
	var c oauthClient
	var uris, createdAt string
	var lastUsed sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT client_id, client_name, redirect_uris, created_at, last_used_at
		 FROM oauth_clients WHERE client_id=?`, clientID,
	).Scan(&c.ClientID, &c.ClientName, &uris, &createdAt, &lastUsed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return nil, fmt.Errorf("unmarshal redirect_uris for %s: %w", clientID, err)
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if lastUsed.Valid {
		c.LastUsedAt, _ = time.Parse(time.RFC3339, lastUsed.String)
	}
	return &c, nil
}

// TouchLastUsed 更新客户端最近使用时间（approve 时调用，供审计/清理参考）。
func (s *oauthClientStore) TouchLastUsed(ctx context.Context, clientID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE oauth_clients SET last_used_at=? WHERE client_id=?`,
		time.Now().UTC().Format(time.RFC3339), clientID)
	return err
}

// purgeStale 清理长期未使用的客户端注册（新注册时顺带执行，防表膨胀；
// 失败只记日志由调用方决定，不阻塞注册）。
func (s *oauthClientStore) purgeStale(ctx context.Context, notUsedSince time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_clients WHERE created_at<? AND last_used_at=?`,
		notUsedSince.Format(time.RFC3339), "")
	return err
}

// ===== oauthCodeStore =====

type oauthCodeStore struct{ db *sql.DB }

func newOAuthCodeStore(db *sql.DB) *oauthCodeStore { return &oauthCodeStore{db: db} }

func (s *oauthCodeStore) Init() error {
	if _, err := s.db.Exec(oauthCodeSchema); err != nil {
		return fmt.Errorf("create oauth_codes table: %w", err)
	}
	return nil
}

// Create 落一条授权码，并顺带清理已过期超过 1 天的死码（授权码本身 5 分钟过期，
// 留 1 天仅作审计余量；一次性消费后无保留价值）。
func (s *oauthCodeStore) Create(ctx context.Context, c *oauthCode) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO oauth_codes(code, client_id, redirect_uri, code_challenge, owner, created_at, expires_at, consumed)
		 VALUES(?,?,?,?,?,?,?,?)`,
		c.Code, c.ClientID, c.RedirectURI, c.CodeChallenge, c.Owner,
		c.CreatedAt.Format(time.RFC3339), c.ExpiresAt.Format(time.RFC3339), 0)
	if err != nil {
		return fmt.Errorf("create oauth code: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_codes WHERE expires_at<?`,
		time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("purge expired oauth codes: %w", err)
	}
	return nil
}

func (s *oauthCodeStore) Get(ctx context.Context, code string) (*oauthCode, error) {
	var c oauthCode
	var createdAt, expiresAt string
	var consumed int
	err := s.db.QueryRowContext(ctx,
		`SELECT code, client_id, redirect_uri, code_challenge, owner, created_at, expires_at, consumed
		 FROM oauth_codes WHERE code=?`, code,
	).Scan(&c.Code, &c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.Owner,
		&createdAt, &expiresAt, &consumed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	c.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	c.Consumed = consumed != 0
	return &c, nil
}

// Consume 原子消费授权码（一次性）：仅当尚未消费时置 consumed=1 并报告成功。
// 并发双重兑换/重放攻击中只有一个请求能看到 RowsAffected==1。
func (s *oauthCodeStore) Consume(ctx context.Context, code string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE oauth_codes SET consumed=1 WHERE code=? AND consumed=0`, code)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
