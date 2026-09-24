// Package quality implements plugin.verify's statistical half (design §5).
//
// Single-message protocol self-consistency (done lifecycle, payload non-empty)
// is enforced here since SDK v0.7.0 removed the runtime-layer checker; the
// remaining SDK contract layers (semantic rule declaration + evaluation) still
// run via contract.PluginChecker. This package owns the batch statistical
// given a whole decode corpus it produces the gt-side QualityStats and merges
// them with the SDK violations into a single plugindev.VerifyResult + verdict.
//
// The split follows the design's dividing line: "can a plugin author run it
// offline with only the SDK?" — yes for the checker, no for the corpus-level
// statistics, so the latter lives here in gametrace.
package quality

import (
	"math"

	sdkcontract "github.com/OwnSecurityGuard/gametrace/sdk/contract"

	"gametrace/pkg/plugindev"
)

// DecodeIO is one decode input/output pair reduced to the fields the verify
// pass needs. It is deliberately transport-agnostic: the Runtime Plane builds
// it from a real session's decode results, or a test harness builds it by hand.
// plugin.verify merges these into a plugindev.VerifyResult.
type DecodeIO struct {
	InputID string // echoed input_id (raw_packet_id in practice)
	// Candidate marks a packet that matches the session's target port — i.e. the
	// traffic this plugin is actually expected to decode. Statistics and
	// violations are computed over candidates only: a plugin is not accountable
	// for packets that are not its protocol, and counting them is exactly what
	// made a wrong session look like a broken plugin.
	Candidate   bool
	Done        bool   // final response flag
	EventType   string // first emitted event_type, "" if none
	PayloadLen  int    // len of the emitted payload (non-empty check)
	DecodeError string // non-empty => the response carried a decode error
	Correlated  bool   // any emitted event carried a correlation_key
	Payload     []byte // undecoded bytes, only for the entropy estimate
}

// packetAgg folds every DecodeIO belonging to one input packet into a single
// verdict-relevant record. A packet normally produces N event responses plus a
// terminating done-response; counting those separately would report the empty
// done-response as an "unknown" packet.
type packetAgg struct {
	decodeErr  bool     // any response for this packet carried a decode error
	hasEvent   bool     // any response emitted an event_type
	correlated bool     // any emitted event carried a correlation key
	payloads   [][]byte // raw bytes seen, for the entropy estimate
}

// Verify runs the SDK contract checker over the candidate DecodeIOs and merges
// the results with gt-side statistical quality into a single VerifyResult.
//
// 责任边界：input.raw = 语料里出现的全部原始包；input.candidate = 命中会话 target
// port 的子集。违规与解码统计一律只对 candidate 计算 —— 非本插件的流量既不是它
// 的责任，也不该污染它的成绩单。
func Verify(corpus []DecodeIO) *plugindev.VerifyResult {
	res := &plugindev.VerifyResult{
		Verdict: "pass",
		Quality: &plugindev.QualityStats{},
		Checks:  &plugindev.VerifyChecks{},
	}
	q := res.Quality

	rawIDs := map[string]struct{}{}
	candIDs := map[string]struct{}{}
	var candidates []DecodeIO
	for _, io := range corpus {
		rawIDs[io.InputID] = struct{}{}
		if !io.Candidate {
			continue
		}
		candidates = append(candidates, io)
		candIDs[io.InputID] = struct{}{}
	}
	q.InputRaw = len(rawIDs)
	q.InputCandidate = len(candIDs)

	if len(candidates) == 0 {
		// 没有 candidate：没有插件该解的流量，任何质量判定都无从谈起。空语料不是
		// pass，所以 verdict 取 warn、两轴 not_run（会话不适用的情况由 pipeline 在
		// applicability 阶段覆盖为 not_applicable）。
		res.Verdict = "warn"
		res.Checks = &plugindev.VerifyChecks{Decode: plugindev.NotRun, Semantic: plugindev.NotRun}
		return res
	}

	// 1) 单消息协议自洽检查。SDK v0.7.0 移除了运行时层 CheckDecodeResponse，
	// 其有效语义在此保留：非终止响应必须携带 event_type / 非空 payload。
	// （旧版 input_id 回显检查在本语料构造下恒真，无需保留。）
	viol := map[string]*plugindev.Violation{}
	var order []string
	for _, io := range candidates {
		if io.Done {
			continue
		}
		var ruleID, msg string
		switch {
		case io.EventType == "":
			ruleID, msg = "payload-non-empty", "non-final response requires event_type"
		case io.PayloadLen == 0:
			ruleID, msg = "payload-non-empty", "non-final response requires non-empty payload_msgpack"
		default:
			continue
		}
		if _, seen := viol[ruleID]; !seen {
			viol[ruleID] = &plugindev.Violation{RuleID: ruleID, Severity: "error", Layer: plugindev.LayerTransport}
			order = append(order, ruleID)
		}
		e := viol[ruleID]
		e.Count++
		if e.Sample == "" {
			e.Sample = msg
		}
		// 该规则属宿主运行时层，SDK contract.yaml 不再收录其 spec（v0.7.0 起
		// 运行时检查移出 SDK），spec 命中时仅补充文档引用等元数据。
		if spec, ok := sdkcontract.Default().RuleByID(ruleID); ok {
			e.Topic = spec.Topic
			e.Severity = string(spec.Severity)
			e.Statement = spec.Statement
			e.DocRef = spec.DocRef
		}
	}
	for _, id := range order {
		res.Violations = append(res.Violations, viol[id])
	}

	// 2) gt-side quality statistics, aggregated per packet (InputID) so that a
	//    packet's terminating done-response is not double-counted as "unknown".
	agg := map[string]*packetAgg{}
	for _, io := range candidates {
		a := agg[io.InputID]
		if a == nil {
			a = &packetAgg{}
			agg[io.InputID] = a
		}
		if io.DecodeError != "" {
			a.decodeErr = true
		}
		if io.EventType != "" {
			a.hasEvent = true
		}
		if io.Correlated {
			a.correlated = true
		}
		if len(io.Payload) > 0 {
			a.payloads = append(a.payloads, io.Payload)
		}
	}

	var success, unknown, correlated, decodeErrors int
	var entropySum float64
	entropyN := 0
	for _, a := range agg {
		if a.decodeErr {
			decodeErrors++
			continue
		}
		if a.hasEvent {
			success++
		} else {
			unknown++
		}
		if a.correlated {
			correlated++
		}
		for _, p := range a.payloads {
			entropySum += shannonBits(p)
			entropyN++
		}
	}
	q.DecodeSuccess = success
	q.DecodeUnknown = unknown
	q.CorrelatedInputs = correlated
	q.DecodeErrors = decodeErrors
	if q.InputCandidate > 0 {
		q.DecodeUnknownRatio = float64(unknown) / float64(q.InputCandidate)
	}
	if entropyN > 0 {
		q.EntropyEstimate = entropySum / float64(entropyN)
	}

	// 3) 分层结论 + verdict.
	res.Checks = axes(res, q)
	res.Verdict = verdict(res, q)
	return res
}

// RecomputeVerdict re-evaluates the verdict + layered checks of a VerifyResult
// after the caller appended extra violations (e.g. semantic-contract CheckEvent
// results) to it. Callers must have finished mutating res.Violations /
// res.Quality beforehand.
func RecomputeVerdict(res *plugindev.VerifyResult) {
	if res == nil {
		return
	}
	if res.Quality == nil {
		res.Quality = &plugindev.QualityStats{}
	}
	res.Checks = axes(res, res.Quality)
	res.Verdict = verdict(res, res.Quality)
}

// axes derives the per-axis verdict (decode / semantic) so a failure points at
// the axis that actually broke instead of one opaque fail.
func axes(res *plugindev.VerifyResult, q *plugindev.QualityStats) *plugindev.VerifyChecks {
	if q == nil || q.InputCandidate == 0 {
		return &plugindev.VerifyChecks{Decode: plugindev.NotRun, Semantic: plugindev.NotRun}
	}
	return &plugindev.VerifyChecks{
		Decode:   axisVerdict(res.Violations, plugindev.LayerTransport, q),
		Semantic: axisVerdict(res.Violations, plugindev.LayerSemantic, q),
	}
}

// axisVerdict 汇总指定层（transport / semantic）的违规 + 该层的统计信号。
// layer=LayerTransport 时并入解码统计（全错误 / 全未知 / 疑似加密）。
func axisVerdict(violations []*plugindev.Violation, layer string, q *plugindev.QualityStats) string {
	hasErr, hasWarn := false, false
	for _, v := range violations {
		if v.Layer != layer {
			continue
		}
		switch v.Severity {
		case string(sdkcontract.SeverityError):
			hasErr = true
		case string(sdkcontract.SeverityWarn):
			hasWarn = true
		}
	}
	if layer == plugindev.LayerTransport {
		if allErrored(q) || allUnknown(q) {
			hasErr = true
		}
		if suspectEncrypted(q) {
			hasWarn = true
		}
	}
	switch {
	case hasErr:
		return "fail"
	case hasWarn:
		return "warn"
	default:
		return "pass"
	}
}

// verdict merges every violation with the statistical signals into
// pass|warn|fail. A session with no candidate packets is never a pass.
func verdict(res *plugindev.VerifyResult, q *plugindev.QualityStats) string {
	if q.InputCandidate == 0 {
		// 空语料不是 pass：AI 给了空语料，或会话对该插件不适用。
		return "warn"
	}
	for _, v := range res.Violations {
		if v.Severity == string(sdkcontract.SeverityError) {
			return "fail"
		}
	}
	if allErrored(q) || allUnknown(q) {
		return "fail"
	}
	for _, v := range res.Violations {
		if v.Severity == string(sdkcontract.SeverityWarn) {
			return "warn"
		}
	}
	if suspectEncrypted(q) {
		return "warn"
	}
	return "pass"
}

// allErrored: every candidate packet came back as a decode error.
func allErrored(q *plugindev.QualityStats) bool {
	return q.InputCandidate > 0 && q.DecodeErrors == q.InputCandidate
}

// allUnknown: at/above AllUnknownRatioThreshold of candidates produced no event.
func allUnknown(q *plugindev.QualityStats) bool {
	return q.DecodeUnknownRatio >= plugindev.AllUnknownRatioThreshold
}

// suspectEncrypted: high entropy + majority undecodable looks encrypted/compressed.
func suspectEncrypted(q *plugindev.QualityStats) bool {
	return q.DecodeUnknownRatio >= plugindev.EncryptionUnknownRatioThreshold &&
		q.EntropyEstimate >= plugindev.HighEntropyThreshold
}

// shannonBits returns the Shannon entropy of b in bits/byte (0..8).
func shannonBits(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var freq [256]float64
	for _, c := range b {
		freq[c]++
	}
	var h float64
	n := float64(len(b))
	for _, f := range freq {
		if f == 0 {
			continue
		}
		p := f / n
		h -= p * math.Log2(p)
	}
	return h
}
