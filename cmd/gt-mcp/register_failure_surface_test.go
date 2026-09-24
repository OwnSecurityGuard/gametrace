package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"gametrace/pkg/auth"
	pb "gametrace/pkg/internalipc/proto"
)

func TestRegisterFailedVisibility(t *testing.T) {
	alice := &auth.Principal{Owner: "alice"}
	admin := &auth.Principal{Owner: "root", IsAdmin: true}
	cases := []struct {
		name string
		ev   pluginEventJSON
		sub  *auth.Principal
		has  bool
		want bool
	}{
		{"匿名事件全员可见", pluginEventJSON{Owner: ""}, nil, false, true},
		{"同 owner 可见", pluginEventJSON{Owner: "alice"}, alice, true, true},
		{"他人 owner 不可见", pluginEventJSON{Owner: "bob"}, alice, true, false},
		{"admin 全可见", pluginEventJSON{Owner: "bob"}, admin, true, true},
		{"匿名订阅者不见他人失败", pluginEventJSON{Owner: "bob"}, nil, false, false},
	}
	for _, c := range cases {
		if got := visibleToSubscriber(c.ev, c.sub, c.has); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestListRegisteredPluginsIncludesFailures(t *testing.T) {
	fc := &fakeCaptureClient{dbDir: t.TempDir(), recentFailures: []*pb.PluginFailure{{
		Name: "my-plug", Error: "connection refused", Owner: "alice", TimestampUnix: 1700000000,
	}}}
	m := &mcpCapture{pipelineClient: fc}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := m.handleListRegisteredPlugins(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(mcp.TextContent).Text
	for _, want := range []string{"recent_register_failures", "my-plug", "connection refused"} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %s: %s", want, text)
		}
	}
}

func TestLatestRegisterFailure(t *testing.T) {
	fc := &fakeCaptureClient{dbDir: t.TempDir(), recentFailures: []*pb.PluginFailure{{
		Name: "my-plug", Error: "connection refused", Owner: "alice", TimestampUnix: 1700000000,
	}}}
	m := &mcpCapture{pipelineClient: fc}

	f := m.latestRegisterFailure(context.Background(), "my-plug")
	if f == nil || f["name"] != "my-plug" || f["error"] != "connection refused" {
		t.Errorf("latestRegisterFailure mismatch: %+v", f)
	}
	if got := m.latestRegisterFailure(context.Background(), "other"); got != nil {
		t.Errorf("unmatched name should return nil, got %+v", got)
	}
}

// TestConnectEnvelopeContract 锁定 connect 的对外契约：无论内部走到哪一环，
// 用户只拿到一个结论（status=ready|failed），失败附断点 stage + 人话 reason +
// 可执行的 next —— 不能把 registered / online 这类机器中间态当结论。
func TestConnectEnvelopeContract(t *testing.T) {
	ready := connectEnvelope("plug", connectStageReady, "registered+online+manifest ok", "", nil)
	if ready["status"] != "ready" || ready["ok"] != true {
		t.Errorf("ready envelope = %+v, want status=ready ok=true", ready)
	}
	if _, has := ready["failure"]; has {
		t.Errorf("ready envelope must not carry failure: %+v", ready)
	}
	if next, ok := ready["next"].([]string); !ok || next == nil {
		t.Errorf("ready envelope next must be a non-nil list: %+v", ready["next"])
	}

	failed := connectEnvelope("plug", connectStageAuth, "registry rejected token", "registry_rejected_token", []string{"refresh token"})
	if failed["status"] != "failed" || failed["ok"] != false || failed["stage"] != connectStageAuth || failed["reason"] != "registry rejected token" {
		t.Errorf("failed envelope = %+v", failed)
	}
	failure, ok := failed["failure"].(map[string]any)
	if !ok || failure["code"] != "registry_rejected_token" {
		t.Errorf("failed envelope failure = %+v, want code registry_rejected_token", failed["failure"])
	}
}

// TestRegisterFailureStageClassifiesAuth 锁定「注册被拒」的断点归类：鉴权类报
// auth（去刷新 token），其余报 connection（去查网络/地址），两者给人的下一步
// 完全不同。
func TestRegisterFailureStageClassifiesAuth(t *testing.T) {
	for _, msg := range []string{
		"rpc error: code = PermissionDenied desc = invalid token",
		"Unauthenticated: missing credentials",
	} {
		stage, code, next := registerFailureStage(msg)
		if stage != connectStageAuth || code != "registry_rejected_token" || len(next) == 0 {
			t.Errorf("%q -> stage=%s code=%s next=%v, want auth/registry_rejected_token", msg, stage, code, next)
		}
	}
	for _, msg := range []string{"connection refused", "i/o timeout"} {
		stage, code, next := registerFailureStage(msg)
		if stage != connectStageConnection || len(next) == 0 {
			t.Errorf("%q -> stage=%s code=%s next=%v, want connection", msg, stage, code, next)
		}
	}
}
