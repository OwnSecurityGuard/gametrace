package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"google.golang.org/grpc"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
)

func (f *fakeCaptureClient) GetRegistryAddr(ctx context.Context, in *pb.GetRegistryAddrRequest, _ ...grpc.CallOption) (*pb.GetRegistryAddrResponse, error) {
	return &pb.GetRegistryAddrResponse{RegistryAddr: ":9091"}, nil
}

func envResultText(t *testing.T, m *mcpCapture, ctx context.Context, args map[string]any) string {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := m.handleGetPluginEnv(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(mcp.TextContent).Text
}

func TestGetPluginEnvAuthenticatedCaller(t *testing.T) {
	t.Setenv("GT_PUBLIC_HOST", "192.168.31.87")
	fc := &fakeCaptureClient{dbDir: t.TempDir()}
	m := &mcpCapture{pipelineClient: fc, tokensByOwner: map[string]string{"alice": "gt_tok_alice"}}

	text := envResultText(t, m, auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "alice"}), map[string]any{})
	for _, want := range []string{
		`"registry_addr":"192.168.31.87:9091"`,
		`"auth_token":"gt_tok_alice"`,
		`"token_source":"env"`,
		"GT_REGISTRY_ADDR=192.168.31.87:9091",
		"GT_AUTH_TOKEN=gt_tok_alice",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %s: %s", want, text)
		}
	}
}

func TestGetPluginEnvAnonymous(t *testing.T) {
	t.Setenv("GT_PUBLIC_HOST", "192.168.31.87")
	fc := &fakeCaptureClient{dbDir: t.TempDir()}
	m := &mcpCapture{pipelineClient: fc}

	text := envResultText(t, m, context.Background(), map[string]any{})
	if !strings.Contains(text, `"token_source":"anonymous"`) || !strings.Contains(text, `"auth_token":""`) {
		t.Errorf("anonymous caller should get empty token: %s", text)
	}
}

func TestGetPluginEnvHostOverride(t *testing.T) {
	// 不设 GT_PUBLIC_HOST：host 参数（跨机部署时插件所在机器视角）生效
	fc := &fakeCaptureClient{dbDir: t.TempDir()}
	m := &mcpCapture{pipelineClient: fc}

	text := envResultText(t, m, context.Background(), map[string]any{"host": "10.0.0.9"})
	if !strings.Contains(text, `"registry_addr":"10.0.0.9:9091"`) {
		t.Errorf("host override not applied: %s", text)
	}
}
