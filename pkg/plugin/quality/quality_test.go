package quality

import (
	"testing"

	"gametrace/pkg/plugindev"
)

// highEntropy / lowEntropy 用于构造可预测的熵估计。
var (
	highEntropy = func() []byte {
		b := make([]byte, 256)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}()
	lowEntropy = []byte("AAAAAAAAAAAAAAAA")
)

// validEventIO 构造一个非终结响应（带 event_type / payload 非空）。
func validEventIO(id string) DecodeIO {
	return DecodeIO{InputID: id, Candidate: true, Done: false, EventType: "game.login", PayloadLen: 1}
}

// doneIO 构造一个终结响应，可携带原始字节供熵估计。
func doneIO(id string, payload []byte) DecodeIO {
	return DecodeIO{InputID: id, Candidate: true, Done: true, Payload: payload}
}

// foreignIO 构造一个未命中会话 target port 的包（非 candidate）：它只应计入
// input.raw，绝不参与解码统计与违规判定。
func foreignIO(id string) DecodeIO {
	return DecodeIO{InputID: id, Done: true, Payload: lowEntropy}
}

func TestVerifyPass(t *testing.T) {
	corpus := []DecodeIO{
		validEventIO("p1"), doneIO("p1", lowEntropy),
		validEventIO("p2"), doneIO("p2", lowEntropy),
		validEventIO("p3"), doneIO("p3", lowEntropy),
	}
	res := Verify(corpus)
	if res.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass", res.Verdict)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("unexpected violations: %+v", res.Violations)
	}
	q := res.Quality
	if q == nil {
		t.Fatal("quality nil")
	}
	// 统计按输入包（InputID）聚合：3 个包各产生「事件响应 + 终结响应」，
	// 计 3 个 candidate input，而不是 6 条 DecodeIO。
	if q.InputCandidate != 3 {
		t.Errorf("input_candidate = %d, want 3", q.InputCandidate)
	}
	if q.DecodeUnknown != 0 {
		t.Errorf("decode_unknown = %d, want 0", q.DecodeUnknown)
	}
	if q.DecodeErrors != 0 {
		t.Errorf("decode_errors = %d, want 0", q.DecodeErrors)
	}
	if q.EntropyEstimate > 1.0 {
		t.Errorf("entropy_estimate = %v, want near 0 (low entropy payload)", q.EntropyEstimate)
	}
}

func TestVerifyFailAllUnknown(t *testing.T) {
	var corpus []DecodeIO
	for i := 0; i < 10; i++ {
		id := "p" + string(rune('a'+i))
		corpus = append(corpus, doneIO(id, lowEntropy)) // decoded but no events
	}
	res := Verify(corpus)
	if res.Verdict != "fail" {
		t.Fatalf("verdict = %q, want fail", res.Verdict)
	}
	q := res.Quality
	if q.DecodeUnknown != 10 || q.DecodeUnknownRatio != 1.0 {
		t.Fatalf("decode_unknown = %d / %v, want 10 / 1.0", q.DecodeUnknown, q.DecodeUnknownRatio)
	}
}

func TestVerifyFailDecodeErrors(t *testing.T) {
	corpus := []DecodeIO{
		{InputID: "p1", Candidate: true, Done: true, DecodeError: "boom", Payload: lowEntropy},
		{InputID: "p2", Candidate: true, Done: true, DecodeError: "bang", Payload: lowEntropy},
		{InputID: "p3", Candidate: true, Done: true, DecodeError: "crash", Payload: lowEntropy},
	}
	res := Verify(corpus)
	if res.Verdict != "fail" {
		t.Fatalf("verdict = %q, want fail", res.Verdict)
	}
	if res.Quality.DecodeErrors != 3 {
		t.Errorf("decode_errors = %d, want 3", res.Quality.DecodeErrors)
	}
}

func TestVerifyViolationPayloadNonEmpty(t *testing.T) {
	// 一个非终结响应带 event_type 但 payload 为空 -> payload-non-empty 违规。
	corpus := []DecodeIO{
		{InputID: "p1", Candidate: true, Done: false, EventType: "game.login"},
		doneIO("p1", lowEntropy),
	}
	res := Verify(corpus)
	if len(res.Violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(res.Violations))
	}
	v := res.Violations[0]
	if v.RuleID != "payload-non-empty" {
		t.Errorf("rule_id = %q, want payload-non-empty", v.RuleID)
	}
	if v.Severity != "error" {
		t.Errorf("severity = %q, want error", v.Severity)
	}
	if v.Count != 1 {
		t.Errorf("count = %d, want 1", v.Count)
	}
	// topic：payload-non-empty 属宿主运行时层，SDK contract.yaml（v0.7.0 起）
	// 不再收录其 spec，因此 topic 为空是预期行为，此处不再断言。
	// error 级违规 -> 整体 fail。
	if res.Verdict != "fail" {
		t.Fatalf("verdict = %q, want fail", res.Verdict)
	}
}

func TestVerifyWarnEncryption(t *testing.T) {
	// 3 个全未知（高熵）+ 1 个有效（高熵）：unknown_ratio 0.75 落在 [0.5,0.95)，
	// 熵估计接近上限 -> 疑似加密，verdict warn（未到全未知 fail 阈值）。
	corpus := []DecodeIO{
		doneIO("p1", highEntropy),
		doneIO("p2", highEntropy),
		doneIO("p3", highEntropy),
		validEventIO("p4"), doneIO("p4", highEntropy),
	}
	res := Verify(corpus)
	if res.Verdict != "warn" {
		t.Fatalf("verdict = %q, want warn", res.Verdict)
	}
	q := res.Quality
	if q.DecodeUnknownRatio < 0.5 || q.DecodeUnknownRatio >= 0.95 {
		t.Errorf("decode_unknown_ratio = %v, want in [0.5,0.95)", q.DecodeUnknownRatio)
	}
	if q.EntropyEstimate < 7.5 {
		t.Errorf("entropy_estimate = %v, want >= 7.5", q.EntropyEstimate)
	}
}

func TestVerifyReassemblyQuality(t *testing.T) {
	// 5 个包都解出事件但无一关联 -> CorrelatedInputs=0（explain 会据此归因缺流重组）。
	corpus := []DecodeIO{}
	for i := 0; i < 5; i++ {
		id := "p" + string(rune('a'+i))
		corpus = append(corpus, validEventIO(id), doneIO(id, lowEntropy))
	}
	res := Verify(corpus)
	q := res.Quality
	if q.CorrelatedInputs != 0 {
		t.Errorf("correlated_inputs = %d, want 0", q.CorrelatedInputs)
	}
	if q.InputCandidate != 5 {
		t.Errorf("input_candidate = %d, want 5 (per input packet, not per DecodeIO)", q.InputCandidate)
	}
}

// TestVerifyOnlyCandidatesCounted 覆盖责任边界：未命中会话 target port 的包只
// 计入 input.raw，不参与统计、不产生违规 —— 否则「用户选错抓包」会被读成
// 「插件质量差」。
func TestVerifyOnlyCandidatesCounted(t *testing.T) {
	corpus := []DecodeIO{
		validEventIO("p1"), doneIO("p1", lowEntropy),
		foreignIO("f1"), foreignIO("f2"), foreignIO("f3"),
	}
	res := Verify(corpus)
	if res.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass (foreign packets must not drag the plugin down)", res.Verdict)
	}
	q := res.Quality
	if q.InputRaw != 4 {
		t.Errorf("input_raw = %d, want 4", q.InputRaw)
	}
	if q.InputCandidate != 1 {
		t.Errorf("input_candidate = %d, want 1", q.InputCandidate)
	}
	if q.DecodeUnknown != 0 || q.DecodeUnknownRatio != 0 {
		t.Errorf("decode_unknown = %d / %v, want 0 / 0", q.DecodeUnknown, q.DecodeUnknownRatio)
	}
}

func TestVerifyEmptyCorpus(t *testing.T) {
	res := Verify(nil)
	if res.Verdict != "warn" {
		t.Fatalf("verdict = %q, want warn (empty corpus is not a pass)", res.Verdict)
	}
	if res.Quality == nil {
		t.Fatal("quality nil")
	}
	if res.Checks == nil || res.Checks.Decode != plugindev.NotRun || res.Checks.Semantic != plugindev.NotRun {
		t.Fatalf("checks = %+v, want both axes not_run", res.Checks)
	}
}

// 确保 plugindev 阈值常量可被本包正确使用（编译期引用）。
func TestThresholdsReferenced(t *testing.T) {
	if plugindev.AllUnknownRatioThreshold <= 0 || plugindev.EncryptionUnknownRatioThreshold <= 0 || plugindev.HighEntropyThreshold <= 0 {
		t.Fatal("thresholds must be positive")
	}
}
