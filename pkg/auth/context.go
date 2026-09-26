package auth

import "context"

// principalKey 是私有类型，避免与其它包往 context 里塞的值撞 key。
type principalKey struct{}

// WithPrincipal 把身份注入 context，供下游业务读取。
// 传入 nil 等价于不注入，下游按「无身份」处理而不是 panic。
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom 取出身份。取不到时返回 (nil, false)。
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	if p == nil {
		return nil, false
	}
	return p, true
}

// tokenKey 是私有类型，避免与其它包往 context 里塞的值撞 key。
type tokenKey struct{}

// WithToken 把原始凭证 token 注入 context。
// 供出站侧（HTTP 中间件 → gRPC 客户端拦截器）跨协议透传凭证：
// HTTP 端解出 token 后传给 gRPC 客户端，客户端拦截器据此附加
// authorization: Bearer <token> 出站 metadata。匿名模式不注入（无 token）。
func WithToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, tokenKey{}, token)
}

// TokenFrom 取出 token；未注入或为空时返回空串。
func TokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(tokenKey{}).(string)
	return t
}

// OwnerFrom 取出 owner，取不到时返回空串。
// 空串是「无主」的显式约定：下游据此决定资源归属或拒绝服务。
func OwnerFrom(ctx context.Context) string {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return ""
	}
	return p.Owner
}
