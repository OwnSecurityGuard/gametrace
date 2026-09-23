package main

import (
	"context"
	"testing"

	"gametrace/pkg/plugin"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"google.golang.org/grpc"
)

// fakeDecoderServerMain 是最小化的解码器桩，经内存隧道挂在注册表上。
type fakeDecoderServerMain struct {
	pb.UnimplementedDecoderServer
}

func (fakeDecoderServerMain) DecodeV2(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]) error {
	return nil
}

// registerFakePlugin 向注册表注册一个具名解码插件（protocol 固定 tcp，便于验证按名区分）。
// 平台只支持隧道注册，测试与本进程内注册表之间用内存 Connect 流（无需 socket）。
func registerFakePlugin(t *testing.T, s *plugin.RegistryServer, name string) func() {
	t.Helper()
	// api_version 必须是 v2：manager 的 CheckManifestVersion 要求 major 与
	// ProtocolVersion 一致，写 v1 会在 Register 阶段直接被拒。
	manifest := "api_version: gt.decoder/v2\nname: " + name + "\nprotocol: tcp\ntype: decoder\n"
	_, stop, err := s.RegisterInProcessTunnel(context.Background(), []byte(manifest), fakeDecoderServerMain{})
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return stop
}

// TestCaptureTask_resolveDecoderClient 验证 GAP 2 修复：
// 会话按 t.plugin 名字精确路由到对应插件，多项目并行互不干扰；
// 未指定插件名时退化按 tcp 协议 hint；未知名返回 not-found。
func TestCaptureTask_resolveDecoderClient(t *testing.T) {
	s := plugin.NewRegistryServer(10)
	defer registerFakePlugin(t, s, "plugin-a")()
	defer registerFakePlugin(t, s, "plugin-b")()

	// 预取两个插件各自的 client 指针，用于断言"按名精确区分"
	ca, okA := s.FindByName("plugin-a")
	cb, okB := s.FindByName("plugin-b")
	if !okA || ca == nil || !okB || cb == nil {
		t.Fatal("precondition: plugin-a and plugin-b must be registered and resolvable")
	}

	// 1. 指定 plugin-a → 必须返回 A 的 client，且不能是 B 的 client
	taskA := &captureTask{registry: s, plugin: "plugin-a"}
	gotA, ok := taskA.resolveDecoderClient()
	if !ok || gotA != ca {
		t.Errorf("plugin-a routed incorrectly: got=%v want=%v ok=%v", gotA, ca, ok)
	}
	if gotA == cb {
		t.Errorf("plugin-a must NOT route to plugin-b's client")
	}

	// 2. 指定 plugin-b → 必须返回 B 的 client
	taskB := &captureTask{registry: s, plugin: "plugin-b"}
	gotB, ok := taskB.resolveDecoderClient()
	if !ok || gotB != cb {
		t.Errorf("plugin-b routed incorrectly: got=%v want=%v ok=%v", gotB, cb, ok)
	}

	// 3. 未指定插件名 → 退化按 tcp 协议 hint，返回一个非 nil 的在线解码器
	taskEmpty := &captureTask{registry: s, plugin: ""}
	if c, ok := taskEmpty.resolveDecoderClient(); !ok || c == nil {
		t.Errorf("empty plugin should fall back to tcp decoder, got=%v ok=%v", c, ok)
	}

	// 4. 未知名 → not-found
	taskZ := &captureTask{registry: s, plugin: "plugin-z"}
	if _, ok := taskZ.resolveDecoderClient(); ok {
		t.Errorf("unknown plugin name should not resolve")
	}
}
