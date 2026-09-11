package main

// user_mgmt_test.go — 自助注册用户管理的端到端测试：
// list_users / revoke_user 的 admin 门禁与撤销语义（users 表）。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"google.golang.org/grpc"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
	"gametrace/pkg/store"
)

// fakeMgmtClient 覆写 StartCapture / GetRegistryAddr，其余走 nil 嵌入（不会调用）。
type fakeMgmtClient struct {
	pb.CaptureControlClient
	startReq   *pb.StartCaptureRequest
	registryQA string
}

func (f *fakeMgmtClient) StartCapture(_ context.Context, in *pb.StartCaptureRequest, _ ...grpc.CallOption) (*pb.StartCaptureResponse, error) {
	f.startReq = in
	return &pb.StartCaptureResponse{SessionId: "s-mgmt-1", DbPath: "E:/tmp/capture.sqlite"}, nil
}

func (f *fakeMgmtClient) GetRegistryAddr(_ context.Context, _ *pb.GetRegistryAddrRequest, _ ...grpc.CallOption) (*pb.GetRegistryAddrResponse, error) {
	return &pb.GetRegistryAddrResponse{RegistryAddr: f.registryQA}, nil
}

func ctxOwnerAdmin(owner string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Owner: owner, IsAdmin: true})
}

func reqWith(kv ...string) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	args := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		args[kv[i]] = kv[i+1]
	}
	req.Params.Arguments = args
	return req
}

// newUserMgmtMCP 构造带 users/accessCodes/projectStore/pipeline 桩的完整 mcpCapture。
func newUserMgmtMCP(t *testing.T) (*mcpCapture, *userStore, *fakeMgmtClient) {
	t.Helper()
	cs, err := store.NewControlStore(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	ps := newProjectStoreDB(cs.DB())
	if err := ps.Init(); err != nil {
		t.Fatal(err)
	}
	us := newUserStore(cs.DB())
	if err := us.Init(); err != nil {
		t.Fatal(err)
	}
	ac := newAccessCodeStore(cs.DB())
	if err := ac.Init(); err != nil {
		t.Fatal(err)
	}
	fc := &fakeMgmtClient{}
	m := &mcpCapture{
		projects: ps, users: us, authz: newProjectAuthorizer(ps),
		accessCodes: ac, tokensByOwner: map[string]string{"bob": "tok-bob"},
		pipelineClient: fc, sessionMgr: newSessionManager(t.TempDir()),
	}
	return m, us, fc
}

// TestRevokeUserHandlers 验证 list_users / revoke_user 的 admin 门禁与撤销语义。
func TestRevokeUserHandlers(t *testing.T) {
	m, us, _ := newUserMgmtMCP(t)
	ctx := context.Background()
	if _, _, err := us.CreateUser(ctx, "dave", "bob"); err != nil {
		t.Fatal(err)
	}

	// 非 admin 调 list_users / revoke_user → 拒绝。
	for _, tc := range []struct {
		name string
		call func() (any, error)
	}{
		{"list_users", func() (any, error) { return m.handleListUsers(ctxOwner("bob"), mcp.CallToolRequest{}) }},
		{"revoke_user", func() (any, error) { return m.handleRevokeUser(ctxOwner("bob"), reqWith("owner", "dave")) }},
	} {
		res, err := tc.call()
		if err != nil {
			t.Fatal(err)
		}
		if text := resultText(t, res.(*mcp.CallToolResult)); !strings.Contains(text, "global admin") {
			t.Errorf("%s non-admin must be forbidden: %s", tc.name, text)
		}
	}

	// admin 撤销 dave → ok；再撤 → not found。
	res, err := m.handleRevokeUser(ctxOwnerAdmin("root"), reqWith("owner", "dave"))
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, res); strings.Contains(text, `"ok":false`) {
		t.Fatalf("admin revoke failed: %s", text)
	}
	if n := countUsers(t, us, "dave"); n != 0 {
		t.Fatalf("dave rows after revoke: %d", n)
	}
	res2, _ := m.handleRevokeUser(ctxOwnerAdmin("root"), reqWith("owner", "dave"))
	if text := resultText(t, res2); !strings.Contains(text, "not found") {
		t.Errorf("revoke twice must be not found: %s", text)
	}

	// admin 不能撤销自己。
	res3, _ := m.handleRevokeUser(ctxOwnerAdmin("root"), reqWith("owner", "root"))
	if text := resultText(t, res3); !strings.Contains(text, "cannot revoke yourself") {
		t.Errorf("self revoke must be rejected: %s", text)
	}
}

func countUsers(t *testing.T, us *userStore, owner string) int {
	t.Helper()
	var n int
	if err := us.db.QueryRow(`SELECT COUNT(*) FROM users WHERE owner=?`, owner).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
