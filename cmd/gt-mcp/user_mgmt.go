// user_mgmt.go — 成员账号管理（list_users / revoke_user）与 env token 反查。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/auth"
	"gametrace/pkg/authz"
)

// loadTokensByOwner 从 GT_AUTH_TOKENS 解析 owner->token（"alice=gt_xxx:admin" 取 "gt_xxx"）。
// 复刻 auth.ParseTokens 的格式但不改动 auth 包（后者只暴露 token->owner 单一方向）。
func loadTokensByOwner() map[string]string {
	m := map[string]string{}
	for _, seg := range strings.Split(os.Getenv(auth.EnvTokens), ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		eq := strings.IndexByte(seg, '=')
		if eq < 0 {
			continue
		}
		owner := strings.TrimSpace(seg[:eq])
		tok := strings.TrimSpace(seg[eq+1:])
		if i := strings.LastIndexByte(tok, ':'); i >= 0 {
			tok = tok[:i]
		}
		if owner != "" && tok != "" {
			m[owner] = tok
		}
	}
	return m
}

// handleListUsers 列出成员账号（仅 global admin；不回 token —— 凭证只在创建时展示一次）。
// users 表之外，env bootstrap 身份（GT_AUTH_TOKENS）以 bootstrap_owners 单独返回：
// 它们不在 users 表、不可撤销，但成员管理界面应可见，否则 admin 看不到自己。
func (m *mcpCapture) handleListUsers(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if err := m.authz.Can(ctx, authz.ActionUserManage, authz.Resource{Kind: authz.KindUser}); err != nil {
		return errorResult(err), nil
	}
	users, err := m.users.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	if users == nil {
		users = []user{}
	}
	bootstrap := m.envResolver.Owners()
	if bootstrap == nil {
		bootstrap = []string{}
	}
	return successResult(map[string]any{"users": users, "bootstrap_owners": bootstrap}), nil
}

// handleRevokeUser 撤销自助注册用户（删除 users 行，token 即时失效；仅 global admin）。
// 只能撤销 users 表里的身份：env bootstrap（GT_AUTH_TOKENS）不在此列，天然不可撤销。
func (m *mcpCapture) handleRevokeUser(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if err := m.authz.Can(ctx, authz.ActionUserManage, authz.Resource{Kind: authz.KindUser}); err != nil {
		return errorResult(err), nil
	}
	owner := strings.TrimSpace(req.GetString("owner", ""))
	if owner == "" {
		return errorResult(fmt.Errorf("owner is required")), nil
	}
	if owner == authzPrincipal(ctx).User {
		return errorResult(fmt.Errorf("cannot revoke yourself")), nil
	}
	found, err := m.users.Revoke(ctx, owner)
	if err != nil {
		return nil, err
	}
	if !found {
		return errorResult(fmt.Errorf("user %s not found (env bootstrap tokens are not revocable here)", owner)), nil
	}
	slog.Info("user revoked", "owner", owner, "actor", authzPrincipal(ctx).User)
	return successResult(map[string]any{"owner": owner, "revoked": true}), nil
}