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

// pluginOwnerFakeClient 是 pb.CaptureControlClient 的轻量桩：仅覆写
// GetPluginManifest（成员添加插件时的归属校验用），其余方法由嵌入的
// nil 接口提供（测试不会调用）。manifestErr 非 nil 时返回该错误（=插件不存在/无权）。
type pluginOwnerFakeClient struct {
	pb.CaptureControlClient
	manifestReq *pb.GetPluginManifestRequest
	manifestErr error
}

func (f *pluginOwnerFakeClient) GetPluginManifest(ctx context.Context, in *pb.GetPluginManifestRequest, _ ...grpc.CallOption) (*pb.GetPluginManifestResponse, error) {
	f.manifestReq = in
	if f.manifestErr != nil {
		return nil, f.manifestErr
	}
	return &pb.GetPluginManifestResponse{Name: in.GetName(), Manifest: []byte("name: " + in.GetName())}, nil
}

// projectWithMember 构造 owner=bob、成员 carol 的项目，返回项目 ID。
func projectWithMember(t *testing.T, m *mcpCapture) string {
	t.Helper()
	ctx := context.Background()
	p := &project{
		ID: "p1", Name: "P", CreatedBy: "bob", Owner: "bob",
		Plugins: []projectPlugin{}, Rules: []projectRule{}, Members: []projectMember{},
		CreatedAt: "2026-09-13T00:00:00Z", UpdatedAt: "2026-09-13T00:00:00Z",
	}
	if err := m.projects.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := m.projects.AddMember(ctx, p.ID, projectMember{User: "carol", Role: roleMember}); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func principalCtx(owner string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Owner: owner})
}

// TestAddProjectPlugin_AdminAnyPlugin：项目 admin（owner）可添加任意插件名，
// 即使该插件不在调用者名下（admin 分支不查注册表），条目 owner 记录为 admin。
func TestAddProjectPlugin_AdminAnyPlugin(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	m.pipelineClient = &pluginOwnerFakeClient{manifestErr: context.DeadlineExceeded} // admin 分支不应触达

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id": pid, "name": "godot-ecs"}
	res, err := m.handleAddProjectPlugin(principalCtx("bob"), req)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, `"added":true`) {
		t.Fatalf("expected added:true, got: %s", text)
	}
	got, err := m.projects.Get(context.Background(), pid)
	if err != nil || len(got.Plugins) != 1 {
		t.Fatalf("plugins = %+v, err=%v", got.Plugins, err)
	}
	if got.Plugins[0].Owner != "bob" {
		t.Fatalf("entry owner = %q, want bob", got.Plugins[0].Owner)
	}
}

// TestAddProjectPlugin_MemberOwnPlugin：普通成员可添加自己注册的插件，
// 校验请求带 owner=调用者，条目 owner 记录为调用者。
func TestAddProjectPlugin_MemberOwnPlugin(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	fc := &pluginOwnerFakeClient{}
	m.pipelineClient = fc

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id": pid, "name": "my-http"}
	res, err := m.handleAddProjectPlugin(principalCtx("carol"), req)
	if err != nil {
		t.Fatal(err)
	}
	if fc.manifestReq == nil || fc.manifestReq.GetOwner() != "carol" || fc.manifestReq.GetName() != "my-http" {
		t.Fatalf("ownership check not forwarded correctly: %+v", fc.manifestReq)
	}
	text := resultText(t, res)
	if !strings.Contains(text, `"added":true`) {
		t.Fatalf("expected added:true, got: %s", text)
	}
	got, err := m.projects.Get(context.Background(), pid)
	if err != nil || len(got.Plugins) != 1 {
		t.Fatalf("plugins = %+v, err=%v", got.Plugins, err)
	}
	if got.Plugins[0].Owner != "carol" {
		t.Fatalf("entry owner = %q, want carol", got.Plugins[0].Owner)
	}
}

// TestAddProjectPlugin_MemberNotOwnRejected：成员添加他人插件被拒（注册表查询失败）。
func TestAddProjectPlugin_MemberNotOwnRejected(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	m.pipelineClient = &pluginOwnerFakeClient{manifestErr: context.DeadlineExceeded}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id": pid, "name": "alice-http"}
	res, err := m.handleAddProjectPlugin(principalCtx("carol"), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, res), "can only add plugins you own") {
		t.Fatalf("expected ownership rejection, got: %s", resultText(t, res))
	}
}

// TestAddProjectPlugin_Idempotent：重复添加同名插件幂等（不报错、列表唯一）。
func TestAddProjectPlugin_Idempotent(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	m.pipelineClient = &pluginOwnerFakeClient{}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id": pid, "name": "my-http"}
	if _, err := m.handleAddProjectPlugin(principalCtx("carol"), req); err != nil {
		t.Fatal(err)
	}
	res, err := m.handleAddProjectPlugin(principalCtx("carol"), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, res), `"added":false`) {
		t.Fatalf("expected added:false on duplicate, got: %s", resultText(t, res))
	}
	got, err := m.projects.Get(context.Background(), pid)
	if err != nil || len(got.Plugins) != 1 {
		t.Fatalf("plugins = %+v, err=%v", got.Plugins, err)
	}
}

// TestAddProjectPlugin_NonMemberRejected：与项目无关的用户被拒（不泄露项目存在性）。
func TestAddProjectPlugin_NonMemberRejected(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	m.pipelineClient = &pluginOwnerFakeClient{}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id": pid, "name": "my-http"}
	res, err := m.handleAddProjectPlugin(principalCtx("dave"), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, res), "not found") {
		t.Fatalf("expected not found for non-member, got: %s", resultText(t, res))
	}
}

// TestRemoveProjectPlugin_MemberOwn：成员可移除自己添加的条目。
func TestRemoveProjectPlugin_MemberOwn(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	m.pipelineClient = &pluginOwnerFakeClient{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id": pid, "name": "my-http"}
	if _, err := m.handleAddProjectPlugin(principalCtx("carol"), req); err != nil {
		t.Fatal(err)
	}

	rmReq := mcp.CallToolRequest{}
	rmReq.Params.Arguments = map[string]any{"project_id": pid, "id": "my-http"}
	if _, err := m.handleRemoveProjectPlugin(principalCtx("carol"), rmReq); err != nil {
		t.Fatal(err)
	}
	got, err := m.projects.Get(context.Background(), pid)
	if err != nil || len(got.Plugins) != 0 {
		t.Fatalf("plugins = %+v, err=%v", got.Plugins, err)
	}
}

// TestRemoveProjectPlugin_MemberOthersRejected：成员不能移除他人添加的条目。
func TestRemoveProjectPlugin_MemberOthersRejected(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	m.pipelineClient = &pluginOwnerFakeClient{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id": pid, "name": "http"}
	if _, err := m.handleAddProjectPlugin(principalCtx("bob"), req); err != nil {
		t.Fatal(err)
	}

	rmReq := mcp.CallToolRequest{}
	rmReq.Params.Arguments = map[string]any{"project_id": pid, "id": "http"}
	res, err := m.handleRemoveProjectPlugin(principalCtx("carol"), rmReq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, res), "can only remove plugins you added") {
		t.Fatalf("expected ownership rejection on remove, got: %s", resultText(t, res))
	}
}

// TestRemoveProjectPlugin_AdminAny：admin 可移除任意条目（含他人添加的）。
func TestRemoveProjectPlugin_AdminAny(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	m.pipelineClient = &pluginOwnerFakeClient{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id": pid, "name": "my-http"}
	if _, err := m.handleAddProjectPlugin(principalCtx("carol"), req); err != nil {
		t.Fatal(err)
	}

	rmReq := mcp.CallToolRequest{}
	rmReq.Params.Arguments = map[string]any{"project_id": pid, "id": "my-http"}
	if _, err := m.handleRemoveProjectPlugin(principalCtx("bob"), rmReq); err != nil {
		t.Fatal(err)
	}
	got, err := m.projects.Get(context.Background(), pid)
	if err != nil || len(got.Plugins) != 0 {
		t.Fatalf("plugins = %+v, err=%v", got.Plugins, err)
	}
}

// TestRemoveProjectPlugin_NotFound：移除不存在的条目报错。
func TestRemoveProjectPlugin_NotFound(t *testing.T) {
	m, _, _ := newUserMgmtMCP(t)
	pid := projectWithMember(t, m)
	rmReq := mcp.CallToolRequest{}
	rmReq.Params.Arguments = map[string]any{"project_id": pid, "id": "nope"}
	res, err := m.handleRemoveProjectPlugin(principalCtx("bob"), rmReq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, res), "not found in project") {
		t.Fatalf("expected entry-not-found, got: %s", resultText(t, res))
	}
}
