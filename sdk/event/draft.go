package event

import (
	"fmt"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// Draft 是插件侧的事件产出。它缺少宿主补齐的 Identity/Trace（EventID、
// SessionID、Source、Timestamp、CausationID、OriginID 等由宿主填充），因此是
// "草稿"而非完整 Event（§7.3）。
//
// 设计约定：插件只负责 type / payload / 可选的关联 hint；宿主补全身份后
// 才是 Event。这样 payload-non-empty / input-id-echo / event-value-required 三条
// 传输层规则在 SDK 里天然满足，插件无需手工拼 DecodeResponseV2。
type Draft struct {
	// Type 是 event_type，描述"发生了什么"（如 game.player.moved）。
	Type EventType
	// Value 是解码出的业务载荷（根必须是 object）。
	Value Value

	// Meta 是元信息（可选）：direction、msg_name、role、is_push 等由系统附加的结构化字段。
	// 传输契约中对应 DecodeResponseV2.meta_msgpack；IsNull() 时不传输。
	Meta Value

	// Analysis 是分析（可选）：_state_changes、entity、entity_type、entity_id、change_count 等
	// 平台推导/投影所需的数据。传输契约中对应 DecodeResponseV2.analysis_msgpack；IsNull() 时不传输。
	Analysis Value

	// CorrelationKey 由插件填的业务关联键（→ Trace.CorrelationID，宿主规范化）。
	CorrelationKey string
	// CausationInputID 由插件填的因果输入 id（→ Trace.CausationID，宿主解析）。
	CausationInputID string
}

// 曾有过 MessageOrdinal 字段，已删除：DecodeResponseV2 没有 message_ordinal
// 字段，该值无法传输，插件设了会静默无效。序号请走载荷保留键
// _meta.message_ordinal（宿主解析进 Context.MessageOrdinal，见 event.go）。

// Validate 校验 Draft 的最小不变量：type 非空、payload 根为 object。
func (d Draft) Validate() error {
	if d.Type == "" {
		return fmt.Errorf("draft: event_type is required")
	}
	if d.Value.Kind != Object {
		return fmt.Errorf("draft: payload root must be object, got %s", d.Value.Kind.String())
	}
	return nil
}

// ToResponse 把 Draft 编码为一条非终止的 DecodeResponseV2。
// 自动满足 input-id-echo（input_id 原样回显）、payload-non-empty、
// event-value-required（经 Value.MarshalMsgpack）。
func (d Draft) ToResponse(inputID string) (*pb.DecodeResponseV2, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	data, err := d.Value.MarshalMsgpack()
	if err != nil {
		return nil, fmt.Errorf("draft: marshal payload: %w", err)
	}
	metaData, err := marshalOpt(d.Meta)
	if err != nil {
		return nil, fmt.Errorf("draft: marshal meta: %w", err)
	}
	analysisData, err := marshalOpt(d.Analysis)
	if err != nil {
		return nil, fmt.Errorf("draft: marshal analysis: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("draft: payload must not be empty")
	}
	return &pb.DecodeResponseV2{
		InputId:        inputID,
		EventType:      string(d.Type),
		PayloadMsgpack: data,
		// 关联/因果 hint 透传（§7.3）：宿主 dispatcher 读这两个字段做
		// WithCorrelation / WithCausation。漏发会导致插件静默失去因果链，
		// 且不报错——只在 trace 下钻时表现为 CorrelationID 为空。
		CorrelationKey:   d.CorrelationKey,
		CausationInputId: d.CausationInputID,
		MetaMsgpack:      metaData,
		AnalysisMsgpack:  analysisData,
	}, nil
}

// marshalOpt 编码可选的 Value：IsNull() 时返回 nil（不传输）。
func marshalOpt(v Value) ([]byte, error) {
	if v.IsNull() {
		return nil, nil
	}
	return v.MarshalMsgpack()
}

// Done 返回一个仅回显 input_id 的终止响应（done=true），供一次输入收尾。
func Done(inputID string) *pb.DecodeResponseV2 {
	return &pb.DecodeResponseV2{InputId: inputID, Done: true}
}
