package main

import (
	"context"
	"testing"

	"gametrace/pkg/auth"
	"gametrace/pkg/plugin"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// failRegister 制造一次注册被拒。宿主已不再拨号插件，注册失败只可能来自
// manifest / 契约校验：这里用一份缺 type 的 manifest。name 参与失败去重键，
// 因此不同调用要传不同的 name，否则第二次会被去重窗口吞掉。
func failRegister(s *pipelineService, ctx context.Context, name string) {
	manifest := "api_version: gt.decoder/v2\nname: " + name + "\nprotocol: tcp\n"
	if _, err := s.registry.Register(ctx, &pb.RegisterRequest{
		Manifest: []byte(manifest),
	}); err == nil {
		panic("register should fail: " + name)
	}
}

// owner 作用域与 ListPlugins 一致：非 admin 见匿名+自己的；admin 全见。
func TestListRegisterFailuresOwnerScope(t *testing.T) {
	s := &pipelineService{registry: plugin.NewRegistryServer(10)}
	failRegister(s, auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"}),
		"scope-decoder-bob")
	failRegister(s, context.Background(), "scope-decoder-anon")

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
