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

// failManifest 是合法最小 manifest，但声明错误的 major 版本（v1 vs 宿主 v2），
// 触发 CheckManifestVersion 失败——这是非隧道拨号删除后登记失败的主要来源。
const failManifest = "api_version: gt.decoder/v1\nname: fail-decoder\nprotocol: tcp\ntype: decoder\n"

// failManifestFmt 是 api_version 格式非法的 manifest，触发 ValidateManifest 失败
// （错误文案与 failManifest 的 version-mismatch 不同，用于去重 key 变化测试）。
const failManifestFmt = "api_version: gt.decoder/v1x\nname: fail-decoder\nprotocol: tcp\ntype: decoder\n"

func registerFail(s *RegistryServer, ctx context.Context, manifest string) error {
	_, err := s.Register(ctx, &pb.RegisterRequest{Manifest: []byte(manifest)})
	return err
}

func TestRegisterFailureRecordedAndEmitted(t *testing.T) {
	s := NewRegistryServer(10)
	events, unsub := s.Subscribe()
	defer unsub()

	err := registerFail(s, context.Background(), failManifest)
	if err == nil {
		t.Fatal("Register should fail on version mismatch")
	}
	if !strings.Contains(err.Error(), "version mismatch") {
		t.Errorf("error should mention version mismatch: %v", err)
	}

	fs := s.ListRegisterFailures()
	if len(fs) != 1 {
		t.Fatalf("want 1 failure, got %d", len(fs))
	}
	if fs[0].Name != "fail-decoder" || fs[0].Error == "" {
		t.Errorf("failure record mismatch: %+v", fs[0])
	}

	select {
	case ev := <-events:
		if ev.Type != PluginEventRegisterFailed || ev.Name != "fail-decoder" {
			t.Errorf("unexpected event: %+v", ev)
		}
		if ev.Error == "" {
			t.Errorf("event missing error detail: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("register_failed event not emitted")
	}
}

func TestRegisterFailureDedupAndKeyChange(t *testing.T) {
	s := NewRegistryServer(10)

	_ = registerFail(s, context.Background(), failManifest)
	_ = registerFail(s, context.Background(), failManifest)
	if got := len(s.ListRegisterFailures()); got != 1 {
		t.Errorf("identical failure within dedup window should be suppressed, got %d", got)
	}

	// key 变化（错误文案不同 → 去重 key 不同）立即记录
	_ = registerFail(s, context.Background(), failManifestFmt)
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
	_ = registerFail(s, context.Background(), failManifest)
	time.Sleep(30 * time.Millisecond)
	if got := len(s.ListRegisterFailures()); got != 0 {
		t.Errorf("expired failure should be dropped, got %d", got)
	}
}

func TestRegisterFailureCapacity(t *testing.T) {
	s := NewRegistryServer(10)
	for i := 0; i < maxRecentFailures+5; i++ {
		// 每个用不同 name 但同为 version-mismatch 错误，确保不去重、填满 ring buffer
		_ = registerFail(s, context.Background(), fmt.Sprintf("api_version: gt.decoder/v1\nname: cap-%d\nprotocol: tcp\ntype: decoder\n", i))
	}
	if got := len(s.ListRegisterFailures()); got != maxRecentFailures {
		t.Errorf("ring buffer should cap at %d, got %d", maxRecentFailures, got)
	}
}

func TestRegisterFailureOwnerRecorded(t *testing.T) {
	s := NewRegistryServer(10)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Owner: "bob"})
	if err := registerFail(s, ctx, failManifest); err == nil {
		t.Fatal("register should fail")
	}
	fs := s.ListRegisterFailures()
	if len(fs) != 1 || fs[0].Owner != "bob" {
		t.Fatalf("failure owner not recorded: %+v", fs)
	}
}
