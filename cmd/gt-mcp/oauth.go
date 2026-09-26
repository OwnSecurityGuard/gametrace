// oauth.go — MCP OAuth 浏览器授权：把「用户现有 token」安全递送给 AI agent。
//
// 为什么需要：agent（Trae/Claude/Cursor 等）接平台 MCP 时用户经常不知道去哪拿
// token。MCP 官方 Authorization 规范（OAuth 2.1 + PKCE）让客户端在 401 时自动
// 发现授权端点并拉起浏览器，用户登录+同意后 agent 经本地回调拿回 token——
// 与飞书授权同体验。
//
// 流程（本文件实现授权服务器侧，资源服务器侧只改 401 挑战头，见 http_server.go）：
//   401 + WWW-Authenticate: resource_metadata=…        （http_server.go 包装器）
//   → GET /.well-known/oauth-protected-resource        （RFC 9728）
//   → GET /.well-known/oauth-authorization-server      （RFC 8414）
//   → POST /oauth/register                             （RFC 7591 动态客户端注册）
//   → GET  /oauth/authorize?…&code_challenge           （校验后返回 SPA 授权页）
//   → GET  /oauth/authorize-info（可选 Bearer）         （授权页拉取 client+登录态）
//   → POST /oauth/approve（必须 Bearer）                 （同意发码 / 拒绝）
//   → POST /oauth/token（code+code_verifier，PKCE S256） （换回用户现有 token）
//
// 安全要点：
//   - access_token 是用户现有 token（env / users 表），不新建身份模型；
//     oauth_codes 只存 owner 不存 token，兑换时反查——approve 后被 revoke_user
//     的用户兑换自然失败。
//   - PKCE S256 强制：只有发起授权的 agent（持有 code_verifier）能换到 token。
//   - redirect_uri 三层精确匹配（注册白名单 → authorize 查询 → 码记录 == 兑换请求），
//     未知 client / 不匹配 redirect 时 400 绝不重定向（防开放重定向）。
//   - 授权码 256bit 随机 + 5 分钟时效 + 原子一次性消费（见 oauth_store.go）。
//   - approve 严格 Bearer（不接纳裸 token/查询参数），探针凭证（ProbeID 非空）拒绝。
//   - 匿名模式（未配置任何 token）下所有 OAuth 端点 404：没有"用户"概念，
//     授权无从谈起，客户端也不会收到 401 挑战。
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gametrace/pkg/auth"
)

// oauthCodeTTL 授权码有效期：浏览器授权完成到 agent 回调兑换之间只需要几秒，
// 5 分钟已极宽松；短时效压缩 code 泄露窗口。
const oauthCodeTTL = 5 * time.Minute

// oauthClientStaleAfter 未使用的 DCR 注册保留期（purgeStale 清理阈值）。
const oauthClientStaleAfter = 30 * 24 * time.Hour

// oauthService 聚合 OAuth 授权服务器所需的依赖。
// resolver 与 HTTP 鉴权链是同一个 FirstResolver（env → users 表）：
// 两种来源的用户都能授权；tokensByOwner 用于兑换时反查 env token。
type oauthService struct {
	resolver      auth.Resolver
	users         *userStore
	tokensByOwner map[string]string
	clients       *oauthClientStore
	codes         *oauthCodeStore
	webFS         fs.FS // SPA 资源（/oauth/authorize 返回 index.html）；测试注入 fstest.MapFS
}

func newOAuthService(resolver auth.Resolver, users *userStore, tokensByOwner map[string]string,
	clients *oauthClientStore, codes *oauthCodeStore, webFS fs.FS) *oauthService {
	return &oauthService{
		resolver:      resolver,
		users:         users,
		tokensByOwner: tokensByOwner,
		clients:       clients,
		codes:         codes,
		webFS:         webFS,
	}
}

// required 报告是否处于 token 模式；匿名模式下 OAuth 端点一律 404。
func (o *oauthService) required() bool {
	rc, ok := o.resolver.(requiredResolver)
	return ok && rc.Required()
}

// ===== 基础工具 =====

// newOAuthSecret 生成 256bit 随机 base64url（43 字符）：client_id 与授权码共用。
// crypto/rand 失败直接 panic：密钥生成失败时继续运行比崩溃更危险（fail closed）。
func newOAuthSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("oauth: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// externalBaseURL 回推客户端可访问的绝对基址。
// X-Forwarded-Proto/Host 优先（TLS 终止在反代时 r.TLS 恒 nil，仅看 r.TLS 会给出
// 错误的 http scheme，而 MCP 客户端对公网端点要求 https）；否则 r.TLS 定 scheme、
// r.Host 定 host。多值转发头取第一段。
func externalBaseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		if i := strings.IndexByte(proto, ','); i >= 0 {
			proto = proto[:i]
		}
		if proto = strings.TrimSpace(proto); proto != "" {
			scheme = proto
		}
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		if i := strings.IndexByte(fh, ','); i >= 0 {
			fh = fh[:i]
		}
		if fh = strings.TrimSpace(fh); fh != "" {
			host = fh
		}
	}
	return scheme + "://" + host
}

// validRedirectURI 校验 DCR 注册的回调地址：http 仅限回环（agent 本地回调服务器，
// 任意端口），https 任意 host。其余 scheme（含自定义 scheme、file、data）一律拒绝。
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	default:
		return false
	}
}

// pkceS256Match 校验 PKCE（RFC 7636）：verifier 43-128 字符（unreserved 字符集），
// base64url(sha256(verifier)) == challenge。
func pkceS256Match(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for i := 0; i < len(verifier); i++ {
		c := verifier[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' || c == '.' || c == '_' || c == '~':
		default:
			return false
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

// validCodeChallenge 校验 S256 challenge 形态：43-128 字符的 base64url。
func validCodeChallenge(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// sanitizeClientName 清理 DCR 的 client_name：去首尾空白、剔控制字符（防授权页
// 展示错乱）、截断到 128。空名合法（授权页显示「未知客户端」）。
func sanitizeClientName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// writeJSON 统一 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthErrorResponse 是 RFC 6749 §5.2 形态的错误体。
type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, oauthErrorResponse{Error: code, ErrorDescription: desc})
}

// bearerPrincipal 严格解析 Bearer（仅认 "Bearer <token>" 形式，不接纳裸 token /
// 查询参数——与 auth.Middleware 面向命令行的宽容语义是两回事，OAuth 端点按 RFC
// 严格）。探针凭证（ProbeID 非空）拒绝：探针身份不是"人"，不能替用户做授权决定。
func (o *oauthService) bearerPrincipal(r *http.Request) (*auth.Principal, bool) {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	i := strings.IndexByte(v, ' ')
	if i < 0 || !strings.EqualFold(v[:i], "bearer") {
		return nil, false
	}
	token := strings.TrimSpace(v[i+1:])
	if token == "" {
		return nil, false
	}
	p, ok := o.resolver.Resolve(token)
	if !ok || p == nil || p.ProbeID != "" {
		return nil, false
	}
	return p, true
}

// ownerToken 反查 owner 的当前 token：env 优先（tokensByOwner），users 表兜底。
// 两源无同名 owner（register.go 的保留名检查保证）；查不到 = 用户已被撤销。
func (o *oauthService) ownerToken(ctx context.Context, owner string) (string, bool) {
	if t, ok := o.tokensByOwner[owner]; ok && t != "" {
		return t, true
	}
	if o.users == nil {
		return "", false
	}
	t, err := o.users.TokenByOwner(ctx, owner)
	if err != nil {
		slog.Warn("oauth: lookup user token failed", "owner", owner, "error", err)
		return "", false
	}
	return t, t != ""
}

// ===== authorize 请求校验（authorize / authorize-info / approve 三处共用） =====

// authorizeParams 是授权请求的公共参数（SPA 深链接读取后原样转交 approve，
// 服务端在 approve 时重新校验，不信任前端转发的正确性）。
type authorizeParams struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Method        string
	State         string
}

// authorizeError 区分两类失败：
//   - fatal（未知 client / redirect 不在白名单）：redirect 目标不可信，只能 400，
//     绝不重定向（防开放重定向）；
//   - 非 fatal（response_type / PKCE 参数问题）：client 与 redirect 已验证可信，
//     按 OAuth 惯例重定向回 redirect_uri 附带 error 参数。
type authorizeError struct {
	fatal bool
	code  string
	desc  string
}

// validateAuthorizeRequest 校验公共参数。q 里取 response_type/code_challenge_method
// 存在时的值；approve 转 JSON 提交时也拼成 url.Values 复用本函数。
func (o *oauthService) validateAuthorizeRequest(ctx context.Context, q url.Values) (*authorizeParams, *authorizeError) {
	p := &authorizeParams{
		ClientID:      q.Get("client_id"),
		RedirectURI:   q.Get("redirect_uri"),
		CodeChallenge: q.Get("code_challenge"),
		Method:        q.Get("code_challenge_method"),
		State:         q.Get("state"),
	}
	client, err := o.clients.Get(ctx, p.ClientID)
	if err != nil {
		slog.Warn("oauth: lookup client failed", "error", err)
		return nil, &authorizeError{fatal: true, code: "invalid_client", desc: "client lookup failed"}
	}
	if client == nil {
		return nil, &authorizeError{fatal: true, code: "invalid_client", desc: "unknown client_id"}
	}
	// redirect_uri 必须与注册白名单精确匹配（逐字符，不做规范化宽松比较）。
	matched := false
	for _, u := range client.RedirectURIs {
		if u == p.RedirectURI {
			matched = true
			break
		}
	}
	if !matched {
		return nil, &authorizeError{fatal: true, code: "invalid_request", desc: "redirect_uri not registered for this client"}
	}
	if rt := q.Get("response_type"); rt != "" && rt != "code" {
		// 非 fatal：client 与 redirect 已验证，返回 p 供调用方重定向报错。
		return p, &authorizeError{code: "unsupported_response_type", desc: "only response_type=code is supported"}
	}
	if p.CodeChallenge == "" {
		return p, &authorizeError{code: "invalid_request", desc: "code_challenge is required (PKCE mandatory)"}
	}
	if p.Method != "S256" {
		return p, &authorizeError{code: "invalid_request", desc: "code_challenge_method must be S256"}
	}
	if !validCodeChallenge(p.CodeChallenge) {
		return p, &authorizeError{code: "invalid_request", desc: "code_challenge must be 43-128 base64url characters"}
	}
	return p, nil
}

// redirectWithError 重定向回 redirect_uri 并附带 OAuth error 参数（state 原样回传）。
func redirectWithError(w http.ResponseWriter, p *authorizeParams, code, desc string) {
	u, err := url.Parse(p.RedirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	qs := u.Query()
	qs.Set("error", code)
	qs.Set("error_description", desc)
	if p.State != "" {
		qs.Set("state", p.State)
	}
	u.RawQuery = qs.Encode()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", u.String())
	w.WriteHeader(http.StatusFound)
}

// ===== 端点：well-known 元数据 =====

// handleProtectedResource（RFC 9728）：告诉 401 的客户端去哪找授权服务器。
func (o *oauthService) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	if !o.required() {
		http.NotFound(w, r)
		return
	}
	base := externalBaseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":              base,
		"authorization_servers": []string{base},
	})
}

// handleAuthServerMetadata（RFC 8414）：授权服务器端点元数据。
func (o *oauthService) handleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	if !o.required() {
		http.NotFound(w, r)
		return
	}
	base := externalBaseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

// ===== 端点：动态客户端注册（RFC 7591） =====

type clientRegisterRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

type clientRegisterResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// handleClientRegister 处理 RFC 7591 动态客户端注册：agent 在发起授权前没有
// 任何凭证，凭一次注册拿到 client_id（公开客户端，token_endpoint_auth_method=none，
// 安全性由 PKCE 保证）。注册的是「agent 客户端」身份而非平台用户——真正的授权
// 门在 approve 必须持有用户 Bearer token。
func (o *oauthService) handleClientRegister(w http.ResponseWriter, r *http.Request) {
	if !o.required() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req clientRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON body")
		return
	}
	// redirect_uris：必填非空，逐条校验（http 仅回环 / https 任意）。
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"http redirect only on loopback (localhost/127.0.0.1/[::1]); https allowed anywhere")
			return
		}
	}
	// 可选字段出现时只接受本实现支持的取值（公开客户端 + 授权码 + code）。
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata",
			"only token_endpoint_auth_method=none is supported")
		return
	}
	if len(req.GrantTypes) > 0 && !(len(req.GrantTypes) == 1 && req.GrantTypes[0] == "authorization_code") {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata",
			"only grant_types=[authorization_code] is supported")
		return
	}
	if len(req.ResponseTypes) > 0 && !(len(req.ResponseTypes) == 1 && req.ResponseTypes[0] == "code") {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata",
			"only response_types=[code] is supported")
		return
	}

	now := time.Now().UTC()
	rec := &oauthClient{
		ClientID:     newOAuthSecret(),
		ClientName:   sanitizeClientName(req.ClientName),
		RedirectURIs: req.RedirectURIs,
		CreatedAt:    now,
	}
	ctx := r.Context()
	if err := o.clients.Create(ctx, rec); err != nil {
		slog.Error("oauth: client register failed", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "persist client failed")
		return
	}
	// 顺带清理长期未使用的陈旧注册（失败不影响本次注册）。
	cutoff := now.Add(-oauthClientStaleAfter)
	if err := o.clients.purgeStale(ctx, cutoff); err != nil {
		slog.Warn("oauth: purge stale clients failed", "error", err)
	}
	slog.Info("oauth client registered", "client_id", rec.ClientID, "client_name", rec.ClientName,
		"redirect_uris", strings.Join(rec.RedirectURIs, ","), "remote", r.RemoteAddr)
	writeJSON(w, http.StatusCreated, clientRegisterResponse{
		ClientID:                rec.ClientID,
		ClientIDIssuedAt:        now.Unix(),
		ClientName:              rec.ClientName,
		RedirectURIs:            rec.RedirectURIs,
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
	})
}

// ===== 端点：authorize（浏览器深链接入口） =====

// handleAuthorize 校验授权请求参数后返回 SPA 授权页（index.html 由前端路由分支
// 渲染 AuthorizePage）。client/redirect 不可信时 400 短错误页（绝不重定向）；
// 参数问题重定向回 redirect_uri 报错（agent 在回调里能看到失败原因）。
func (o *oauthService) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if !o.required() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, aerr := o.validateAuthorizeRequest(r.Context(), r.URL.Query())
	if aerr != nil {
		if aerr.fatal {
			http.Error(w, "授权请求无效："+aerr.desc, http.StatusBadRequest)
			return
		}
		redirectWithError(w, p, aerr.code, aerr.desc)
		return
	}
	// 校验通过：返回 SPA（前端按 window.location.pathname 识别 /oauth/authorize
	// 并渲染授权页，参数从 URL 读取）。
	serveIndexHTML(w, o.webFS)
}

// authorizeInfoResponse 是授权页渲染所需的全部数据。
type authorizeInfoResponse struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
	// Owner 为空串表示未登录/凭证无效，前端显示登录卡；非空表示当前登录身份，
	// 前端直接显示确认卡。
	Owner   string `json:"owner"`
	IsAdmin bool   `json:"is_admin"`
}

// handleAuthorizeInfo 授权页数据端点：验证 authorize 参数（供页面尽早报错）+
// 回显 client 名称与当前登录态。Bearer 可选：无/无效时 owner 为空串（而非 401），
// 前端据此展示登录卡；登录卡提交后前端带着 token 重调本端点验证。
func (o *oauthService) handleAuthorizeInfo(w http.ResponseWriter, r *http.Request) {
	if !o.required() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, aerr := o.validateAuthorizeRequest(r.Context(), r.URL.Query())
	if aerr != nil || p == nil {
		// XHR 场景：两类错误都返回 400 JSON（p 仅在 fatal 时为 nil，
		// 非 fatal 时 p 已解析出 redirect 但页面无需重定向，错误码原样给出）。
		desc := "invalid authorize request"
		code := "invalid_request"
		if aerr != nil {
			code, desc = aerr.code, aerr.desc
		}
		oauthError(w, http.StatusBadRequest, code, desc)
		return
	}
	client, err := o.clients.Get(r.Context(), p.ClientID)
	if err != nil || client == nil {
		oauthError(w, http.StatusBadRequest, "invalid_client", "unknown client_id")
		return
	}
	resp := authorizeInfoResponse{ClientID: client.ClientID, ClientName: client.ClientName}
	if pr, ok := o.bearerPrincipal(r); ok {
		resp.Owner = pr.Owner
		resp.IsAdmin = pr.IsAdmin
	}
	writeJSON(w, http.StatusOK, resp)
}

// ===== 端点：approve（授权页同意/拒绝） =====

type approveRequest struct {
	Action             string `json:"action"` // approve | deny
	ClientID           string `json:"client_id"`
	RedirectURI        string `json:"redirect_uri"`
	CodeChallenge      string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	State              string `json:"state"`
}

type approveResponse struct {
	// action=approve
	Code        string `json:"code,omitempty"`
	// 公共：回调地址与 state（前端拼 URL 跳转）
	RedirectURI string `json:"redirect_uri"`
	State       string `json:"state,omitempty"`
	// action=deny
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// handleApprove 处理授权页的同意/拒绝。必须持有效用户 Bearer（严格解析）；
// 参数在服务端重新校验（不信任前端转发的正确性）。同意时签发一次性授权码
// （绑定 client/redirect/challenge/owner），拒绝时返回 access_denied 载荷，
// 两者都由前端拼接 redirect_uri 跳转回 agent 的本地回调。
func (o *oauthService) handleApprove(w http.ResponseWriter, r *http.Request) {
	if !o.required() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// CSRF 双保险：强制 JSON（跨站表单无法伪造 application/json）+ Bearer 头
	//（跨站表单/简单请求无法携带 Authorization）。
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		oauthError(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/json")
		return
	}
	pr, ok := o.bearerPrincipal(r)
	if !ok {
		oauthError(w, http.StatusUnauthorized, "invalid_token", "valid user Bearer token required")
		return
	}
	var req approveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Action != "approve" && req.Action != "deny" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "action must be approve or deny")
		return
	}
	// 服务端重验参数（与 authorize 同一套规则）。
	q := url.Values{
		"client_id":     {req.ClientID},
		"redirect_uri":  {req.RedirectURI},
		"code_challenge": {req.CodeChallenge},
	}
	if req.CodeChallengeMethod != "" {
		q.Set("code_challenge_method", req.CodeChallengeMethod)
	}
	if req.State != "" {
		q.Set("state", req.State)
	}
	p, aerr := o.validateAuthorizeRequest(r.Context(), q)
	if aerr != nil || p == nil {
		code, desc := "invalid_request", "invalid authorize request"
		if aerr != nil {
			code, desc = aerr.code, aerr.desc
		}
		oauthError(w, http.StatusBadRequest, code, desc)
		return
	}

	if req.Action == "deny" {
		slog.Info("oauth denied", "owner", pr.Owner, "client_id", p.ClientID, "remote", r.RemoteAddr)
		writeJSON(w, http.StatusOK, approveResponse{
			RedirectURI: p.RedirectURI, State: p.State,
			Error: "access_denied", ErrorDescription: "用户拒绝了本次授权",
		})
		return
	}

	now := time.Now().UTC()
	rec := &oauthCode{
		Code:         newOAuthSecret(),
		ClientID:     p.ClientID,
		RedirectURI:  p.RedirectURI,
		CodeChallenge: p.CodeChallenge,
		Owner:        pr.Owner,
		CreatedAt:    now,
		ExpiresAt:    now.Add(oauthCodeTTL),
	}
	if err := o.codes.Create(r.Context(), rec); err != nil {
		slog.Error("oauth: create code failed", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "persist code failed")
		return
	}
	if err := o.clients.TouchLastUsed(r.Context(), p.ClientID); err != nil {
		slog.Warn("oauth: touch client failed", "error", err)
	}
	client, _ := o.clients.Get(r.Context(), p.ClientID)
	clientName := ""
	if client != nil {
		clientName = client.ClientName
	}
	slog.Info("oauth approved", "owner", pr.Owner, "client_id", p.ClientID, "client_name", clientName, "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, approveResponse{
		Code: rec.Code, RedirectURI: p.RedirectURI, State: p.State,
	})
}

// ===== 端点：token（授权码兑换） =====

// tokenRequest 兑换参数：OAuth 规范是 form-encoded，部分客户端发 JSON，两者都收。
type tokenRequest struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	ClientID     string `json:"client_id"`
	RedirectURI  string `json:"redirect_uri"`
	Resource     string `json:"resource"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// parseTokenRequest 兼容 form 与 JSON 两种编码。
func parseTokenRequest(r *http.Request) (*tokenRequest, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var req tokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		return &req, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return &tokenRequest{
		GrantType:    r.Form.Get("grant_type"),
		Code:         r.Form.Get("code"),
		CodeVerifier: r.Form.Get("code_verifier"),
		ClientID:     r.Form.Get("client_id"),
		RedirectURI:  r.Form.Get("redirect_uri"),
		Resource:     r.Form.Get("resource"),
	}, nil
}

// handleToken 授权码换 token：PKCE 校验通过后原子消费授权码，反查 owner 的
// 当前 token 返回。失败统一 invalid_grant（不泄露是哪一步失败，防探测）。
func (o *oauthService) handleToken(w http.ResponseWriter, r *http.Request) {
	if !o.required() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := parseTokenRequest(r)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed request body")
		return
	}
	if req.GrantType != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only authorization_code is supported (tokens are long-lived, no refresh needed)")
		return
	}
	if req.Code == "" || req.ClientID == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code and client_id are required")
		return
	}
	// resource 指示符（RFC 8707）出现时必须指向本站。
	if req.Resource != "" && req.Resource != externalBaseURL(r) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource does not match this server")
		return
	}

	ctx := r.Context()
	rec, err := o.codes.Get(ctx, req.Code)
	if err != nil {
		slog.Warn("oauth: lookup code failed", "error", err)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid code")
		return
	}
	if rec == nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid code")
		return
	}
	if time.Now().After(rec.ExpiresAt) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code expired")
		return
	}
	if rec.ClientID != req.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_client", "client_id mismatch")
		return
	}
	if rec.RedirectURI != req.RedirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if !pkceS256Match(req.CodeVerifier, rec.CodeChallenge) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	// 原子一次性消费：并发重放只有一个成功。
	ok, err := o.codes.Consume(ctx, req.Code)
	if err != nil || !ok {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code already used")
		return
	}
	token, ok := o.ownerToken(ctx, rec.Owner)
	if !ok {
		// approve 之后用户被 revoke_user：兑换失败（fail closed）。
		oauthError(w, http.StatusBadRequest, "invalid_grant", "user no longer exists")
		return
	}
	slog.Info("oauth token issued", "owner", rec.Owner, "client_id", req.ClientID, "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, tokenResponse{AccessToken: token, TokenType: "Bearer"})
}
