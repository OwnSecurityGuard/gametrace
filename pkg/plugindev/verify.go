package plugindev

// VerifyResult is the output of plugin.verify (P4). It carries the SDK contract
// violations (each tagged with a contract.yaml rule_id), the gt-side quality
// statistics computed over the real decode corpus, and an overall verdict.
//
// plugin.explain (P3b) consumes a VerifyResult to attribute decode-class
// failures — the verdict alone ("fail") is useless to the AI; what it needs is
// "all inputs came back unknown" / "framing is wrong" / "looks encrypted" /
// "looks like reassembly is missing". Those four patterns are exactly what
// explainVerify reads off this struct.
type VerifyResult struct {
	// Violations are SDK checker results (transport self-consistency + semantic
	// contract), each referencing a contract.yaml rule_id and tagged with the
	// Layer it came from.
	Violations []*Violation
	// Quality is the gt-side statistical view over the candidate corpus (good-vs-bad
	// judgement, which needs real traffic and therefore cannot run offline in
	// the plugin module — design §5). nil when the session was not applicable:
	// reporting statistics over traffic the plugin was never meant to decode is
	// what made a wrong session look like a broken plugin.
	Quality *QualityStats
	// Verdict is one of "pass" | "warn" | "fail" | "not_applicable".
	Verdict string
	// Checks is the layered verdict (decode / semantic axes) so a failure can be
	// pointed at the axis that actually broke.
	Checks *VerifyChecks
}

// VerifyChecks 分层结论：把 verdict 拆到两条校验轴上，避免「插件坏了」和
// 「会话选错了」被同一个 fail 糊在一起。applicability 不作为独立字段 —— 它由
// session applicability 判定（capturecontrol.VerifyApplicability）单独承载；
// 会话不适用时这里两轴都是 not_run。
type VerifyChecks struct {
	// Decode is the decode-quality axis: "pass" | "warn" | "fail" | "not_run".
	Decode string
	// Semantic is the SDK semantic-contract axis: "pass" | "warn" | "fail" | "not_run".
	Semantic string
}

// 校验轴（Violation.Layer）—— 决定一条违规计入哪条 checks 轴。
const (
	// LayerTransport is the host-side transport self-consistency layer
	// (single-message protocol: non-final response must carry event_type and a
	// non-empty payload).
	LayerTransport = "transport"
	// LayerSemantic is the SDK semantic-contract layer (runtime/schema/state).
	LayerSemantic = "semantic"
)

// NotRun 是「该轴未执行」的哨兵值：会话不适用时 decode / semantic 两轴均为它 ——
// 没有该插件的流量，任何质量判定都无从谈起。
const NotRun = "not_run"

// Violation is a single SDK contract rule that the verify corpus tripped.
type Violation struct {
	RuleID    string
	Topic     string
	Severity  string
	Statement string
	DocRef    string
	Count     int
	Sample    string
	// Layer is LayerTransport or LayerSemantic; it decides which checks axis the
	// violation feeds.
	Layer string
}

// QualityStats holds the corpus-level signals explainVerify classifies. Every
// field is optional on the wire; a nil QualityStats simply means no statistical
// evidence is available (explain then falls back to violations only).
//
// The input/decode split is the responsibility boundary: input.raw is the whole
// window, input.candidate is the subset matching the session's target port, and
// every decode statistic is computed over candidates only.
type QualityStats struct {
	// InputRaw is the number of raw packets the verified window contained.
	InputRaw int
	// InputCandidate is the subset of InputRaw matching the session's target port —
	// the packets this plugin is actually expected to decode.
	InputCandidate int
	// DecodeSuccess / DecodeUnknown count candidate packets that produced at least
	// one event vs. none at all. Their sum + DecodeErrors is InputCandidate.
	DecodeSuccess int
	DecodeUnknown int
	// DecodeUnknownRatio is DecodeUnknown/InputCandidate, cached for convenience.
	// When set (>=0) it is trusted over recomputing from the integer counts.
	//
	// 名字里带 decode 是刻意的：future 还会有 ignored_frame / control_frame /
	// encrypted_payload 等其它「未产出事件」的原因，泛化的 unknown_ratio 到时
	// 一定会歧义。
	DecodeUnknownRatio float64
	// CorrelatedInputs counts candidate inputs that carried a correlation_key (or
	// were tied to a prior input via causation). Zero with many inputs suggests
	// missing stream reassembly.
	CorrelatedInputs int
	// LongPacketErrors counts decode_errors that concentrated on long packets —
	// a hint that message boundaries were guessed wrong (framing / reassembly).
	LongPacketErrors int
	// EntropyEstimate is the mean Shannon entropy of payloads in bits/byte
	// (0..8). High values with high unknown ratio suggest encryption/compression.
	EntropyEstimate float64
	// DecodeErrors is the total number of decode errors across the candidate corpus.
	DecodeErrors int
}

// Decode-attribution thresholds (P3b). Centralised so tests and future tuning
// share one source of truth. Exported because pkg/plugin/quality (P4) reuses
// them when computing the verdict.
const (
	// AllUnknownRatioThreshold: at/above this share of undecodable inputs the
	// decoder is treated as producing all-unknown output.
	AllUnknownRatioThreshold = 0.95
	// EncryptionUnknownRatioThreshold: combined with high entropy, this share
	// of undecodable inputs triggers the suspected-encryption finding.
	EncryptionUnknownRatioThreshold = 0.5
	// HighEntropyThreshold bits/byte: payload entropy at/above this, together
	// with a majority of undecodable inputs, looks encrypted/compressed.
	HighEntropyThreshold = 7.5
	// VerdictNotApplicable: 会话适用性判定（P1-1）给出的 verdict —— 过滤窗口内
	// 没有插件该解的流量（matching=0 / 无包），属于「换会话重试」而非「插件质量差」。
	// 该 verdict 不触发 validated 立证（仅 pass 立证），也不进入 pass|warn|fail
	// 的质量判定。
	VerdictNotApplicable = "not_applicable"
	// VerdictPass 是唯一会立证（plugin_validations）的 verdict：只有会话适用且
	// 解码/语义两轴都通过的插件实例才算「已验证」。
	VerdictPass = "pass"
)
