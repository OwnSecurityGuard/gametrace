package auth

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// authorizationKey 是读取凭证所用的 metadata key。
// gRPC 的 metadata 在写入时会把 key 规范成小写，所以这个小写常量本身就是大小写不敏感的读法。
const authorizationKey = "authorization"

// UnaryInterceptor 校验一元调用的凭证，并把身份注入传给 handler 的 context。
// 匿名模式下（resolver 未配置任何 token）直接放行并注入 local 身份。
func UnaryInterceptor(r Resolver) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		p, err := principalFromIncoming(r, ctx)
		if err != nil {
			return nil, err
		}
		return handler(WithPrincipal(ctx, p), req)
	}
}

// StreamInterceptor 校验流式调用的凭证。插件注册后的心跳走的就是流，
// 漏掉它等于给「顶替别人的插件」留了一扇后门。
func StreamInterceptor(r Resolver) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		p, err := principalFromIncoming(r, ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &principalStream{ServerStream: ss, ctx: WithPrincipal(ss.Context(), p)})
	}
}

// principalFromIncoming 从入站 metadata 取凭证并解析身份。
func principalFromIncoming(r Resolver, ctx context.Context) (*Principal, error) {
	var token string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, v := range md.Get(authorizationKey) {
			if t, ok := parseBearer(v); ok {
				token = t
				break
			}
		}
	}
	if p, ok := r.Resolve(token); ok {
		return p, nil
	}
	// 统一回 PermissionDenied 而不是 Unauthenticated：
	// 不区分「没带 token」和「token 不对」，避免探测出哪些 token 有效。
	return nil, status.Error(codes.PermissionDenied, "invalid or missing auth token")
}

// principalStream 覆写 Context，让下游 handler 能拿到注入的身份。
type principalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *principalStream) Context() context.Context { return s.ctx }

// ClientUnaryInterceptor 是客户端侧拦截器：读 ctx 中的原始 token
// （auth.WithToken 注入）并附加 authorization: Bearer <token> 出站 metadata。
// 无 token 时原样放行（匿名模式），服务端校验兜底。
// 让 HTTP 中间件解析出的凭证能跨协议透传到 gRPC 服务（如 gt-mcp → pipeline）。
func ClientUnaryInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(attachBearerToken(ctx), method, req, reply, cc, opts...)
	}
}

// ClientStreamInterceptor 同 ClientUnaryInterceptor，作用于流式 RPC
// （如 WatchPlugins 这类 server-stream 订阅）。
func ClientStreamInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(attachBearerToken(ctx), desc, cc, method, opts...)
	}
}

// attachBearerToken 把 ctx 中的 token 附加为出站 Bearer metadata。
func attachBearerToken(ctx context.Context) context.Context {
	token := TokenFrom(ctx)
	if token == "" {
		return ctx
	}
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.MD{}
	} else {
		md = md.Copy()
	}
	md.Set(authorizationKey, "Bearer "+token)
	return metadata.NewOutgoingContext(ctx, md)
}
