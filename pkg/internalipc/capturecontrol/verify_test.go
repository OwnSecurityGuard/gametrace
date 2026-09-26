package capturecontrol

import (
	"context"
	"testing"

	pb "gametrace/pkg/internalipc/proto"
)

func TestServer_Verify(t *testing.T) {
	engine := &fakeEngine{
		verifyResult: VerifyResult{
			Verdict:     "warn",
			VerifyRunID: "verify_1",
			SessionID:   "s1",
			AtUnix:      123,
			Violations: []ViolationView{
				{RuleID: "payload-non-empty", Topic: "encoding", Severity: "error", Count: 2, Sample: "schema_id empty", Layer: "transport"},
			},
			Checks: VerifyChecks{Decode: "warn", Semantic: "pass"},
			Quality: &QualityView{
				InputRaw: 20, InputCandidate: 10, DecodeSuccess: 5, DecodeUnknown: 5, DecodeUnknownRatio: 0.5,
				CorrelatedInputs: 1, EntropyEstimate: 7.9, DecodeErrors: 0,
			},
			Applicability: &VerifyApplicability{Result: "match", Applicable: true, Reason: "ok", TargetPort: 8080, TotalPackets: 20, MatchedPackets: 10},
		},
	}
	srv := NewServer(engine)
	resp, err := srv.Verify(context.Background(), &pb.VerifyRequest{
		SessionId: "s1", Plugin: "http", Protocol: "tcp", Limit: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetVerdict() != "warn" {
		t.Errorf("verdict = %q, want warn", resp.GetVerdict())
	}
	if resp.GetVerifyRunId() != "verify_1" || resp.GetSessionId() != "s1" || resp.GetAtUnix() != 123 {
		t.Errorf("unexpected identifiers: run=%q session=%q at=%d", resp.GetVerifyRunId(), resp.GetSessionId(), resp.GetAtUnix())
	}
	if len(resp.GetViolations()) != 1 {
		t.Fatalf("violations = %d, want 1", len(resp.GetViolations()))
	}
	v := resp.GetViolations()[0]
	if v.GetRuleId() != "payload-non-empty" || v.GetSeverity() != "error" || v.GetCount() != 2 || v.GetLayer() != "transport" {
		t.Errorf("violation = %+v", v)
	}
	if c := resp.GetChecks(); c.GetDecode() != "warn" || c.GetSemantic() != "pass" {
		t.Errorf("checks = %+v", c)
	}
	if a := resp.GetApplicability(); a.GetResult() != "match" || !a.GetApplicable() || a.GetMatchedPackets() != 10 {
		t.Errorf("applicability = %+v", a)
	}
	q := resp.GetQuality()
	if q.GetInputRaw() != 20 || q.GetInputCandidate() != 10 || q.GetDecodeUnknown() != 5 ||
		q.GetDecodeUnknownRatio() != 0.5 || q.GetEntropyEstimate() != 7.9 {
		t.Errorf("quality = %+v", q)
	}
	// 请求参数正确传到引擎。
	if engine.verifyLastReq.SessionID != "s1" || engine.verifyLastReq.Plugin != "http" ||
		engine.verifyLastReq.Protocol != "tcp" || engine.verifyLastReq.Limit != 7 {
		t.Errorf("engine req = %+v", engine.verifyLastReq)
	}
}

// TestServer_VerifyNotApplicable 锁定「会话不适用」的线上形态：Quality 必须为
// nil（不能给出任何质量数字），两轴 not_run，verdict not_applicable —— 用户看到
// 的是「换会话」而不是「插件质量差」。
func TestServer_VerifyNotApplicable(t *testing.T) {
	engine := &fakeEngine{
		verifyResult: VerifyResult{
			Verdict:       "not_applicable",
			SessionID:     "s1",
			Checks:        VerifyChecks{Decode: "not_run", Semantic: "not_run"},
			Applicability: &VerifyApplicability{Result: "not_match", Applicable: false, Reason: "no_matching_packets", TargetPort: 8080, TotalPackets: 1000, MatchedPackets: 0},
		},
	}
	srv := NewServer(engine)
	resp, err := srv.Verify(context.Background(), &pb.VerifyRequest{SessionId: "s1", Plugin: "http"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetVerdict() != "not_applicable" {
		t.Errorf("verdict = %q, want not_applicable", resp.GetVerdict())
	}
	if resp.GetQuality() != nil {
		t.Errorf("quality = %+v, want nil for a non-applicable session", resp.GetQuality())
	}
	if c := resp.GetChecks(); c.GetDecode() != "not_run" || c.GetSemantic() != "not_run" {
		t.Errorf("checks = %+v, want both not_run", c)
	}
	if a := resp.GetApplicability(); a.GetResult() != "not_match" || a.GetApplicable() {
		t.Errorf("applicability = %+v", a)
	}
}

func TestServer_SampleBytes(t *testing.T) {
	engine := &fakeEngine{
		sampleResult: SampleBytesResult{
			SessionID:        "s1",
			RequestedPackets: 20,
			ReturnedPackets:  3,
			ReturnedBytes:    64,
			Truncated:        true,
			MeanEntropy:      5.5,
			AuditID:          42,
			Packets: []SampledPacket{
				{RawPacketID: "r1", Src: "1.1.1.1", Dst: "2.2.2.2", Length: 64, Hex: "ab", Entropy: 5.5, FirstByte: 1},
			},
			LengthHistogram: map[int32]int64{64: 3},
			FirstByteDist:   map[int32]int64{1: 3},
		},
	}
	srv := NewServer(engine)
	resp, err := srv.SampleBytes(context.Background(), &pb.SampleBytesRequest{
		SessionId: "s1", Plugin: "http", Limit: 20, MaxBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetReturnedPackets() != 3 || resp.GetReturnedBytes() != 64 || !resp.GetTruncated() {
		t.Errorf("counts = pkts=%d bytes=%d trunc=%v", resp.GetReturnedPackets(), resp.GetReturnedBytes(), resp.GetTruncated())
	}
	if resp.GetMeanEntropy() != 5.5 {
		t.Errorf("mean_entropy = %v, want 5.5", resp.GetMeanEntropy())
	}
	if resp.GetAuditId() != 42 {
		t.Errorf("audit_id = %d, want 42", resp.GetAuditId())
	}
	if len(resp.GetPackets()) != 1 || resp.GetPackets()[0].GetRawPacketId() != "r1" {
		t.Errorf("packets = %+v", resp.GetPackets())
	}
	if resp.GetLengthHistogram()[64] != 3 || resp.GetFirstByteDistribution()[1] != 3 {
		t.Errorf("histograms = len=%v fbd=%v", resp.GetLengthHistogram(), resp.GetFirstByteDistribution())
	}
	if engine.sampleLastReq.SessionID != "s1" || engine.sampleLastReq.Limit != 20 || engine.sampleLastReq.MaxBytes != 64 {
		t.Errorf("engine req = %+v", engine.sampleLastReq)
	}
}
