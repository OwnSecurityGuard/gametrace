package plugin

import (
	"context"
	"testing"

	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
)

// TestFindFor_MatchesTransports 验证 registry 按插件声明的 transports 匹配协议：
// 声明 transports:[udp] 的插件可被 FindFor(owner,"udp") 命中，供 dispatcher
// 按抓包协议路由（见 SDK contract.yaml manifest.transports）；未声明的协议不误命中。
func TestFindFor_MatchesTransports(t *testing.T) {
	s := NewRegistryServer(10)
	defer s.Close()

	const udpManifest = `api_version: gt.decoder/v2
name: udp-decoder
protocol: test_proto
type: decoder
transports:
  - udp
`
	if _, err := s.Register(context.Background(), &pb.RegisterRequest{
		SocketPath: "unix:/nonexistent/udp.sock",
		Manifest:   []byte(udpManifest),
	}); err != nil {
		t.Fatalf("register udp decoder: %v", err)
	}

	if _, ok := s.FindFor("", "udp"); !ok {
		t.Fatal("FindFor(udp) should match plugin declaring transports:[udp]")
	}
	if _, ok := s.FindFor("", "tcp"); ok {
		t.Fatal("FindFor(tcp) must not match a udp-only plugin")
	}
}
