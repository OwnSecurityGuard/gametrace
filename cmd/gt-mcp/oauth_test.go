// oauth_test.go — MCP OAuth 浏览器授权全流程测试：well-known 发现 → DCR →
// authorize 校验 → approve（env 与 users 表两种身份）→ PKCE 兑换 → 401 挑战头。
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gametrace/pkg/auth"
	"gametrace/pkg/store"
)

// newOAuthTest 构造与 main() 同构的 OAuth 测试服务（root mux + /oauth/ 子 mux），
// envSpec 形如 "alice=gt_alice_secret:admin"。
func newOAuthTest(t *testing.T, envSpec string) (*oauthService, *userStore, http.Handler) {
	t.Helper()
	t.Setenv(auth.EnvTokens, envSpec)
	env, err := auth.ParseTokens(envSpec)
	if err != nil {
		t.Fatalf("parse env tokens: %v", err)
	}
	cs, err := store.NewControlStore(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	us := newUserStore(cs.DB())
	if err := us.Init(); err != nil {
		t.Fatal(err)
	}
	clients := newOAuthClientStore(cs.DB())
	if err := clients.Init(); err != nil {
		t.Fatal(err)
	}
	codes := newOAuthCodeStore(cs.DB())
	if err := codes.Init(); err != nil {
		t.Fatal(err)
	}
	resolver := auth.NewFirstResolver(env, auth.NewDBResolver(cs.DB()))
	webFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>authorize-page</html>")}}
	srv := newOAuthService(resolver, us, loadTokensByOwner(), clients, codes, webFS)

	// 路由装配与 main() 一致。
	root := http.NewServeMux()
	root.HandleFunc("/.well-known/oauth-protected-resource", srv.handleProtectedResource)
	root.HandleFunc("/.well-known/oauth-authorization-server", srv.handleAuthServerMetadata)
	oauthMux := http.NewServeMux()
	oauthMux.HandleFunc("/register", srv.handleClientRegister)
	oauthMux.HandleFunc("/authorize", srv.handleAuthorize)
	oauthMux.HandleFunc("/authorize-info", srv.handleAuthorizeInfo)
	oauthMux.HandleFunc("/approve", srv.handleApprove)
	oauthMux.HandleFunc("/token", srv.handleToken)
	root.Handle("/oauth/", http.StripPrefix("/oauth", oauthMux))
	return srv, us, root
}

// testPKCE 生成一对 verifier/challenge（与客户端侧算法一致）。
func testPKCE(t *testing.T) (verifier, challenge string) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// doOAuth 向测试服务发请求并返回 recorder。
func doOAuth(h http.Handler, method, target, bearer, contentType, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// registerOAuthClient 走 DCR 注册一个客户端，返回 client_id。
func registerOAuthClient(t *testing.T, h http.Handler, name, redirectURI string) string {
	t.Helper()
	body := fmt.Sprintf(`{"client_name":%q,"redirect_uris":[%q]}`, name, redirectURI)
	rec := doOAuth(h, http.MethodPost, "http://example.com/oauth/register", "", "application/json", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("client register status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res clientRegisterResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.ClientID) != 43 {
		t.Fatalf("client_id should be 43-char base64url, got %q", res.ClientID)
	}
	return res.ClientID
}

// authorizeQuery 构造标准授权请求 query。
func authorizeQuery(clientID, redirectURI, challenge, state string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if state != "" {
		q.Set("state", state)
	}
	return q.Encode()
}

// approveAndExchange 走完 approve→token 兑换，返回兑换响应 recorder。
func approveAndExchange(h http.Handler, clientID, redirectURI, challenge, state, verifier, bearer, code string) *httptest.ResponseRecorder {
	if code == "" {
		body := fmt.Sprintf(`{"action":"approve","client_id":%q,"redirect_uri":%q,"code_challenge":%q,"code_challenge_method":"S256","state":%q}`,
			clientID, redirectURI, challenge, state)
		rec := doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", bearer, "application/json", body)
		if rec.Code != http.StatusOK {
			return rec
		}
		var res approveResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		code = res.Code
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	}
	return doOAuth(h, http.MethodPost, "http://example.com/oauth/token", "",
		"application/x-www-form-urlencoded", form.Encode())
}

// ===== 1. well-known 元数据 =====

func TestOAuthMetadataEndpoints(t *testing.T) {
	_, _, h := newOAuthTest(t, "alice=gt_alice_secret:admin")

	rec := doOAuth(h, http.MethodGet, "http://example.com/.well-known/oauth-protected-resource", "", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var pr struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil {
		t.Fatal(err)
	}
	if pr.Resource != "http://example.com" || len(pr.AuthorizationServers) != 1 || pr.AuthorizationServers[0] != "http://example.com" {
		t.Fatalf("protected-resource metadata unexpected: %+v", pr)
	}

	rec = doOAuth(h, http.MethodGet, "http://example.com/.well-known/oauth-authorization-server", "", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var as map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &as); err != nil {
		t.Fatal(err)
	}
	if as["authorization_endpoint"] != "http://example.com/oauth/authorize" ||
		as["token_endpoint"] != "http://example.com/oauth/token" ||
		as["registration_endpoint"] != "http://example.com/oauth/register" {
		t.Fatalf("authorization-server metadata unexpected: %v", as)
	}
	methods, _ := as["code_challenge_methods_supported"].([]any)
	if len(methods) != 1 || methods[0] != "S256" {
		t.Fatalf("code_challenge_methods_supported = %v, want [S256]", methods)
	}
}

// 匿名模式：未配置任何 token 时所有 OAuth 端点 404。
func TestOAuthAnonymousModeAll404(t *testing.T) {
	_, _, h := newOAuthTest(t, "")
	targets := []string{
		"http://example.com/.well-known/oauth-protected-resource",
		"http://example.com/.well-known/oauth-authorization-server",
		"http://example.com/oauth/register",
		"http://example.com/oauth/authorize",
		"http://example.com/oauth/authorize-info",
		"http://example.com/oauth/approve",
		"http://example.com/oauth/token",
	}
	for _, target := range targets {
		rec := doOAuth(h, http.MethodGet, target, "", "", "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, rec.Code)
		}
	}
}

// ===== 2. 动态客户端注册 =====

func TestOAuthClientRegister(t *testing.T) {
	_, _, h := newOAuthTest(t, "alice=gt_alice_secret:admin")

	registerOAuthClient(t, h, "Trae", "http://localhost:37777/callback")

	cases := []struct {
		name string
		body string
	}{
		{"空 redirect_uris", `{"client_name":"x","redirect_uris":[]}`},
		{"缺 redirect_uris", `{"client_name":"x"}`},
		{"非回环 http", `{"client_name":"x","redirect_uris":["http://evil.com/cb"]}`},
		{"file scheme", `{"client_name":"x","redirect_uris":["file:///etc"]}`},
		{"不支持的 auth method", `{"redirect_uris":["http://localhost:1/cb"],"token_endpoint_auth_method":"client_secret_basic"}`},
		{"不支持的 grant", `{"redirect_uris":["http://localhost:1/cb"],"grant_types":["password"]}`},
		{"坏 JSON", `{`},
	}
	for _, c := range cases {
		rec := doOAuth(h, http.MethodPost, "http://example.com/oauth/register", "", "application/json", c.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body=%s)", c.name, rec.Code, rec.Body.String())
		}
	}
	// https 任意 host 合法。
	rec := doOAuth(h, http.MethodPost, "http://example.com/oauth/register", "", "application/json",
		`{"client_name":"x","redirect_uris":["https://any.host/cb"]}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("https redirect: status = %d", rec.Code)
	}
	// client_name 控制字符被剔除。
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/register", "", "application/json",
		`{"client_name":"a\nb\u0000c","redirect_uris":["http://localhost:1/cb"]}`)
	var res clientRegisterResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.ClientName != "abc" {
		t.Errorf("sanitized name = %q, want %q", res.ClientName, "abc")
	}
}

// ===== 3. authorize 参数校验 =====

func TestOAuthAuthorizeValidation(t *testing.T) {
	_, _, h := newOAuthTest(t, "alice=gt_alice_secret:admin")
	const redirectURI = "http://localhost:37777/callback"
	clientID := registerOAuthClient(t, h, "Trae", redirectURI)
	_, challenge := testPKCE(t)

	// 未知 client → 400（绝不重定向）。
	q := authorizeQuery("unknown-client", redirectURI, challenge, "st")
	rec := doOAuth(h, http.MethodGet, "http://example.com/oauth/authorize?"+q, "", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown client: status = %d, want 400", rec.Code)
	}

	// redirect_uri 不在白名单 → 400。
	q = authorizeQuery(clientID, "http://localhost:9999/other", challenge, "st")
	rec = doOAuth(h, http.MethodGet, "http://example.com/oauth/authorize?"+q, "", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unregistered redirect: status = %d, want 400", rec.Code)
	}

	// response_type=token → 302 unsupported_response_type。
	q = authorizeQuery(clientID, redirectURI, challenge, "st")
	q = strings.Replace(q, "response_type=code", "response_type=token", 1)
	rec = doOAuth(h, http.MethodGet, "http://example.com/oauth/authorize?"+q, "", "", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("response_type=token: status = %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("error") != "unsupported_response_type" || loc.Query().Get("state") != "st" {
		t.Fatalf("redirect error params unexpected: %s", rec.Header().Get("Location"))
	}

	// 缺 code_challenge → 302 invalid_request（其余参数完整，仅去掉 challenge）。
	vv := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge_method": {"S256"},
		"state":                 {"st"},
	}
	rec = doOAuth(h, http.MethodGet, "http://example.com/oauth/authorize?"+vv.Encode(), "", "", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("missing challenge: status = %d, want 302 (body=%s)", rec.Code, rec.Body.String())
	}
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("error") != "invalid_request" {
		t.Fatalf("missing challenge error = %q", loc.Query().Get("error"))
	}

	// 非 S256 → 302。
	vv = url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"plain"},
	}
	rec = doOAuth(h, http.MethodGet, "http://example.com/oauth/authorize?"+vv.Encode(), "", "", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("plain method: status = %d, want 302", rec.Code)
	}

	// 合法请求 → 200 SPA index.html。
	q = authorizeQuery(clientID, redirectURI, challenge, "st")
	rec = doOAuth(h, http.MethodGet, "http://example.com/oauth/authorize?"+q, "", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("valid authorize: status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "authorize-page") {
		t.Fatalf("authorize should serve SPA index.html, got %s", rec.Body.String())
	}
}

// ===== 4. approve =====

func TestOAuthApprove(t *testing.T) {
	_, us, h := newOAuthTest(t, "alice=gt_alice_secret:admin")
	const redirectURI = "http://localhost:37777/callback"
	clientID := registerOAuthClient(t, h, "Trae", redirectURI)
	_, challenge := testPKCE(t)

	approveBody := func(action string) string {
		return fmt.Sprintf(`{"action":%q,"client_id":%q,"redirect_uri":%q,"code_challenge":%q,"code_challenge_method":"S256","state":"s1"}`,
			action, clientID, redirectURI, challenge)
	}

	// 无 Bearer → 401。
	rec := doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", "", "application/json", approveBody("approve"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status = %d, want 401", rec.Code)
	}
	// 坏 token → 401。
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", "gt_wrong", "application/json", approveBody("approve"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: status = %d, want 401", rec.Code)
	}
	// 非 JSON Content-Type → 415（CSRF 防线）。
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", "gt_alice_secret", "application/x-www-form-urlencoded", "action=approve")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form content-type: status = %d, want 415", rec.Code)
	}

	// env 身份 approve → 拿到码。
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", "gt_alice_secret", "application/json", approveBody("approve"))
	if rec.Code != http.StatusOK {
		t.Fatalf("env approve: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res approveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Code == "" || res.State != "s1" || res.RedirectURI != redirectURI {
		t.Fatalf("approve response unexpected: %+v", res)
	}

	// users 表身份 approve：先注册用户。
	_, carolToken, err := us.CreateUser(t.Context(), "carol", "")
	if err != nil {
		t.Fatal(err)
	}
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", carolToken, "application/json", approveBody("approve"))
	if rec.Code != http.StatusOK {
		t.Fatalf("users-table approve: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// deny → access_denied 载荷。
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", "gt_alice_secret", "application/json", approveBody("deny"))
	if rec.Code != http.StatusOK {
		t.Fatalf("deny: status = %d", rec.Code)
	}
	res = approveResponse{}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Error != "access_denied" || res.State != "s1" || res.RedirectURI != redirectURI {
		t.Fatalf("deny response unexpected: %+v", res)
	}

	// 参数不合法（未知 client）→ 400，不签码。
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", "gt_alice_secret", "application/json",
		strings.Replace(approveBody("approve"), clientID, "unknown", 1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown client approve: status = %d, want 400", rec.Code)
	}
}

// ===== 5/6. token 兑换 =====

func TestOAuthTokenHappyPath(t *testing.T) {
	_, us, h := newOAuthTest(t, "alice=gt_alice_secret:admin")
	const redirectURI = "http://localhost:37777/callback"
	clientID := registerOAuthClient(t, h, "Trae", redirectURI)
	verifier, challenge := testPKCE(t)

	// env 用户完整流：authorize-info（未登录）→ approve → 兑换。
	q := authorizeQuery(clientID, redirectURI, challenge, "s1")
	rec := doOAuth(h, http.MethodGet, "http://example.com/oauth/authorize-info?"+q, "", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize-info: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var info authorizeInfoResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info.ClientName != "Trae" || info.Owner != "" {
		t.Fatalf("authorize-info (anonymous) unexpected: %+v", info)
	}
	// 带 token 的 authorize-info 回显登录态。
	rec = doOAuth(h, http.MethodGet, "http://example.com/oauth/authorize-info?"+q, "gt_alice_secret", "", "")
	info = authorizeInfoResponse{}
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info.Owner != "alice" || !info.IsAdmin {
		t.Fatalf("authorize-info (logged in) unexpected: %+v", info)
	}

	rec = approveAndExchange(h, clientID, redirectURI, challenge, "s1", verifier, "gt_alice_secret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("token exchange: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var tres tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tres); err != nil {
		t.Fatal(err)
	}
	if tres.AccessToken != "gt_alice_secret" || tres.TokenType != "Bearer" {
		t.Fatalf("access_token = %q (want env token)", tres.AccessToken)
	}

	// users 表用户完整流：兑换返回其表内 token。
	_, carolToken, err := us.CreateUser(t.Context(), "carol", "")
	if err != nil {
		t.Fatal(err)
	}
	verifier2, challenge2 := testPKCE(t)
	rec = approveAndExchange(h, clientID, redirectURI, challenge2, "s2", verifier2, carolToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("carol exchange: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	tres = tokenResponse{}
	_ = json.Unmarshal(rec.Body.Bytes(), &tres)
	if tres.AccessToken != carolToken {
		t.Fatalf("carol access_token = %q, want %q", tres.AccessToken, carolToken)
	}
}

func TestOAuthTokenFailures(t *testing.T) {
	srv, us, h := newOAuthTest(t, "alice=gt_alice_secret:admin")
	const redirectURI = "http://localhost:37777/callback"
	clientID := registerOAuthClient(t, h, "Trae", redirectURI)
	verifier, challenge := testPKCE(t)

	// 先走一次 approve 拿到有效 code。
	body := fmt.Sprintf(`{"action":"approve","client_id":%q,"redirect_uri":%q,"code_challenge":%q,"code_challenge_method":"S256","state":"s1"}`,
		clientID, redirectURI, challenge)
	rec := doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", "gt_alice_secret", "application/json", body)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	var ares approveResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &ares)
	code := ares.Code

	exchange := func(verifierStr, codeStr, clientIDStr, redirectURIStr string) *httptest.ResponseRecorder {
		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {codeStr},
			"code_verifier": {verifierStr},
			"client_id":     {clientIDStr},
			"redirect_uri":  {redirectURIStr},
		}
		return doOAuth(h, http.MethodPost, "http://example.com/oauth/token", "",
			"application/x-www-form-urlencoded", form.Encode())
	}
	errCode := func(rec *httptest.ResponseRecorder) string {
		var e oauthErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		return e.Error
	}

	// 错 verifier → invalid_grant。
	otherVerifier := strings.Repeat("x", 43)
	if rec := exchange(otherVerifier, code, clientID, redirectURI); rec.Code != http.StatusBadRequest || errCode(rec) != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %s", rec.Code, errCode(rec))
	}
	// client_id 不符 → invalid_client。
	if rec := exchange(verifier, code, "other-client", redirectURI); rec.Code != http.StatusBadRequest || errCode(rec) != "invalid_client" {
		t.Fatalf("client mismatch: %d %s", rec.Code, errCode(rec))
	}
	// redirect_uri 不符 → invalid_grant。
	if rec := exchange(verifier, code, clientID, "http://localhost:1/cb"); rec.Code != http.StatusBadRequest || errCode(rec) != "invalid_grant" {
		t.Fatalf("redirect mismatch: %d %s", rec.Code, errCode(rec))
	}
	// 正确兑换（成功消费）。
	if rec := exchange(verifier, code, clientID, redirectURI); rec.Code != http.StatusOK {
		t.Fatalf("valid exchange failed: %d %s", rec.Code, rec.Body.String())
	}
	// 码复用 → invalid_grant。
	if rec := exchange(verifier, code, clientID, redirectURI); rec.Code != http.StatusBadRequest || errCode(rec) != "invalid_grant" {
		t.Fatalf("code reuse: %d %s", rec.Code, errCode(rec))
	}
	// 过期码：直接造一条已过期的。
	expired := &oauthCode{
		Code: newOAuthSecret(), ClientID: clientID, RedirectURI: redirectURI,
		CodeChallenge: challenge, Owner: "alice",
		CreatedAt: time.Now().Add(-10 * time.Minute), ExpiresAt: time.Now().Add(-5 * time.Minute),
	}
	if err := srv.codes.Create(t.Context(), expired); err != nil {
		t.Fatal(err)
	}
	if rec := exchange(verifier, expired.Code, clientID, redirectURI); rec.Code != http.StatusBadRequest || errCode(rec) != "invalid_grant" {
		t.Fatalf("expired code: %d %s", rec.Code, errCode(rec))
	}
	// 用户被撤销：approve 后 revoke，兑换失败（fail closed）。
	_, bobToken, err := us.CreateUser(t.Context(), "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	verifier2, challenge2 := testPKCE(t)
	bobBody := fmt.Sprintf(`{"action":"approve","client_id":%q,"redirect_uri":%q,"code_challenge":%q,"code_challenge_method":"S256"}`,
		clientID, redirectURI, challenge2)
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", bobToken, "application/json", bobBody)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ares)
	if _, err := us.Revoke(t.Context(), "bob"); err != nil {
		t.Fatal(err)
	}
	if rec := exchange(verifier2, ares.Code, clientID, redirectURI); rec.Code != http.StatusBadRequest || errCode(rec) != "invalid_grant" {
		t.Fatalf("revoked user: %d %s", rec.Code, errCode(rec))
	}
	// unsupported grant_type。
	form := url.Values{"grant_type": {"refresh_token"}, "code": {"x"}}
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/token", "", "application/x-www-form-urlencoded", form.Encode())
	if rec.Code != http.StatusBadRequest || errCode(rec) != "unsupported_grant_type" {
		t.Fatalf("refresh_token: %d %s", rec.Code, errCode(rec))
	}
	// resource 不符 → invalid_target。
	form = url.Values{
		"grant_type": {"authorization_code"}, "code": {"x"},
		"client_id": {clientID},
		"resource":  {"http://other.com"},
	}
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/token", "", "application/x-www-form-urlencoded", form.Encode())
	if rec.Code != http.StatusBadRequest || errCode(rec) != "invalid_target" {
		t.Fatalf("bad resource: %d %s", rec.Code, errCode(rec))
	}
}

// JSON 编码的 token 请求同样被接受（部分 MCP 客户端发 JSON）。
func TestOAuthTokenFormAndJSON(t *testing.T) {
	_, _, h := newOAuthTest(t, "alice=gt_alice_secret:admin")
	const redirectURI = "http://localhost:37777/callback"
	clientID := registerOAuthClient(t, h, "Trae", redirectURI)
	verifier, challenge := testPKCE(t)

	// JSON 走完 approve + 兑换。
	body := fmt.Sprintf(`{"action":"approve","client_id":%q,"redirect_uri":%q,"code_challenge":%q,"code_challenge_method":"S256"}`,
		clientID, redirectURI, challenge)
	rec := doOAuth(h, http.MethodPost, "http://example.com/oauth/approve", "gt_alice_secret", "application/json", body)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	var ares approveResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &ares)

	jsonBody := fmt.Sprintf(`{"grant_type":"authorization_code","code":%q,"code_verifier":%q,"client_id":%q,"redirect_uri":%q}`,
		ares.Code, verifier, clientID, redirectURI)
	rec = doOAuth(h, http.MethodPost, "http://example.com/oauth/token", "gt_alice_secret", "application/json", jsonBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("JSON exchange: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var tres tokenResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &tres)
	if tres.AccessToken != "gt_alice_secret" {
		t.Fatalf("JSON exchange token = %q", tres.AccessToken)
	}
}

// ===== 7. 401 挑战头（资源服务器侧） =====

func TestOAuthChallengeHeader(t *testing.T) {
	env, err := auth.ParseTokens("alice=gt_alice_secret")
	if err != nil {
		t.Fatal(err)
	}
	resolver := auth.NewFirstResolver(env, auth.NewDBResolver(nil))
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := buildHTTPHandler([]string{}, resolver, mux)

	// 无 token → 401 + resource_metadata 绝对 URL（从 Host 回推）。
	req := httptest.NewRequest(http.MethodPost, "http://example.com:8781/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	wa := rec.Header().Get("WWW-Authenticate")
	want := `resource_metadata="http://example.com:8781/.well-known/oauth-protected-resource"`
	if !strings.Contains(wa, want) || !strings.Contains(wa, `realm="gametrace"`) {
		t.Fatalf("WWW-Authenticate = %q", wa)
	}

	// 反代部署：X-Forwarded-Proto 决定 scheme。
	req = httptest.NewRequest(http.MethodPost, "http://internal:8781/mcp", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	wa = rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(wa, `resource_metadata="https://internal:8781/.well-known/oauth-protected-resource"`) {
		t.Fatalf("X-Forwarded-Proto variant WWW-Authenticate = %q", wa)
	}

	// 有效 token → 200，无挑战头追加（done 标志不影响正常响应）。
	req = httptest.NewRequest(http.MethodPost, "http://example.com:8781/mcp", nil)
	req.Header.Set("Authorization", "Bearer gt_alice_secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed request status = %d", rec.Code)
	}

	// 匿名模式：无 401、无挑战头（包装空转）。
	anonResolver := auth.NewFirstResolver(&auth.StaticResolver{}, auth.NewDBResolver(nil))
	h2 := buildHTTPHandler([]string{}, anonResolver, mux)
	req = httptest.NewRequest(http.MethodPost, "http://example.com:8781/mcp", nil)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous status = %d, want 200", rec.Code)
	}
}

// challengeWriter 透传 Flush：包装后 handler 仍能断言 http.Flusher（SSE 依赖）。
func TestChallengeWriterFlush(t *testing.T) {
	flushed := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("Flusher assertion failed through challengeWriter")
		}
		f.Flush()
		flushed = true
	})
	h := oauthChallenge(inner)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !flushed {
		t.Fatal("flush not invoked")
	}
}
