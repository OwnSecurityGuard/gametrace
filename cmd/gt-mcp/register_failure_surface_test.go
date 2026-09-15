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
		Name: "my-plug", SocketPath: "127.0.0.1:61887", Error: "connection refused", Owner: "alice", TimestampUnix: 1700000000,
	}}}
	m := &mcpCapture{pipelineClient: fc}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := m.handleListRegisteredPlugins(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(mcp.TextContent).Text
	for _, want := range []string{"recent_register_failures", "my-plug", "127.0.0.1:61887", "connection refused"} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %s: %s", want, text)
		}
	}
}

func TestLatestRegisterFailure(t *testing.T) {
	fc := &fakeCaptureClient{dbDir: t.TempDir(), recentFailures: []*pb.PluginFailure{{
		Name: "my-plug", SocketPath: "127.0.0.1:61887", Error: "connection refused", Owner: "alice", TimestampUnix: 1700000000,
	}}}
	m := &mcpCapture{pipelineClient: fc}

	f := m.latestRegisterFailure(context.Background(), "my-plug")
	if f == nil || f["socket_path"] != "127.0.0.1:61887" || f["error"] != "connection refused" {
		t.Errorf("latestRegisterFailure mismatch: %+v", f)
	}
	if got := m.latestRegisterFailure(context.Background(), "other"); got != nil {
		t.Errorf("unmatched name should return nil, got %+v", got)
	}
}
