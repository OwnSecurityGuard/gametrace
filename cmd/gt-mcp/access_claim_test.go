package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	pb "gametrace/pkg/internalipc/proto"
	"gametrace/pkg/store"
)

// claimFakePipeline 仅实现 claim 需要的方法（StartCapture + GetRegistryAddr），
// 避免依赖共享 fakeCaptureClient（其后端接口为 nil，调用会 panic）。
type claimFakePipeline struct {
	pb.CaptureControlClient
	sessionID string
}

func (f *claimFakePipeline) StartCapture(_ context.Context, _ *pb.StartCaptureRequest, _ ...grpc.CallOption) (*pb.StartCaptureResponse, error) {
	return &pb.StartCaptureResponse{SessionId: f.sessionID, State: "running", DbPath: "s-claim/capture.sqlite"}, nil
}

func (f *claimFakePipeline) GetRegistryAddr(_ context.Context, _ *pb.GetRegistryAddrRequest, _ ...grpc.CallOption) (*pb.GetRegistryAddrResponse, error) {
	return &pb.GetRegistryAddrResponse{RegistryAddr: "192.168.1.10:9091"}, nil
}

func newClaimCapture(t *testing.T) (*mcpCapture, *accessCodeStore) {
	t.Helper()
	workDir := t.TempDir()
	cs, err := store.NewControlStore(filepath.Join(workDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	store := newAccessCodeStore(cs.DB())
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	m := &mcpCapture{
		sessionMgr:     newSessionManager(workDir),
		accessCodes:    store,
		pipelineClient: &claimFakePipeline{sessionID: "s-claim"},
		tokensByOwner:  map[string]string{"alice": "tok_alice"},
	}
	return m, store
}

// TestAccessClaimReturnsConfig 验证 claim 只发「身份与回连」：
// 不开会话、不带 bpf/plugin_names，回连地址按调用方请求回推（无 GT_PUBLIC_HOST 时）。
func TestAccessClaimReturnsConfig(t *testing.T) {
	m, store := newClaimCapture(t)
	if err := store.Create(context.Background(), &accessCode{
		Code: "GT-ABC1-DEF2", Owner: "alice",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/access/claim?code=GT-ABC1-DEF2", nil)
	rec := httptest.NewRecorder()
	m.handleAccessClaim(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	// 端口取 pipeline 的 registry 端口，host 取调用方请求的 host（探针怎么进来就怎么回连）。
	if cfg["server"] != "example.com:9091" {
		t.Fatalf("server = %v", cfg["server"])
	}
	if cfg["ingest_addr"] != "example.com:9092" {
		t.Fatalf("ingest_addr = %v", cfg["ingest_addr"])
	}
	if cfg["token"] != "tok_alice" {
		t.Fatalf("token = %v", cfg["token"])
	}
	// 抓包参数不再在接入阶段下发（由 probe_start_capture 决定）。
	for _, k := range []string{"session", "bpf", "plugin_names"} {
		if v, ok := cfg[k]; ok && v != "" {
			t.Fatalf("claim must not carry capture params: %s = %v", k, v)
		}
	}
	if rec.Header().Get("X-Session-Id") != "" {
		t.Fatalf("X-Session-Id = %q, want empty", rec.Header().Get("X-Session-Id"))
	}
	got, _ := store.Get(context.Background(), "GT-ABC1-DEF2")
	if !got.Claimed {
		t.Fatalf("code not marked claimed: %+v", got)
	}
}

// TestAdvertisedAddrsPrecedence 锁住回连地址的优先级：
// GT_PUBLIC_HOST（+ 可选端口覆盖）> 调用方请求的 Host > lanIP。
func TestAdvertisedAddrsPrecedence(t *testing.T) {
	m, _ := newClaimCapture(t) // 桩 pipeline 报 registry 192.168.1.10:9091
	ctx := context.Background()

	// 1) 未配置：host 取请求 Host，端口取 pipeline 内部端口。
	reg, ing, src := m.advertisedAddrs(ctx, "203.0.113.7:18781")
	if reg != "203.0.113.7:9091" || ing != "203.0.113.7:9092" || src != addrSourceRequest {
		t.Fatalf("request-derived: %s %s %s", reg, ing, src)
	}
	// 2) 回环请求不可用（探针拿到会连它自己）→ 退回 lanIP。
	lanIPOverride = "10.0.0.5"
	defer func() { lanIPOverride = "" }()
	reg, _, src = m.advertisedAddrs(ctx, "localhost:8781")
	if reg != "10.0.0.5:9091" || src != addrSourceLAN {
		t.Fatalf("lan fallback: %s %s", reg, src)
	}
	// 3) 显式通告：docker 端口映射场景（内 9091/9092，外 19091/19092）。
	t.Setenv(envPublicHost, "gt.example.com")
	t.Setenv(envPublicRegistryPort, "19091")
	t.Setenv(envPublicIngestPort, "19092")
	reg, ing, src = m.advertisedAddrs(ctx, "localhost:8781")
	if reg != "gt.example.com:19091" || ing != "gt.example.com:19092" || src != addrSourceEnv {
		t.Fatalf("public env: %s %s %s", reg, ing, src)
	}
	// 4) 只配 host：端口沿用 pipeline 内部端口（裸机/同端口部署）。
	os.Unsetenv(envPublicRegistryPort)
	os.Unsetenv(envPublicIngestPort)
	reg, ing, _ = m.advertisedAddrs(ctx, "")
	if reg != "gt.example.com:9091" || ing != "gt.example.com:9092" {
		t.Fatalf("host-only: %s %s", reg, ing)
	}
}

func TestAccessClaimRejectsInvalidAndExpired(t *testing.T) {
	m, store := newClaimCapture(t)
	// 过期码
	if err := store.Create(context.Background(), &accessCode{
		Code: "GT-OLD1-XXXX", Owner: "bob", ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	expReq := httptest.NewRequest(http.MethodGet, "/access/claim?code=GT-OLD1-XXXX", nil)
	expRec := httptest.NewRecorder()
	m.handleAccessClaim(expRec, expReq)
	if expRec.Code != http.StatusGone {
		t.Fatalf("expired code: expected 410, got %d", expRec.Code)
	}

	// 未知码
	badReq := httptest.NewRequest(http.MethodGet, "/access/claim?code=GT-NOPE-0000", nil)
	badRec := httptest.NewRecorder()
	m.handleAccessClaim(badRec, badReq)
	if badRec.Code != http.StatusNotFound {
		t.Fatalf("unknown code: expected 404, got %d", badRec.Code)
	}
}

func TestSetupScriptSnippet(t *testing.T) {
	m, _ := newClaimCapture(t)
	req := httptest.NewRequest(http.MethodGet, "/setup.sh?code=GT-ABC1-DEF2", nil)
	rec := httptest.NewRecorder()
	m.handleSetupScript(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup.sh: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte(`CODE="GT-ABC1-DEF2"`)) && !bytes.Contains([]byte(body), []byte("GT-ABC1-DEF2")) {
		t.Fatalf("setup.sh missing code, got:\n%s", body)
	}
	if !bytes.Contains([]byte(body), []byte("/access/claim?code=")) {
		t.Fatalf("setup.sh missing claim url, got:\n%s", body)
	}
}

func TestSetupPS1ScriptSnippet(t *testing.T) {
	m, _ := newClaimCapture(t)
	req := httptest.NewRequest(http.MethodGet, "/setup.ps1?code=GT-ABC1-DEF2&platform=windows/amd64", nil)
	rec := httptest.NewRecorder()
	m.handleSetupScriptPS1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup.ps1: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte("GT-ABC1-DEF2")) {
		t.Fatalf("setup.ps1 missing code, got:\n%s", body)
	}
	if !bytes.Contains([]byte(body), []byte("/access/claim?code=")) {
		t.Fatalf("setup.ps1 missing claim url, got:\n%s", body)
	}
	if !bytes.Contains([]byte(body), []byte("Invoke-RestMethod")) {
		t.Fatalf("setup.ps1 missing Invoke-RestMethod, got:\n%s", body)
	}
	if !bytes.Contains([]byte(body), []byte("Expand-Archive")) {
		t.Fatalf("setup.ps1 missing Expand-Archive, got:\n%s", body)
	}
}

func TestSetupPS1RequiresCode(t *testing.T) {
	m, _ := newClaimCapture(t)
	req := httptest.NewRequest(http.MethodGet, "/setup.ps1", nil)
	rec := httptest.NewRecorder()
	m.handleSetupScriptPS1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("setup.ps1 without code: expected 400, got %d", rec.Code)
	}
}
