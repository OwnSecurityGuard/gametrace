package plugin

import (
	"context"
	"net"
	"testing"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"gametrace/pkg/auth"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// 本文件验证 main.go 的真实接线语义：RegistryServer 挂上 pkg/auth 拦截器后，
// token 模式下 Bearer owner 决定插件键 owner/name；匿名模式（不挂拦截器，
// 对应 main.go 中 authResolver.Required()==false 的分支）保持裸 name 键。
// 既有单测直接往 ctx 注入 Principal，绕过了拦截器链，盖不住这一层。

// startWiredRegistry 起一个完整的 PluginRegistry gRPC server（可选鉴权拦截器），
// 返回客户端与停止函数。wireAuth 模拟 main.go 的「配置了 token 才挂拦截器」分支。
func startWiredRegistry(t *testing.T, wireAuth bool, tokens string) (pb.PluginRegistryClient, *RegistryServer, func()) {
	t.Helper()
	srv := NewRegistryServer(10)
	var grpcSrv *grpc.Server
	if wireAuth {
		resolver, err := auth.ParseTokens(tokens)
		if err != nil {
			t.Fatalf("ParseTokens: %v", err)
		}
		if !resolver.Required() {
			t.Fatal("tokens 配置后 Required() 必须为 true")
		}
		grpcSrv = grpc.NewServer(
			grpc.ChainUnaryInterceptor(auth.UnaryInterceptor(resolver)),
			grpc.ChainStreamInterceptor(auth.StreamInterceptor(resolver)),
		)
	} else {
		grpcSrv = grpc.NewServer()
	}
	pb.RegisterPluginRegistryServer(grpcSrv, srv)

	lis := bufconn.Listen(1 << 16)
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	stop := func() {
		_ = conn.Close()
		grpcSrv.Stop()
		_ = lis.Close()
	}
	return pb.NewPluginRegistryClient(conn), srv, stop
}

// regWithToken 用给定 Bearer token（可为空 = 不带 metadata）走一次真实 RPC 注册，
// 返回分配到的 instance_id。
func regWithToken(t *testing.T, c pb.PluginRegistryClient, name, token string) (string, error) {
	t.Helper()
	ctx := context.Background()
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	resp, err := c.Register(ctx, &pb.RegisterRequest{
		Manifest: []byte("api_version: gt.decoder/v2\nname: " + name + "\nprotocol: test_proto\ntype: decoder\nhints:\n  - tcp\n"),
	})
	if err != nil {
		return "", err
	}
	return resp.GetInstanceId(), nil
}

// connectTunnel 从插件侧打开一条 Connect 隧道，把 instance_id 与 token（owner）带上，
// 等宿主 bindTunnelClient 把实例置为在线后返回。平台只认隧道注册（非隧道拨号已删除）：
// 真实 RPC 注册只分配 instance_id，必须再经 Connect 精确绑定插件才可被 Find 命中。
func connectTunnel(t *testing.T, c pb.PluginRegistryClient, token, instanceID string) pb.PluginRegistry_ConnectClient {
	t.Helper()
	ctx := context.Background()
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	ctx = metadata.AppendToOutgoingContext(ctx, sdk.TunnelInstanceIDKey, instanceID)
	stream, err := c.Connect(ctx)
	if err != nil {
		t.Fatalf("connect tunnel: %v", err)
	}
	return stream
}

func TestRegistryWiring_TokenModeOwnerScoping(t *testing.T) {
	c, registry, stop := startWiredRegistry(t, true, "alice=gt_a,bob=gt_b")
	defer stop()

	idA, err := regWithToken(t, c, "my-plugin", "gt_a")
	if err != nil {
		t.Fatalf("alice register: %v", err)
	}
	idB, err := regWithToken(t, c, "my-plugin", "gt_b")
	if err != nil {
		t.Fatalf("bob register: %v", err)
	}

	// 绑定隧道使插件在线（平台只认隧道注册）。
	streamA := connectTunnel(t, c, "gt_a", idA)
	defer streamA.CloseSend()
	streamB := connectTunnel(t, c, "gt_b", idB)
	defer streamB.CloseSend()
	waitTunnelBound(t, registry, idA)
	waitTunnelBound(t, registry, idB)

	// 各 owner 只能命中自己的实例，同名不互相顶替。
	if _, ok := registry.FindByNameFor("alice", "my-plugin"); !ok {
		t.Fatal("alice 应能找到 alice/my-plugin")
	}
	if _, ok := registry.FindByNameFor("bob", "my-plugin"); !ok {
		t.Fatal("bob 应能找到 bob/my-plugin")
	}
	for _, p := range registry.List() {
		if p.Owner != "alice" && p.Owner != "bob" {
			t.Fatalf("token 模式下不应存在非 alice/bob 的实例: %+v", p)
		}
	}

	// 错误 token 被拒。
	if _, err := regWithToken(t, c, "my-plugin", "gt_wrong"); err == nil {
		t.Fatal("错误 token 的 Register 应被拒绝")
	}
	// 无 token 也被拒。
	if _, err := regWithToken(t, c, "my-plugin", ""); err == nil {
		t.Fatal("无 token 的 Register 应被拒绝")
	}
}

func TestRegistryWiring_AnonymousModeBareName(t *testing.T) {
	// wireAuth=false 对应 main.go 匿名分支：不挂拦截器，无 Principal，
	// 插件键必须是裸 name（FindByNameFor("", ...) 能命中），行为与改造前一致。
	c, registry, stop := startWiredRegistry(t, false, "")
	defer stop()

	id, err := regWithToken(t, c, "my-plugin", "")
	if err != nil {
		t.Fatalf("anonymous register: %v", err)
	}
	// 绑定隧道使插件在线（平台只认隧道注册）。
	stream := connectTunnel(t, c, "", id)
	defer stream.CloseSend()
	waitTunnelBound(t, registry, id)

	if _, ok := registry.FindByNameFor("", "my-plugin"); !ok {
		t.Fatal("匿名模式下 FindByNameFor(\"\", name) 应命中裸 name 键")
	}
	for _, p := range registry.List() {
		if p.Owner != "" {
			t.Fatalf("匿名模式注册的插件 owner 必须为空串，实际 %q", p.Owner)
		}
	}
	// 心跳等后续调用同样无需 token。
	if _, err := c.Heartbeat(context.Background(), &pb.HeartbeatRequest{InstanceId: id}); err != nil {
		t.Fatalf("anonymous heartbeat: %v", err)
	}
}
