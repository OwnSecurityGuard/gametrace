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

	sdkcontract "github.com/OwnSecurityGuard/gt-plugin-sdk/contract"

	"gametrace/pkg/plugindev"
)

// DecodeIO is one decode input/output pair reduced to the fields the verify
// pass needs. It is deliberately transport-agnostic: the Runtime Plane builds
// it from a real session's decode results, or a test harness builds it by hand.
// plugin.verify merges these into a plugindev.VerifyResult.
type DecodeIO struct {
	InputID     string // echoed input_id (raw_packet_id in practice)
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

// Verify runs the SDK contract checker over every DecodeIO and merges the
// results with gt-side statistical quality into a single VerifyResult.
func Verify(corpus []DecodeIO) *plugindev.VerifyResult {
	res := &plugindev.VerifyResult{Verdict: "pass", Quality: &plugindev.QualityStats{}}
	if len(corpus) == 0 {
		// Nothing to verify is not a pass: the AI supplied an empty corpus.
		res.Verdict = "warn"
		return res
	}

	// 1) 单消息协议自洽检查。SDK v0.7.0 移除了运行时层 CheckDecodeResponse，
	// 其有效语义在此保留：非终止响应必须携带 event_type / 非空 payload。
	// （旧版 input_id 回显检查在本语料构造下恒真，无需保留。）
	viol := map[string]*plugindev.Violation{}
	var order []string
	for _, io := range corpus {
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
			viol[ruleID] = &plugindev.Violation{RuleID: ruleID, Severity: "error"}
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
	q := res.Quality
	agg := map[string]*packetAgg{}
	var addPacket func(io DecodeIO)
	addPacket = func(io DecodeIO) {
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
	for _, io := range corpus {
		addPacket(io)
	}

	q.TotalInputs = len(agg)
	var unknown, correlated, decodeErrors int
	var entropySum float64
	entropyN := 0
	for _, a := range agg {
		if a.decodeErr {
			decodeErrors++
			continue
		}
		if !a.hasEvent {
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
	q.UnknownInputs = unknown
	q.CorrelatedInputs = correlated
	q.DecodeErrors = decodeErrors
	if q.TotalInputs > 0 {
		q.UnknownRatio = float64(unknown) / float64(q.TotalInputs)
	}
	if entropyN > 0 {
		q.EntropyEstimate = entropySum / float64(entropyN)
	}

	// 3) verdict.
	res.Verdict = verdict(res, q)
	return res
}

// RecomputeVerdict re-evaluates the verdict of a VerifyResult after the caller
// appended extra violations (e.g. semantic-contract CheckEvent results) to it.
// Callers must have finished mutating res.Violations / res.Quality beforehand.
func RecomputeVerdict(res *plugindev.VerifyResult) {
	if res == nil {
		return
	}
	if res.Quality == nil {
		res.Quality = &plugindev.QualityStats{}
	}
	res.Verdict = verdict(res, res.Quality)
}

// verdict merges SDK violations with statistical signals into pass|warn|fail.
func verdict(res *plugindev.VerifyResult, q *plugindev.QualityStats) string {
	hasErr, hasWarn := false, false
	for _, v := range res.Violations {
		switch v.Severity {
		case string(sdkcontract.SeverityError):
			hasErr = true
		case string(sdkcontract.SeverityWarn):
			hasWarn = true
		}
	}
	allErrored := q.TotalInputs > 0 && q.DecodeErrors == q.TotalInputs
	allUnknown := q.UnknownRatio >= plugindev.AllUnknownRatioThreshold
	// High entropy + majority undecodable looks encrypted/compressed.
	suspectEnc := q.UnknownRatio >= plugindev.EncryptionUnknownRatioThreshold &&
		q.EntropyEstimate >= plugindev.HighEntropyThreshold

	if hasErr || allErrored || allUnknown {
		return "fail"
	}
	if hasWarn || suspectEnc {
		return "warn"
	}
	return "pass"
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
