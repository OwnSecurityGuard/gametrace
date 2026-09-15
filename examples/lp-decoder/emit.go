package main

import (
	"encoding/json"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// 消息定义：与 examples/lp 的 client/server 信封约定保持一致。
const (
	cmdLoginRequest  = 1001 // 请求消息（role=request）
	cmdLoginResponse = 1002 // 正常响应（role=response）
	cmdPlayerNotify  = 2001 // 推送消息（role=push，命中 push rule）
)

// maxBodyBytes caps the captured payload text per frame (64KB). Longer
// payloads are truncated and flagged via body_truncated.
const maxBodyBytes = 64 << 10

// msgName maps a cmd message id to its symbolic name (unknown when not declared).
func msgName(cmd int64) string {
	switch cmd {
	case cmdLoginRequest:
		return "LoginRequest"
	case cmdLoginResponse:
		return "LoginResponse"
	case cmdPlayerNotify:
		return "PlayerNotify"
	default:
		return "unknown"
	}
}

// envelopeSemantics holds the decoded envelope fields of one lp frame,
// covering message id / role / seq correlation / push rule / error.
type envelopeSemantics struct {
	Cmd       int64
	MsgName   string
	IsPush    bool // push rule: cmd==2001 或 seq==0
	Seq       int64
	ErrorCode int64
	IsError   bool // error rule: error_code != 0
	IsRequest bool // direction: cmd 身份决定（模板协议特有，见 plugin.yaml）
}

// parseEnvelope best-effort extracts the envelope semantics from a JSON body.
// A malformed or non-JSON body yields the zero semantics (unknown, no error).
func parseEnvelope(body []byte) envelopeSemantics {
	var raw struct {
		Cmd       int64 `json:"cmd"`
		Seq       int64 `json:"seq"`
		ErrorCode int64 `json:"error_code"`
	}
	_ = json.Unmarshal(body, &raw)

	s := envelopeSemantics{
		Cmd:       raw.Cmd,
		Seq:       raw.Seq,
		ErrorCode: raw.ErrorCode,
	}
	s.MsgName = msgName(s.Cmd)
	s.IsPush = s.Cmd == cmdPlayerNotify || s.Seq == 0
	s.IsRequest = s.Cmd == cmdLoginRequest
	s.IsError = s.ErrorCode != 0
	return s
}

// direction 由协议语义（cmd 身份）推导：1001 必为客户端→服务端，
// 1002/2001 必为服务端→客户端。⚠️ 这是模板协议独有的约定——如果真实协议
// 存在双向同 cmd 的消息，就不能沿用此处，必须改用请求方向可判别的字段。
func (s envelopeSemantics) direction() string {
	if s.IsRequest {
		return "client_to_server"
	}
	return "server_to_client"
}

// role returns the communication role for one direction.
// 推送判定仍由解码器给出（供前端即时展示），规则侧的语义标注由平台
// 依据 semantic_rules 的 annotate 效果独立产出。
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

	// 业务 payload：共用签名（cmd/seq/error_code/is_error/body_text）。
	payload := map[string]any{
		"cmd":            sem.Cmd,
		"seq":            sem.Seq,
		"error_code":     sem.ErrorCode,
		"is_error":       sem.IsError,
		"body_text":      string(body),
		"body_truncated": truncated,
	}

	// 状态变更：请求/响应各按自己的计数路径投影，version 为流内消息总数。
	var draft event.Draft
	change := map[string]any{
		"subject_type": "lp_request",
		"subject_id":   flowID,
		"op":           "set",
		"path":         "requests",
		"before":       c.requests,
		"after":        c.requests + 1,
		"version":      c.requests + c.responses + 1,
	}
	if sem.IsRequest {
		c.requests++
		payload["requests"] = c.requests
		payload["req_seq"] = sem.Seq
		draft.Type = "lp.request"
	} else {
		c.responses++
		change["subject_type"] = "lp_response"
		change["path"] = "responses"
		change["before"] = c.responses - 1
		change["after"] = c.responses
		change["version"] = c.requests + c.responses
		payload["responses"] = c.responses
		draft.Type = "lp.response"
	}
	draft.Value = event.ValueFromMap(payload)
	draft.Meta = event.ValueFromMap(meta)
	draft.Analysis = event.ValueFromMap(map[string]any{
		"_state_changes": []any{change},
	})
	d.counts[flowID] = c

	resp, err := draft.ToResponse(inputID)
	if err != nil {
		return stream.Send(&pb.DecodeResponseV2{InputId: inputID, Done: true, Error: err.Error()})
	}
	return stream.Send(resp)
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
