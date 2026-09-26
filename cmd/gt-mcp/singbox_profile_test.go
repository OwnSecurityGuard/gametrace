package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
)

// fakeLeaseClient 只实现 ListProxyLeases，并记录调用方 ctx 里携带的原始 token。
//
// 为什么记 token 而不是出站 metadata：Bearer 头是 auth.ClientUnaryInterceptor
// 在真实 gRPC 连接层加的，桩函数拿不到；但拦截器的唯一输入就是 ctx 里的
// auth.TokenFrom，所以「ctx 里有 token」等价于「出站会带凭证」。
type fakeLeaseClient struct {
	pb.CaptureControlClient
	leases   []*pb.ProxyLeaseState
	gotToken string
	err      error
}

func (f *fakeLeaseClient) ListProxyLeases(ctx context.Context, _ *pb.ListProxyLeasesRequest, _ ...grpc.CallOption) (*pb.ListProxyLeasesResponse, error) {
	f.gotToken = auth.TokenFrom(ctx)
	if f.err != nil {
		return nil, f.err
	}
	return &pb.ListProxyLeasesResponse{Leases: f.leases}, nil
}

// TestHandleSingboxProfileCarriesServiceToken 锁住回归点：
// /singbox/profile 挂在鉴权链之外（SFA 无法带 Bearer 头），但 handler 内部要经
// pipeline 的 CaptureControl 反查租约；出站若不补凭证，pipeline 的 auth 拦截器
// 直接 PermissionDenied，端点表现为 503「pipeline unavailable」。
func TestHandleSingboxProfileCarriesServiceToken(t *testing.T) {
	fake := &fakeLeaseClient{leases: []*pb.ProxyLeaseState{{LeaseId: "L1", AgentListenPort: 12100}}}
	m := &mcpCapture{
		pipelineClient: fake,
		httpAddr:       ":8781",
		tokensByOwner:  map[string]string{"alice": "gt_tok_alice"},
		envResolver: auth.NewStaticResolver(map[string]auth.Principal{
			"gt_tok_alice": {Owner: "alice", IsAdmin: true},
		}),
	}

	req := httptest.NewRequest(http.MethodGet, "http://192.168.31.87:18781/singbox/profile?port=12100", nil)
	rec := httptest.NewRecorder()
	m.handleSingboxProfile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if fake.gotToken != "gt_tok_alice" {
		t.Fatalf("outbound token = %q, want %q（鉴权豁免端点的内部调用必须补服务凭证）", fake.gotToken, "gt_tok_alice")
	}

	var cfg struct {
		Outbounds []struct {
			Tag        string `json:"tag"`
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("profile 不是合法 JSON: %v", err)
	}
	if len(cfg.Outbounds) == 0 {
		t.Fatal("profile 缺少 outbounds")
	}
	proxy := cfg.Outbounds[0]
	if proxy.Server != "192.168.31.87" || proxy.ServerPort != 12100 {
		t.Fatalf("proxy outbound = %s:%d, want 192.168.31.87:12100", proxy.Server, proxy.ServerPort)
	}
}

// TestHandleSingboxProfileUnknownPort 陈旧二维码（租约已释放）必须 404，
// 且这个判定同样要走过带凭证的 pipeline 通道。
func TestHandleSingboxProfileUnknownPort(t *testing.T) {
	fake := &fakeLeaseClient{leases: []*pb.ProxyLeaseState{{LeaseId: "L1", AgentListenPort: 12100}}}
	m := &mcpCapture{
		pipelineClient: fake,
		httpAddr:       ":8781",
		tokensByOwner:  map[string]string{"alice": "gt_tok_alice"},
		envResolver: auth.NewStaticResolver(map[string]auth.Principal{
			"gt_tok_alice": {Owner: "alice", IsAdmin: true},
		}),
	}
	req := httptest.NewRequest(http.MethodGet, "http://192.168.31.87:18781/singbox/profile?port=12199", nil)
	rec := httptest.NewRecorder()
	m.handleSingboxProfile(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404（端口无活跃租约）", rec.Code)
	}
}

// TestServiceTokenPrefersAdmin 服务通道需要跨 owner 可见（非 admin 会被 pipeline
// 的 owner 作用域过滤，表现为别人的二维码恒 404、别人的插件事件不广播），
// 所以有 admin token 时必须优先返回它，而不是字典序首个。
func TestServiceTokenPrefersAdmin(t *testing.T) {
	m := &mcpCapture{
		// alice 字典序在前但不是 admin；zoe 是 admin。
		tokensByOwner: map[string]string{"alice": "tok-alice", "zoe": "tok-zoe"},
		envResolver: auth.NewStaticResolver(map[string]auth.Principal{
			"tok-alice": {Owner: "alice"},
			"tok-zoe":   {Owner: "zoe", IsAdmin: true},
		}),
	}
	if got := m.serviceToken(); got != "tok-zoe" {
		t.Fatalf("serviceToken() = %q, want admin token %q", got, "tok-zoe")
	}
}

// TestServiceTokenFallbackWithoutAdmin 没有 admin token 时退回原行为
//（owner 字典序首个非空），匿名模式返回空串。
func TestServiceTokenFallbackWithoutAdmin(t *testing.T) {
	m := &mcpCapture{
		tokensByOwner: map[string]string{"bob": "tok-bob", "alice": "tok-alice"},
		envResolver: auth.NewStaticResolver(map[string]auth.Principal{
			"tok-alice": {Owner: "alice"},
			"tok-bob":   {Owner: "bob"},
		}),
	}
	if got := m.serviceToken(); got != "tok-alice" {
		t.Fatalf("serviceToken() = %q, want 字典序首个 %q", got, "tok-alice")
	}

	anon := &mcpCapture{envResolver: auth.NewStaticResolver(nil)}
	if got := anon.serviceToken(); got != "" {
		t.Fatalf("匿名模式 serviceToken() = %q, want 空串", got)
	}
}

// TestServiceTokenNilResolverIsSafe envResolver 尚未注入时（装配早期启动的
// WatchPlugins）不能 panic，退回「首个 token」即可。
func TestServiceTokenNilResolverIsSafe(t *testing.T) {
	m := &mcpCapture{tokensByOwner: map[string]string{"alice": "tok-alice"}}
	if got := m.serviceToken(); got != "tok-alice" {
		t.Fatalf("serviceToken() = %q, want %q", got, "tok-alice")
	}
}

// newOffsetCapture 构造一个「容器内 12100-12199 → 宿主 22100-22199，mcp 8781
// → 宿主 18781」的 capture，模拟 docker compose 非恒等端口映射。
func newOffsetCapture(t *testing.T, fake pb.CaptureControlClient) *mcpCapture {
	t.Helper()
	old := lanIPOverride
	t.Cleanup(func() { lanIPOverride = old })
	lanIPOverride = "192.168.31.87"
	return &mcpCapture{
		pipelineClient:  fake,
		httpAddr:        ":8781",
		publicMCPPort:   18781,
		proxyPortOffset: 10000,
		tokensByOwner:   map[string]string{"alice": "gt_tok_alice"},
		envResolver: auth.NewStaticResolver(map[string]auth.Principal{
			"gt_tok_alice": {Owner: "alice", IsAdmin: true},
		}),
	}
}

// TestLeaseJSONUsesPublicPorts 二维码必须写宿主对外端口，不能写容器内端口。
func TestLeaseJSONUsesPublicPorts(t *testing.T) {
	m := newOffsetCapture(t, &fakeLeaseClient{})
	lease := m.leaseToJSON(&pb.ProxyLeaseState{LeaseId: "L1", AgentListenPort: 12100})

	if lease.AgentListenPort != 12100 {
		t.Fatalf("AgentListenPort = %d, want 12100（容器内真实监听端口应保留）", lease.AgentListenPort)
	}
	if lease.PublicPort != 22100 {
		t.Fatalf("PublicPort = %d, want 22100（12100 + offset 10000）", lease.PublicPort)
	}
	if lease.ConnectAddr != "192.168.31.87:22100" {
		t.Fatalf("ConnectAddr = %q, want 192.168.31.87:22100", lease.ConnectAddr)
	}
	wantURL := url.QueryEscape("http://192.168.31.87:18781/singbox/profile?port=22100")
	if !strings.Contains(lease.SingboxURI, wantURL) {
		t.Fatalf("SingboxURI = %q, 应内嵌 %s（宿主 mcp 端口 18781 + 对外端口 22100）", lease.SingboxURI, wantURL)
	}
}

// TestLeaseJSONIdentityMappingUnchanged 恒等映射（默认部署）行为必须与改动前一致。
func TestLeaseJSONIdentityMappingUnchanged(t *testing.T) {
	old := lanIPOverride
	t.Cleanup(func() { lanIPOverride = old })
	lanIPOverride = "192.168.31.87"
	m := &mcpCapture{httpAddr: ":8781"}

	lease := m.leaseToJSON(&pb.ProxyLeaseState{LeaseId: "L1", AgentListenPort: 12100})
	if lease.PublicPort != 12100 || lease.ConnectAddr != "192.168.31.87:12100" {
		t.Fatalf("恒等映射下 PublicPort=%d ConnectAddr=%q, want 12100 / 192.168.31.87:12100",
			lease.PublicPort, lease.ConnectAddr)
	}
	if !strings.Contains(lease.SingboxURI, url.QueryEscape("http://192.168.31.87:8781/singbox/profile?port=12100")) {
		t.Fatalf("恒等映射下 SingboxURI = %q, 应退回自身监听端口 8781", lease.SingboxURI)
	}
}

// TestHandleSingboxProfileWithPortOffset 二维码带的是对外端口（22100），
// 校验要反解回容器内端口（12100）才能命中租约，而回给手机的配置里必须是
// 对外端口——否则手机连不上。
func TestHandleSingboxProfileWithPortOffset(t *testing.T) {
	fake := &fakeLeaseClient{leases: []*pb.ProxyLeaseState{{LeaseId: "L1", AgentListenPort: 12100}}}
	m := newOffsetCapture(t, fake)

	req := httptest.NewRequest(http.MethodGet, "http://192.168.31.87:18781/singbox/profile?port=22100", nil)
	rec := httptest.NewRecorder()
	m.handleSingboxProfile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}

	var cfg struct {
		Outbounds []struct {
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("profile 不是合法 JSON: %v", err)
	}
	if len(cfg.Outbounds) == 0 {
		t.Fatal("profile 缺少 outbounds")
	}
	if got := cfg.Outbounds[0].ServerPort; got != 22100 {
		t.Fatalf("server_port = %d, want 22100（必须是宿主对外端口，不是容器内 12100）", got)
	}

	// 未映射到任何租约的对外端口（22199 → 12199，租约在 12100）应 404。
	miss := httptest.NewRequest(http.MethodGet, "http://192.168.31.87:18781/singbox/profile?port=22199", nil)
	missRec := httptest.NewRecorder()
	m.handleSingboxProfile(missRec, miss)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", missRec.Code)
	}
}
