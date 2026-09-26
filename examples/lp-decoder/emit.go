package main

import (
	"encoding/json"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// 消息定义：与 examples/lp 的 client/server 信封约定保持一致。
// 号段语义：1000~1999 请求/响应成对（请求为奇数、响应号 = 请求号 + 1），
// 2000~2999 服务端推送，9000~9999 信封级错误回包。
const (
	cmdLoginRequest     = 1001
	cmdLoginResponse    = 1002
	cmdGetBagRequest    = 1003
	cmdGetBagResponse   = 1004
	cmdUseItemRequest   = 1005
	cmdUseItemResponse  = 1006
	cmdGatherRequest    = 1007
	cmdGatherResponse   = 1008
	cmdPlayerInfoNotify = 2001
	cmdItemCountNotify  = 2002
	cmdResourceNotify   = 2003
	cmdBadRequest       = 9001
)

// pushCmdRange / requestCmdRange 是号段边界，与 plugin.yaml 的语义规则同源。
const (
	requestCmdLow  = 1000
	requestCmdHigh = 2000
	pushCmdLow     = 2000
	pushCmdHigh    = 3000
)

// maxBodyBytes caps the captured payload text per frame (64KB). Longer
// payloads are truncated and flagged via body_truncated.
const maxBodyBytes = 64 << 10

// msgNames maps a cmd message id to its symbolic name (unknown when not declared).
var msgNames = map[int64]string{
	cmdLoginRequest:     "LoginRequest",
	cmdLoginResponse:    "LoginResponse",
	cmdGetBagRequest:    "GetBagRequest",
	cmdGetBagResponse:   "GetBagResponse",
	cmdUseItemRequest:   "UseItemRequest",
	cmdUseItemResponse:  "UseItemResponse",
	cmdGatherRequest:    "GatherRequest",
	cmdGatherResponse:   "GatherResponse",
	cmdPlayerInfoNotify: "PlayerInfoNotify",
	cmdItemCountNotify:  "ItemCountNotify",
	cmdResourceNotify:   "ResourceNotify",
	cmdBadRequest:       "BadRequest",
}

func msgName(cmd int64) string {
	if name, ok := msgNames[cmd]; ok {
		return name
	}
	return "unknown"
}

// envelopeSemantics holds the decoded envelope fields of one lp frame,
// covering message id / role / seq correlation / push rule / error.
type envelopeSemantics struct {
	Cmd       int64
	MsgName   string
	IsPush    bool // push rule: cmd 落在 2000~2999 推送号段
	Seq       int64
	ErrorCode int64
	ErrorMsg  string
	IsError   bool // error rule: error_code != 0
	IsRequest bool // direction: cmd 号段+奇偶决定（模板协议特有，见 plugin.yaml）
	Data      json.RawMessage
}

// parseEnvelope best-effort extracts the envelope semantics from a JSON body.
// A malformed or non-JSON body yields the zero semantics (unknown, no error).
func parseEnvelope(body []byte) envelopeSemantics {
	var raw struct {
		Cmd       int64           `json:"cmd"`
		Seq       int64           `json:"seq"`
		ErrorCode int64           `json:"error_code"`
		ErrorMsg  string          `json:"error_msg"`
		Data      json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &raw)

	s := envelopeSemantics{
		Cmd:       raw.Cmd,
		Seq:       raw.Seq,
		ErrorCode: raw.ErrorCode,
		ErrorMsg:  raw.ErrorMsg,
		Data:      raw.Data,
	}
	s.MsgName = msgName(s.Cmd)
	s.IsPush = raw.Cmd >= pushCmdLow && raw.Cmd < pushCmdHigh
	s.IsRequest = raw.Cmd >= requestCmdLow && raw.Cmd < requestCmdHigh && raw.Cmd%2 == 1
	s.IsError = s.ErrorCode != 0
	return s
}

// direction 由协议语义（cmd 号段 + 奇偶）推导：奇数请求号必为客户端→服务端，
// 其响应、推送与错误回包必为服务端→客户端。⚠️ 这是模板协议独有的约定——如果真实协议
// 存在双向同 cmd 的消息，就不能沿用此处，必须改用请求方向可判别的字段。
func (s envelopeSemantics) direction() string {
	if s.IsRequest {
		return "client_to_server"
	}
	return "server_to_client"
}

// role returns the communication role for one direction.
// 号段奇偶的判定只有解码器握得到，结果写进 Meta 供 annotate 规则读取
// （request/response），推送侧另由 cmd 号段规则标注 notification。
func (s envelopeSemantics) role() string {
	if s.IsPush {
		return "push"
	}
	if s.IsRequest {
		return "request"
	}
	return "response"
}

// emit turns one parsed lp frame into an event with the Payload / Meta /
// Analysis channels separated, then sends it:
//
//	Payload:  业务字段（信封语义 + body 原文）
//	Meta:     网络/元事实（flow_id / direction / msg_name / role / is_push
//	          + 帧头事实 version / len_width / endian / length —— 线格式信息
//	            属于 meta 而非业务）
//	Analysis: 状态变更（_state_changes，供平台实体基线投影）
func (d *decoder) emit(stream pb.Decoder_DecodeV2Server, inputID, flowID string, f lpFrame) error {
	c := d.counts[flowID]
	sem := parseEnvelope(f.payload)
	body, truncated := truncate(f.payload, maxBodyBytes)

	meta := map[string]any{
		"flow_id":   flowID,
		"msg_name":  sem.MsgName,
		"role":      sem.role(),
		"is_push":   sem.IsPush,
		"direction": sem.direction(),
		// 帧头事实：长度字段宽度与端序逐帧不同，只有握到帧头才知道。
		// 放 meta 便于核对解码是否与发送端一致，不混入业务 payload。
		"frame": map[string]any{
			"version":   int64(f.header.version),
			"len_width": int64(f.header.lenWidth),
			"endian":    endianName(f.header.little),
			"length":    int64(f.header.length),
		},
	}

	// 业务 payload：共用签名（cmd/seq/error_code/error_msg/is_error/body_text）。
	payload := map[string]any{
		"cmd":            sem.Cmd,
		"seq":            sem.Seq,
		"error_code":     sem.ErrorCode,
		"error_msg":      sem.ErrorMsg,
		"is_error":       sem.IsError,
		"body_text":      string(body),
		"body_truncated": truncated,
	}

	// 状态变更两条来源：① 流内请求/响应计数；② 消息体携带的实体状态
	// （道具数量、金币/钻石、等级/经验/在线）。version 统一取流内消息序号。
	var draft event.Draft
	var counter map[string]any
	var version int64
	if sem.IsRequest {
		c.requests++
		version = c.requests + c.responses
		counter = flowCounterChange(flowID, "lp_request", "requests", c.requests-1, c.requests, version)
		payload["requests"] = c.requests
		payload["req_seq"] = sem.Seq
		draft.Type = "lp.request"
	} else {
		c.responses++
		version = c.requests + c.responses
		counter = flowCounterChange(flowID, "lp_response", "responses", c.responses-1, c.responses, version)
		payload["responses"] = c.responses
		draft.Type = "lp.response"
	}
	changes := append([]any{counter}, d.entities.project(flowID, sem, version)...)

	draft.Value = event.ValueFromMap(payload)
	draft.Meta = event.ValueFromMap(meta)
	draft.Analysis = event.ValueFromMap(map[string]any{
		"_state_changes": changes,
	})
	d.counts[flowID] = c

	resp, err := draft.ToResponse(inputID)
	if err != nil {
		return stream.Send(&pb.DecodeResponseV2{InputId: inputID, Done: true, Error: err.Error()})
	}
	return stream.Send(resp)
}

// flowCounterChange 构造一条请求/响应计数的状态变更。
func flowCounterChange(flowID, subjectType, path string, before, after, version int64) map[string]any {
	return map[string]any{
		"subject_type": subjectType,
		"subject_id":   flowID,
		"op":           "set",
		"path":         path,
		"before":       before,
		"after":        after,
		"version":      version,
	}
}

// endianName renders the length-field endianness for the Meta channel.
func endianName(little bool) string {
	if little {
		return "little"
	}
	return "big"
}

// truncate retains at most max bytes, flagging the loss.
func truncate(b []byte, max int) ([]byte, bool) {
	if len(b) > max {
		return b[:max], true
	}
	return b, false
}
