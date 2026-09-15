package plugin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gametrace/pkg/auth"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// failManifest 是合法最小 manifest（与 manager_test.go 的 maniA 同构）。
const failManifest = "api_version: gt.decoder/v2\nname: fail-decoder\nprotocol: tcp\ntype: decoder\n"

func registerFail(s *RegistryServer, ctx context.Context, socketPath string) error {
	_, err := s.Register(ctx, &pb.RegisterRequest{SocketPath: socketPath, Manifest: []byte(failManifest)})
	return err
}

func TestRegisterDialFailureRecordedAndEmitted(t *testing.T) {
	s := NewRegistryServer(10)
	events, unsub := s.Subscribe()
	defer unsub()

	err := registerFail(s, context.Background(), "unix:/nonexistent/fail.sock")
	if err == nil {
		t.Fatal("Register should fail when decoder socket is unreachable")
	}
	if !strings.Contains(err.Error(), "dial plugin socket") {
		t.Errorf("error should mention dial plugin socket: %v", err)
	}

	fs := s.ListRegisterFailures()
	if len(fs) != 1 {
		t.Fatalf("want 1 failure, got %d", len(fs))
	}
	if fs[0].Name != "fail-decoder" || fs[0].SocketPath != "unix:/nonexistent/fail.sock" || fs[0].Error == "" {
		t.Errorf("failure record mismatch: %+v", fs[0])
	}

	select {
	case ev := <-events:
		if ev.Type != PluginEventRegisterFailed || ev.Name != "fail-decoder" {
			t.Errorf("unexpected event: %+v", ev)
		}
		if ev.SocketPath != "unix:/nonexistent/fail.sock" || ev.Error == "" {
			t.Errorf("event missing dial details: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("register_failed event not emitted")
	}
}

func TestRegisterFailureDedupAndKeyChange(t *testing.T) {
	s := NewRegistryServer(10)

	_ = registerFail(s, context.Background(), "unix:/nonexistent/a.sock")
	_ = registerFail(s, context.Background(), "unix:/nonexistent/a.sock")
	if got := len(s.ListRegisterFailures()); got != 1 {
		t.Errorf("identical failure within dedup window should be suppressed, got %d", got)
	}

	// key 变化（用户改了 .env 重启）立即记录
	_ = registerFail(s, context.Background(), "unix:/nonexistent/b.sock")
	if got := len(s.ListRegisterFailures()); got != 2 {
		t.Errorf("changed key should record immediately, got %d", got)
	}
}

func TestRegisterFailureTTL(t *testing.T) {
	oldTTL, oldDedup := failureTTL, failureDedupWindow
	failureTTL = 20 * time.Millisecond
	failureDedupWindow = time.Hour // 防去重干扰 TTL 断言
	defer func() { failureTTL, failureDedupWindow = oldTTL, oldDedup }()

	s := NewRegistryServer(10)
	_ = registerFail(s, context.Background(), "unix:/nonexistent/ttl.sock")
	time.Sleep(30 * time.Millisecond)
	if got := len(s.ListRegisterFailures()); got != 0 {
		t.Errorf("expired failure should be dropped, got %d", got)
	}
}

func TestRegisterFailureCapacity(t *testing.T) {
	s := NewRegistryServer(10)
	for i := 0; i < maxRecentFailures+5; i++ {
		_ = registerFail(s, context.Background(), fmt.Sprintf("unix:/nonexistent/cap-%d.sock", i))
	}
	if got := len(s.ListRegisterFailures()); got != maxRecentFailures {
		t.Errorf("ring buffer should cap at %d, got %d", maxRecentFailures, got)
	}
}

func TestRegisterFailureOwnerRecorded(t *testing.T) {
	s := NewRegistryServer(10)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"})
	if err := registerFail(s, ctx, "unix:/nonexistent/own.sock"); err == nil {
		t.Fatal("register should fail")
	}
	fs := s.ListRegisterFailures()
	if len(fs) != 1 || fs[0].Owner != "bob" {
		t.Fatalf("failure owner not recorded: %+v", fs)
	}
}
