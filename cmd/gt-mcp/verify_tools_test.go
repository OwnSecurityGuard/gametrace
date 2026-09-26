package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"google.golang.org/grpc"
	pb "gametrace/pkg/internalipc/proto"
)

// fakeCaptureClient 是 pb.CaptureControlClient 的轻量桩：仅覆写 Verify 与
// SampleBytes，其余接口方法由嵌入的 nil 接口提供（测试不会调用）。
type fakeCaptureClient struct {
	pb.CaptureControlClient
	verifyReq      *pb.VerifyRequest
	verifyResp     *pb.VerifyResponse
	verifyErr      error
	sampleReq      *pb.SampleBytesRequest
	sampleResp     *pb.SampleBytesResponse
	sampleErr      error
	startReq       *pb.StartCaptureRequest
	dbDir          string
	listPluginsReq *pb.ListPluginsRequest
	manifestReq    *pb.GetPluginManifestRequest
	// recentFailures 预设 ListPlugins 返回的注册失败记录。
	recentFailures []*pb.PluginFailure
	// liveSessions 预设 ListCaptureSessions 的返回值（nil = 无 live 会话）。
	liveSessions *pb.ListCaptureSessionsResponse
}

func (f *fakeCaptureClient) Verify(ctx context.Context, in *pb.VerifyRequest, _ ...grpc.CallOption) (*pb.VerifyResponse, error) {
	f.verifyReq = in
	return f.verifyResp, f.verifyErr
}
func (f *fakeCaptureClient) SampleBytes(ctx context.Context, in *pb.SampleBytesRequest, _ ...grpc.CallOption) (*pb.SampleBytesResponse, error) {
	f.sampleReq = in
	return f.sampleResp, f.sampleErr
}
// GetCaptureStatus 返回空响应：list_all_sessions 的 live 计数覆盖在 fake 下不可用，
// 运行中会话的计数回落到持久化元数据（语义与 pipeline 短暂不可达一致）。
func (f *fakeCaptureClient) GetCaptureStatus(ctx context.Context, in *pb.GetCaptureStatusRequest, _ ...grpc.CallOption) (*pb.GetCaptureStatusResponse, error) {
	return nil, nil
}

// TestHandleVerifyPluginForwards locks in P4: the MCP layer is a pure forwarder
// to the Runtime Plane — it maps arguments onto the gRPC VerifyRequest and
// relays the verdict/violations back. No attribution logic lives here.
func TestHandleVerifyPluginForwards(t *testing.T) {
	fc := &fakeCaptureClient{
		verifyResp: &pb.VerifyResponse{
			Verdict:     "warn",
			VerifyRunId: "verify_1",
			SessionId:   "s1",
			Violations:  []*pb.VerifyViolation{{RuleId: "payload-non-empty", Severity: "error", Count: 2, Layer: "transport"}},
			Checks:      &pb.VerifyChecks{Decode: "warn", Semantic: "pass"},
			Applicability: &pb.VerifyApplicability{
				Result: "match", Applicable: true, Reason: "ok", TargetPort: 8080, TotalPackets: 20, MatchedPackets: 10,
			},
			Quality: &pb.VerifyQuality{InputRaw: 20, InputCandidate: 10, DecodeSuccess: 7, DecodeUnknown: 3, DecodeUnknownRatio: 0.3},
		},
	}
	m := &mcpCapture{pipelineClient: fc}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"session_id": "s1", "plugin": "http", "limit": 5}

	res, err := m.handleVerifyPlugin(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if fc.verifyReq == nil || fc.verifyReq.GetSessionId() != "s1" ||
		fc.verifyReq.GetPlugin() != "http" || fc.verifyReq.GetLimit() != 5 {
		t.Fatalf("verify not forwarded correctly: %+v", fc.verifyReq)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "warn") || !strings.Contains(text, "payload-non-empty") {
		t.Fatalf("response missing verdict/rule_id: %s", text)
	}
	// 分层呈现：input/decode 分组 + applicability，用户才能看出分母是谁。
	for _, want := range []string{`"raw":20`, `"candidate":10`, `"unknown":3`, `"result":"match"`, `"target_port_hits":10`} {
		if !strings.Contains(text, want) {
			t.Fatalf("response missing %s: %s", want, text)
		}
	}
}

// TestHandleVerifyPluginNotApplicable 锁定「选错会话」的呈现：verdict/status 为
// not_applicable、quality 为 null（不给质量数字）、ok=false，并给出换会话的
// 下一步 —— 与「插件质量差」彻底分开。
func TestHandleVerifyPluginNotApplicable(t *testing.T) {
	fc := &fakeCaptureClient{
		verifyResp: &pb.VerifyResponse{
			Verdict:   "not_applicable",
			SessionId: "s1",
			Checks:    &pb.VerifyChecks{Decode: "not_run", Semantic: "not_run"},
			Applicability: &pb.VerifyApplicability{
				Result: "not_match", Applicable: false, Reason: "no_matching_packets", TargetPort: 8080, TotalPackets: 1000, MatchedPackets: 0,
			},
		},
	}
	m := &mcpCapture{pipelineClient: fc}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"session_id": "s1", "plugin": "http"}

	res, err := m.handleVerifyPlugin(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(mcp.TextContent).Text
	for _, want := range []string{`"ok":false`, `"status":"not_applicable"`, `"quality":null`, `"verdict":"not_applicable"`, `"result":"not_match"`, `"matched_packets":0`} {
		if !strings.Contains(text, want) {
			t.Fatalf("response missing %s: %s", want, text)
		}
	}
}

func TestHandleVerifyPluginMissingArgs(t *testing.T) {
	fc := &fakeCaptureClient{}
	m := &mcpCapture{pipelineClient: fc}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"plugin": "http"} // missing session_id

	res, _ := m.handleVerifyPlugin(context.Background(), req)
	if fc.verifyReq != nil {
		t.Fatal("should not call pipeline when session_id is missing")
	}
	if !strings.Contains(res.Content[0].(mcp.TextContent).Text, "session_id is required") {
		t.Fatalf("expected required-arg error")
	}
}

// TestHandleSampleBytesPluginForwards locks in P4: sample_bytes is a pure
// forwarder; the response carries the audit_id so the caller can prove the
// access was recorded (design §6).
func TestHandleSampleBytesPluginForwards(t *testing.T) {
	fc := &fakeCaptureClient{
		sampleResp: &pb.SampleBytesResponse{
			SessionId:        "s1",
			RequestedPackets: 20,
			ReturnedPackets:  3,
			ReturnedBytes:    64,
			Truncated:        true,
			MeanEntropy:      5.5,
			AuditId:          42,
			Packets:          []*pb.SampledPacket{{RawPacketId: "r1"}},
			LengthHistogram:  map[int32]int64{64: 3},
		},
	}
	m := &mcpCapture{pipelineClient: fc}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"session_id": "s1", "plugin": "http", "limit": 20, "max_bytes": 64}

	res, err := m.handleSampleBytesPlugin(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if fc.sampleReq == nil || fc.sampleReq.GetSessionId() != "s1" ||
		fc.sampleReq.GetLimit() != 20 || fc.sampleReq.GetMaxBytes() != 64 {
		t.Fatalf("sample_bytes not forwarded correctly: %+v", fc.sampleReq)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "audit_id") || !strings.Contains(text, "42") {
		t.Fatalf("response missing audit_id: %s", text)
	}
}
