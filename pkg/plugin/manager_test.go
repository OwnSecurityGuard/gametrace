package plugin

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// registerFakeDecoder 把 manifest 注册成隧道插件并绑定一条 noop 隧道，
// 返回 instance_id 与停止函数。替代已删除的非隧道 startFakeDecoder：
// 平台现在只认隧道注册，Register 分配 instance_id，再经 Connect 精确绑定才在线。
// ctx 须携带与 Register 一致的属主（ownerCtx），owner 作用域才一致。
func registerFakeDecoder(t *testing.T, s *RegistryServer, ctx context.Context, manifest []byte) (string, func()) {
	t.Helper()
	resp, err := s.Register(ctx, &pb.RegisterRequest{Manifest: manifest})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	p := newTunnelHubPipeFor(ctx, resp.GetInstanceId())
	go func() { _ = s.tunnelHub.Connect(hubEnd{p}) }()
	waitTunnelBound(t, s, resp.GetInstanceId())
	return resp.GetInstanceId(), p.close
}

const testManifest = `api_version: gt.decoder/v2
name: test-decoder
protocol: test_proto
type: decoder
hints:
  - tcp
  - "port:7000"
`

// TestRegister_RejectsBadSemanticDeclaration 验证注册期语义校验：
// semantic_rules 声明非法（未知 effect 类型）必须被 PluginChecker.Check 拦下，
// 且发生在拨号验证之前（无需真实 decoder socket 即可断言）。
func TestRegister_RejectsBadSemanticDeclaration(t *testing.T) {
	bad := `api_version: gt.decoder/v2
name: bad-schema-decoder
protocol: test_proto
type: decoder
semantic_rules:
  - id: game.bogus
    when:
      - { path: type, op: exists }
    effect: { type: not_a_real_effect }
`
	s := NewRegistryServer(10)
	_, err := s.Register(context.Background(), &pb.RegisterRequest{
		Manifest: []byte(bad),
	})
	if err == nil {
		t.Fatal("Register should reject a manifest with an invalid schema field type")
	}
	if !strings.Contains(err.Error(), "semantic contract check failed") {
		t.Fatalf("error %q should be a semantic contract rejection", err.Error())
	}
}

// TestRegister_AcceptsSemanticDeclaration 验证合法的四层声明能通过注册校验：
// 语义层不拦截（Register 会先做 Decode 地址可达性探测，需真实监听 socket），
// 注册成功并返回 instance_id。
func TestRegister_AcceptsSemanticDeclaration(t *testing.T) {
	good := `api_version: gt.decoder/v2
name: good-schema-decoder
protocol: test_proto
type: decoder
capabilities:
  decode: true
  schema: true
schemas:
  - id: test.player.v1
    version: 1
    strict: true
    fields:
      hp: { type: uint32, semantic: health, unit: hp, aggregatable: true }
`
	s := NewRegistryServer(10)
	_, stop := registerFakeDecoder(t, s, context.Background(), []byte(good))
	defer stop()
}

// TestRegistryServer_RegisterAndLifecycle 覆盖 Register 后的完整生命周期：
// Find（按 protocol / hint / 未知）、Heartbeat、List、CheckOffline、GetPluginManifest、Deregister。
func TestRegistryServer_RegisterAndLifecycle(t *testing.T) {
	s := NewRegistryServer(10)

	id, stop := registerFakeDecoder(t, s, context.Background(), []byte(testManifest))
	defer stop()

	if id == "" {
		t.Fatalf("expected non-empty instance_id")
	}

	// Find by protocol
	if client, ok := s.Find("test_proto"); !ok || client == nil {
		t.Errorf("Find by protocol failed: ok=%v client=%v", ok, client)
	}
	// Find by hint
	if _, ok := s.Find("port:7000"); !ok {
		t.Errorf("Find by hint port:7000 failed")
	}
	// Find by unknown must not match
	if _, ok := s.Find("nope"); ok {
		t.Errorf("Find by unknown should not match")
	}

	// Heartbeat 刷新 LastHeartbeat
	if _, err := s.Heartbeat(context.Background(), &pb.HeartbeatRequest{InstanceId: id}); err != nil {
		t.Errorf("Heartbeat: %v", err)
	}

	// List 应含 1 个在线插件
	if list := s.List(); len(list) != 1 || !list[0].Online {
		t.Errorf("List = %+v, want 1 online", list)
	}

	// 回填 LastHeartbeat 到过去，CheckOffline 应将其标记下线
	// （Windows 下 time.Now() 分辨率约 15ms，不能用亚毫秒超时断言）
	for _, rp := range s.plugins {
		rp.LastHeartbeat = time.Now().Add(-time.Minute)
	}
	s.CheckOffline(time.Second)
	if list := s.List(); len(list) != 1 || list[0].Online {
		t.Errorf("CheckOffline should mark offline, got %+v", list)
	}

	// GetPluginManifest 返回非空 bytes
	mani, err := s.GetPluginManifest("test-decoder")
	if err != nil {
		t.Fatalf("GetPluginManifest: %v", err)
	}
	if len(mani) == 0 {
		t.Errorf("GetPluginManifest returned empty")
	}

	// Deregister 后不再可被 Find 命中
	if _, err := s.Deregister(context.Background(), &pb.DeregisterRequest{InstanceId: id}); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if _, ok := s.Find("test_proto"); ok {
		t.Errorf("after Deregister, Find should not match")
	}
}

// TestRegistryServer_RegisterInvalidManifest 验证非法 manifest 在 parse/validate/version 阶段即报错。
func TestRegistryServer_RegisterInvalidManifest(t *testing.T) {
	s := NewRegistryServer(10)
	// api_version 格式非法
	_, err := s.Register(context.Background(), &pb.RegisterRequest{
		Manifest: []byte("api_version: bad\nname: x\nprotocol: p\ntype: decoder\n"),
	})
	if err == nil {
		t.Fatal("expected error for invalid api_version, got nil")
	}
}

// TestRegistryServer_RegisterVersionMismatch 验证 major 版本不匹配时注册被拒绝。
func TestRegistryServer_RegisterVersionMismatch(t *testing.T) {
	s := NewRegistryServer(10)
	mani := "api_version: gt.decoder/v1\nname: x\nprotocol: p\ntype: decoder\n"
	_, err := s.Register(context.Background(), &pb.RegisterRequest{
		Manifest: []byte(mani),
	})
	if err == nil {
		t.Fatal("expected version-mismatch error, got nil")
	}
}

// TestRegistryServer_FindByName 验证按插件名精确路由（GAP 2 核心能力）：
// 同协议（都声明 tcp）的两个插件可被按名区分，A 会话只命中 A、B 会话只命中 B，
// 未知名报 not-found，注销后名查失效，协议 hint 退化路径仍可用。
func TestRegistryServer_FindByName(t *testing.T) {
	const (
		maniA = "api_version: gt.decoder/v2\nname: a-decoder\nprotocol: tcp\ntype: decoder\n"
		maniB = "api_version: gt.decoder/v2\nname: b-decoder\nprotocol: tcp\ntype: decoder\n"
	)

	s := NewRegistryServer(10)
	idA, stopA := registerFakeDecoder(t, s, context.Background(), []byte(maniA))
	defer stopA()
	idB, stopB := registerFakeDecoder(t, s, context.Background(), []byte(maniB))
	defer stopB()

	ca, okA := s.FindByName("a-decoder")
	if !okA || ca == nil {
		t.Fatalf("FindByName a-decoder failed: ok=%v client=%v", okA, ca)
	}
	cb, okB := s.FindByName("b-decoder")
	if !okB || cb == nil {
		t.Fatalf("FindByName b-decoder failed: ok=%v client=%v", okB, cb)
	}
	// 同名解析必须稳定返回同一 client，且与另一插件不同
	if ca2, ok2 := s.FindByName("a-decoder"); !ok2 || ca2 != ca {
		t.Errorf("FindByName a-decoder should return stable client: ok=%v client=%v", ok2, ca2)
	}
	if ca == cb {
		t.Error("a-decoder and b-decoder must resolve to distinct clients")
	}

	// 未知名应返回 not-found
	if _, ok := s.FindByName("missing"); ok {
		t.Error("FindByName missing should return not-found")
	}

	// 协议 hint 退化路径：Find("tcp") 仍能命中（两个插件之一）
	if c, ok := s.Find("tcp"); !ok || c == nil {
		t.Errorf("Find tcp fallback failed: ok=%v client=%v", ok, c)
	}

	// 注销 b-decoder 后：名查失效，a-decoder 不受影响
	if _, err := s.Deregister(context.Background(), &pb.DeregisterRequest{InstanceId: idB}); err != nil {
		t.Fatalf("deregister b-decoder: %v", err)
	}
	if _, ok := s.FindByName("b-decoder"); ok {
		t.Error("FindByName b-decoder should fail after deregister")
	}
	if _, ok := s.FindByName("a-decoder"); !ok {
		t.Error("a-decoder should still resolve after b-decoder deregistered")
	}
	_ = idA
}
