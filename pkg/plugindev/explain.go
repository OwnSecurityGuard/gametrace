package plugindev

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// ExplainRefPrefix prefixes every explain_ref. A ref uniquely identifies one
// explain conclusion so callers can point back to it.
const ExplainRefPrefix = "expl_"

var explainSeq int64

// newExplainRef returns a process-unique explain_ref derived from the nanosecond
// clock plus a monotonic counter, so two conclusions generated in the same
// instant never collide.
func newExplainRef() string {
	n := atomic.AddInt64(&explainSeq, 1)
	return ExplainRefPrefix + strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(n, 36)
}

// ExplainRequest asks for the attribution of a plugin's verify result.
type ExplainRequest struct {
	Name string
	// Verify is an inline verify result to attribute (decode-class failures).
	// When omitted, the most recent result recorded via RecordVerify is used.
	Verify *VerifyResult
}

// ExplainFinding is one attributed cause of a decode failure.
type ExplainFinding struct {
	Category string // machine category
	RuleID   string // optional SDK contract rule_id (SSOT: contract.yaml rules:)
	Why      string // human explanation
	Fix      string // actionable corrective step
}

// ExplainResult is the conclusion of a plugin.explain invocation. Its Ref is
// what callers point back to.
type ExplainResult struct {
	Ref        string
	Name       string
	Action     string
	At         time.Time
	Summary    string
	Findings   []*ExplainFinding
	NextAction string
}

// Explain attributes a plugin's verify result: the inline result when supplied,
// otherwise the most recent one recorded by plugin.verify. A passing verdict has
// nothing to fix; a failing one is classified into the decode-class findings and
// the conclusion is stored so callers can surface explain_ref.
func Explain(_ context.Context, req *ExplainRequest) (*ExplainResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	return explainVerifyRequest(req)
}

// explainVerifyRequest attributes a verify result for a plugin, either the one
// passed inline or the most recent recorded by plugin.verify.
func explainVerifyRequest(req *ExplainRequest) (*ExplainResult, error) {
	res := &ExplainResult{
		Ref:    newExplainRef(),
		Name:   req.Name,
		Action: "verify",
		At:     time.Now(),
	}
	result := req.Verify
	if result == nil {
		result = defaultTracker.LastVerify(req.Name)
	}
	if result == nil {
		res.Summary = "no verify result recorded for " + req.Name
		res.NextAction = "run verify_plugin to produce a verdict, then explain with that verdict"
		return res, nil
	}
	if result.Verdict == "pass" {
		res.Summary = "verify passed; nothing to explain"
		res.NextAction = "plugin instance is validated; continue capturing traffic with it"
		return res, nil
	}

	findings := explainVerify(result)
	if len(findings) == 0 {
		// Verify failed/warn but no decode-class pattern matched: still surface
		// the raw verdict so the AI has a pointer into the violations list.
		findings = []*ExplainFinding{{
			Category: "verify-other",
			Why:      "verify 未通过（verdict=" + result.Verdict + "）但未命中任何已知解码类模式；见 violations 与 quality 细节",
			Fix:      "检查 verify 返回的 violations 列表，按 rule_id 逐条修正后重新 verify",
		}}
	}
	res.Findings = findings
	res.Summary = fmt.Sprintf("verify=%s with %d decode finding(s)", result.Verdict, len(findings))
	res.NextAction = verifyNextAction(findings)
	return res, nil
}

// explainVerify classifies a verify result into the four decode-class findings.
// Each finding references a contract.yaml rule_id so the AI can cross-reference
// the same vocabulary used by verify.
//
// The patterns are intentionally independent and may co-occur (e.g. a decoder
// that both strips headers AND sees all-unknown), so multiple findings can be
// returned.
func explainVerify(result *VerifyResult) []*ExplainFinding {
	var findings []*ExplainFinding

	// 1) Wrong framing — driven by SDK violations on the framing rules.
	// These fire when a decoder treats a full link-layer frame as L7 (did NOT
	// strip by link_type / reassemble). This is the highest-signal finding
	// because it comes straight from the contract checker, not a heuristic.
	for _, v := range result.Violations {
		switch v.RuleID {
		case "payload-framing-by-link-type", "link-type-selects-framing":
			findings = append(findings, &ExplainFinding{
				Category: "wrong-framing",
				RuleID:   v.RuleID,
				Why:      "SDK 校验（" + v.RuleID + "）判定解码器把完整链路层帧当成了 L7：pcap 类来源交付的是带链路/网络/传输头的完整帧，需要先按 link_type 剥头、再按流重组，而不是直接当应用层字节解析",
				Fix:      "用 framing.ExtractL7(payload, link_type) 按 link_type 剥头，再用 framing.NewReassembler 做 TCP 重组；只有 ProxyPayload(1001)/TLSPlaintext(1002) 才是纯 L7（见 contract.yaml payload-framing-by-link-type）",
			})
		}
	}

	q := result.Quality
	if q == nil {
		// No quality stats: only SDK violations are attributable.
		return findings
	}

	// 2) All-unknown — every candidate input produced no event.
	unknownRatio := q.DecodeUnknownRatio
	if unknownRatio <= 0 && q.InputCandidate > 0 {
		unknownRatio = float64(q.DecodeUnknown) / float64(q.InputCandidate)
	}
	if q.InputCandidate > 0 && q.DecodeUnknown >= q.InputCandidate {
		findings = append(findings, &ExplainFinding{
			Category: "all-unknown",
			RuleID:   "inspect-bytes-first",
			Why:      "解码器对全部 " + strconv.Itoa(q.InputCandidate) + " 个候选输入都未产出任何事件（全 unknown）。典型根因是剥头/重组缺失：把带链路层头的完整帧直接当 L7 解析，业务字节整体错位导致 0 命中",
			Fix:      "①先用 sample_bytes_plugin 看真实首字节确认 link_type 与帧结构；②用 framing.ExtractL7 按 link_type 剥头；③TCP 类协议接 framing.Reassembler 重组。不要假设 payload 已是 L7（只有 ProxyPayload/TLSPlaintext 才是）",
		})
	} else if unknownRatio >= AllUnknownRatioThreshold {
		findings = append(findings, &ExplainFinding{
			Category: "all-unknown",
			RuleID:   "inspect-bytes-first",
			Why:      fmt.Sprintf("%.0f%% 的输入未能解码（全 unknown），大概率解码器没按 link_type 剥头/重组，把完整帧当 L7 解析", unknownRatio*100),
			Fix:      "先用 sample_bytes_plugin 看真实首字节；用 framing.ExtractL7 剥头 + framing.Reassembler 重组；TCP body 恒空往往是缺重组的信号",
		})
	}

	// 3) Suspected encryption/compression — high entropy + majority undecodable.
	if q.EntropyEstimate >= HighEntropyThreshold && unknownRatio >= EncryptionUnknownRatioThreshold {
		findings = append(findings, &ExplainFinding{
			Category: "suspected-encryption",
			RuleID:   "inspect-bytes-first",
			Why: fmt.Sprintf("首字节分布接近均匀、熵估计约 %.1f bit/byte（接近 8 上限），且 %.0f%% 输入未能解码，疑似加密或压缩流，没有明文 framing 可识别",
				q.EntropyEstimate, unknownRatio*100),
			Fix: "确认该协议是否真有明文层；若确为加密/压缩，先解密再解码，或显式将 schema 标注为 opaque bytes 而非强行解析",
		})
	}

	// 4) Suspected missing stream reassembly — many inputs, none correlated,
	// but the decoder DID produce some events (otherwise there is nothing to
	// correlate and the all-unknown finding already covers it).
	if q.InputCandidate > 1 && q.CorrelatedInputs == 0 && (q.InputCandidate-q.DecodeUnknown-q.DecodeErrors) > 0 {
		findings = append(findings, &ExplainFinding{
			Category: "suspected-reassembly",
			RuleID:   "tcp-reassembly-required",
			Why:      fmt.Sprintf("同一会话被切成 %d 个候选 input，但没有任何 correlation_key 串联，疑似缺流重组或粘包未切分", q.InputCandidate),
			Fix:      "按流重组后再按长度前缀/分隔符切分消息（用 framing.NewReassembler）；只有在确实可推断时才填 correlation_key（correlation-only-when-known）",
		})
	}

	return findings
}

// verifyNextAction derives a single human next step from the decode findings,
// ordered by how directly it unblocks the decoder.
func verifyNextAction(findings []*ExplainFinding) string {
	if len(findings) == 0 {
		return "review the verify verdict and retest"
	}
	switch findings[0].Category {
	case "wrong-framing":
		return "add framing.ExtractL7 + Reassembler to strip by link_type (payload is a full frame, NOT L7), then re-verify"
	case "all-unknown":
		return "run sample_bytes_plugin to inspect real bytes, then add framing.ExtractL7 + Reassembler"
	case "suspected-encryption":
		return "confirm whether the stream is encrypted/compressed; decrypt first or mark schema opaque"
	case "suspected-reassembly":
		return "reassembly the stream and split messages by length-prefix/delimiter before decoding"
	default:
		return "review the verify violations and retest"
	}
}