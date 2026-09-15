package main

import (
	"context"
	"testing"

	"gametrace/pkg/auth"
	"gametrace/pkg/plugin"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

const scopeManifest = "api_version: gt.decoder/v2\nname: scope-decoder\nprotocol: tcp\ntype: decoder\n"

func failRegister(s *pipelineService, ctx context.Context, socketPath string) {
	if _, err := s.registry.Register(ctx, &pb.RegisterRequest{
		SocketPath: socketPath, Manifest: []byte(scopeManifest),
	}); err == nil {
		panic("register should fail: " + socketPath)
	}
}

// owner 作用域与 ListPlugins 一致：非 admin 见匿名+自己的；admin 全见。
func TestListRegisterFailuresOwnerScope(t *testing.T) {
	s := &pipelineService{registry: plugin.NewRegistryServer(10)}
	failRegister(s, auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"}),
		"unix:/nonexistent/bob.sock")
	failRegister(s, context.Background(), "unix:/nonexistent/anon.sock")

	// alice 非 admin：只见匿名
	got, err := s.ListRegisterFailures(auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "alice"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Owner != "" {
		t.Fatalf("alice should see only anonymous failure, got %+v", got)
	}

	// bob：自己的 + 匿名
	got, err = s.ListRegisterFailures(auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("bob should see own+anonymous failures, got %d", len(got))
	}

	// admin：全部
	got, err = s.ListRegisterFailures(auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "root", IsAdmin: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("admin should see all failures, got %d", len(got))
	}
}
