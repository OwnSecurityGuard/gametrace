package main

import (
	"encoding/json"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// 消息定义：与 examples/http 的 client/server 信封约定保持一致。
const (
	cmdLoginRequest  = 1001 // 请求消息（role=request）
	cmdLoginResponse = 1002 // 正常响应（role=response）
	cmdPlayerNotify  = 2001 // 推送消息（role=push，命中 push rule）
)

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

// envelopeSemantics holds the decoded envelope fields of one HTTP message,
// covering message id / role / seq correlation / push rule / error.
type envelopeSemantics struct {
	Cmd       int64
	MsgName   string
	IsPush    bool // push rule: cmd==2001 或 body.seq==0
	Seq       int64
	ErrorCode int64
	IsError   bool // error rule: error_code != 0
}

// parseEnvelope best-effort extracts the envelope semantics from a JSON body.
// A malformed or non-JSON body yields the zero semantics (unknown, no error).
func parseEnvelope(body []byte) envelopeSemantics {
	var raw struct {
		Header struct {
			Cmd int64 `json:"cmd"`
		} `json:"header"`
		Body struct {
			Seq       int64 `json:"seq"`
			ErrorCode int64 `json:"error_code"`
		} `json:"body"`
	}
	_ = json.Unmarshal(body, &raw)

	s := envelopeSemantics{
		Cmd:       raw.Header.Cmd,
		Seq:       raw.Body.Seq,
		ErrorCode: raw.Body.ErrorCode,
	}
	s.MsgName = msgName(s.Cmd)
	s.IsPush = s.Cmd == cmdPlayerNotify || s.Seq == 0
	s.IsError = s.ErrorCode != 0
	return s
}

// role returns the communication role for one direction.
// 推送判定仍由解码器给出（供前端即时展示），规则侧的语义标注由平台
// 依据 semantic_rules 的 annotate 效果独立产出。
func (s envelopeSemantics) role(isRequest bool) string {
	if s.IsPush {
		return "push"
	}
	if isRequest {
		return "request"
	}
	return "response"
}

// emit turns one parsed HTTP message into an event with the Payload / Meta /
// Analysis channels separated, then sends it:
//
//	Payload:  业务字段（信封语义 + HTTP 行信息 + body 原文）
//	Meta:     网络/元事实（direction / flow_id / msg_name / role / is_push）
//	Analysis: 状态变更（_state_changes，供平台实体基线投影）
func (d *decoder) emit(stream pb.Decoder_DecodeV2Server, inputID, flowID string, m *httpMessage) error {
	c := d.counts[flowID]
	sem := parseEnvelope(m.body)

	meta := map[string]any{
		"flow_id":   flowID,
		"msg_name":  sem.MsgName,
		"role":      sem.role(m.isRequest),
		"is_push":   sem.IsPush,
		"direction": "client_to_server",
	}
	if !m.isRequest {
		meta["direction"] = "server_to_client"
	}

	// 业务 payload：请求带 method/path，响应带 status；error_code/is_error
	// 双方向都上报，保证 error 语义规则（error_code != 0）对请求同样生效。
	payload := map[string]any{
		"cmd":            sem.Cmd,
		"seq":            sem.Seq,
		"error_code":     sem.ErrorCode,
		"is_error":       sem.IsError,
		"body_text":      string(m.body),
		"body_truncated": m.bodyTruncated,
	}

	// 状态变更：请求/响应各按自己的计数路径（requests / responses）投影，
	// version 为流内消息总数（请求+响应）。
	var draft event.Draft
	change := map[string]any{
		"subject_type": "http_request",
		"subject_id":   flowID,
		"op":           "set",
		"path":         "requests",
		"before":       c.requests,
		"after":        c.requests + 1,
		"version":      c.requests + c.responses + 1,
	}
	if m.isRequest {
		c.requests++
		payload["method"] = m.method
		payload["path"] = m.path
		payload["requests"] = c.requests
		draft.Type = "http.request"
	} else {
		c.responses++
		change["subject_type"] = "http_response"
		change["path"] = "responses"
		change["before"] = c.responses - 1
		change["after"] = c.responses
		change["version"] = c.requests + c.responses
		payload["status"] = m.status
		payload["responses"] = c.responses
		draft.Type = "http.response"
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
