package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// maxBodyBytes caps the captured payload text per frame (64KB). Longer
// payloads are truncated and flagged via *_truncated.
const maxBodyBytes = 64 << 10

// textEnvelope is the JSON envelope carried by ws.text frames (examples/ws).
// Parsed best-effort and flattened into the payload so the semantic_rules
// (pair by seq, annotate push/error) can read type / seq / error_code.
type textEnvelope struct {
	Type      string `json:"type"`
	Seq       int64  `json:"seq"`
	Text      string `json:"text"`
	ErrorCode int64  `json:"error_code,omitempty"`
}

// parseTextEnvelope best-effort parses a text frame payload as JSON. A
// malformed payload yields the zero envelope (unknown, no error).
func parseTextEnvelope(p []byte) textEnvelope {
	var env textEnvelope
	_ = json.Unmarshal(p, &env)
	return env
}

// baseMeta builds the Meta channel facts for one event. Direction follows the
// wire: client frames are masked, server frames are not (RFC 6455 §5.3), and
// the handshake request/response are inherently c2s / s2c. An empty msgName
// omits the key entirely — the name semantic rule supplies it instead.
func baseMeta(flowID, direction, msgName, role string, isPush bool) map[string]any {
	meta := map[string]any{
		"flow_id":   flowID,
		"direction": direction,
		"role":      role,
		"is_push":   isPush,
	}
	if msgName != "" {
		meta["msg_name"] = msgName
	}
	return meta
}

// frameRole derives the communication role of a frame from its direction.
// 推送判定由 textEnvelope.Type==push 给出（供前端即时展示），规则侧的语义
// 标注由平台依据 semantic_rules 的 annotate 效果独立产出。
func frameRole(direction string, isPush bool) string {
	if isPush {
		return "push"
	}
	if direction == "client_to_server" {
		return "request"
	}
	return "response"
}

// emitHandshake turns one parsed handshake message into a ws.handshake event.
// Non-upgrade HTTP messages are skipped (nothing to decode as WebSocket).
func (d *decoder) emitHandshake(stream pb.Decoder_DecodeV2Server, inputID, flowID string, hs *wsHandshake) error {
	if hs.isRequest {
		if !hs.upgrade {
			return nil
		}
		return send(stream, inputID, event.Draft{
			Type: "ws.handshake",
			Value: event.ValueFromMap(map[string]any{
				"method":  hs.method,
				"path":    hs.path,
				"host":    hs.host,
				"version": hs.version,
			}),
			Meta: event.ValueFromMap(baseMeta(flowID, "client_to_server", "handshake", "request", false)),
			Analysis: event.ValueFromMap(map[string]any{
				"_state_changes": []any{handshakeChange(flowID, false)},
			}),
		})
	}
	if hs.status != 101 {
		return nil
	}
	return send(stream, inputID, event.Draft{
		Type: "ws.handshake",
		Value: event.ValueFromMap(map[string]any{
			"status": hs.status,
		}),
		Meta: event.ValueFromMap(baseMeta(flowID, "server_to_client", "handshake", "response", false)),
		Analysis: event.ValueFromMap(map[string]any{
			"_state_changes": []any{handshakeChange(flowID, true)},
		}),
	})
}

// emitFrame handles one frame: control frames are emitted directly; data
// frames go through continuation reassembly before being emitted.
func (d *decoder) emitFrame(stream pb.Decoder_DecodeV2Server, inputID, flowID string, f wsFrame) error {
	fs := d.flows[flowID]
	if fs == nil {
		fs = &flowState{}
		d.flows[flowID] = fs
	}

	switch f.opcode {
	case wsOpPing, wsOpPong, wsOpClose:
		fs.counts.control++
		return d.emitControl(stream, inputID, flowID, f)
	case wsOpText, wsOpBinary:
		if f.fin {
			fs.fragment = nil // 新的完整数据帧：丢弃未完成的旧分片（异常容错）
			return d.emitData(stream, inputID, flowID, f, f.opcode, f.payload, false)
		}
		fs.fragment = append([]byte{}, f.payload...)
		fs.fragOpcode = f.opcode
		return nil
	case wsOpContinuation:
		fs.fragment = append(fs.fragment, f.payload...)
		if !f.fin {
			return nil // 分片尚未结束
		}
		op := fs.fragOpcode
		if op == 0 {
			op = wsOpBinary // 未定义起始的延续帧按 binary 兜底
		}
		payload := fs.fragment
		fs.fragment = nil
		return d.emitData(stream, inputID, flowID, f, op, payload, true)
	}
	return nil // 未知 opcode：忽略
}

// emitData emits a completed ws.text / ws.binary message. Direction comes from
// the sender's mask bit; text payloads carry the flattened JSON envelope.
func (d *decoder) emitData(stream pb.Decoder_DecodeV2Server, inputID, flowID string, f wsFrame, opcode byte, payload []byte, fragmented bool) error {
	fs := d.flows[flowID]
	path := "binary_frames"
	if opcode == wsOpText {
		path = "text_frames"
		fs.counts.text++
	} else {
		fs.counts.binary++
	}

	direction := "server_to_client"
	if f.masked {
		direction = "client_to_server"
	}

	body, truncated := truncate(payload, maxBodyBytes)
	value := map[string]any{
		"length":         len(payload),
		"fragmented":     fragmented,
		"text":           string(body),
		"text_truncated": truncated,
	}

	// msg_name 约定（与 plugin.yaml 的 ws.name_message 规则配套）：信封自带
	// type 字段时不写 msg_name，由 name 规则声明式提取（规则优先于硬编码）；
	// 没有 type 字段时（binary、非 JSON 文本）解码器兜底。
	msgName := "text"
	isPush := false
	if opcode == wsOpText {
		if env := parseTextEnvelope(payload); env.Type != "" {
			msgName = ""
			isPush = env.Type == "push"
			value["type"] = env.Type
			value["seq"] = env.Seq
			if env.ErrorCode != 0 {
				value["error_code"] = env.ErrorCode
				value["is_error"] = true
			} else {
				value["is_error"] = false
			}
		}
	} else {
		msgName = "binary"
		value["hex"] = hex.EncodeToString(body)
	}

	draft := event.Draft{
		Type:  "ws.text",
		Value: event.ValueFromMap(value),
		Meta:  event.ValueFromMap(baseMeta(flowID, direction, msgName, frameRole(direction, isPush), isPush)),
		Analysis: event.ValueFromMap(map[string]any{
			"_state_changes": []any{stateChange(flowID, path, fs.counts)},
		}),
	}
	if opcode == wsOpBinary {
		draft.Type = "ws.binary"
	}
	return send(stream, inputID, draft)
}

// emitControl emits a ws.ping / ws.pong / ws.close event. A close frame with a
// payload carries the close code (RFC 6455 §5.5.1).
func (d *decoder) emitControl(stream pb.Decoder_DecodeV2Server, inputID, flowID string, f wsFrame) error {
	fs := d.flows[flowID]
	direction := "server_to_client"
	if f.masked {
		direction = "client_to_server"
	}

	name := "ping"
	switch f.opcode {
	case wsOpPong:
		name = "pong"
	case wsOpClose:
		name = "close"
	}

	value := map[string]any{"length": len(f.payload)}
	if f.opcode == wsOpClose && len(f.payload) >= 2 {
		value["code"] = int64(binary.BigEndian.Uint16(f.payload[:2]))
	}

	return send(stream, inputID, event.Draft{
		Type:  event.EventType("ws." + name),
		Value: event.ValueFromMap(value),
		Meta:  event.ValueFromMap(baseMeta(flowID, direction, name, frameRole(direction, false), false)),
		Analysis: event.ValueFromMap(map[string]any{
			"_state_changes": []any{stateChange(flowID, "control_frames", fs.counts)},
		}),
	})
}

// stateChange builds one _state_changes entry for the per-flow frame counters.
func stateChange(flowID, path string, c flowCount) map[string]any {
	return map[string]any{
		"subject_type": "ws_flow",
		"subject_id":   flowID,
		"op":           "set",
		"path":         path,
		"before":       c.total() - 1,
		"after":        c.total(),
		"version":      c.total(),
	}
}

// handshakeChange projects the WebSocket handshake phase: request opens the
// flow (0→1), response marks it complete (1→2). version is the message count
// within the flow.
func handshakeChange(flowID string, completed bool) map[string]any {
	after := int64(1)
	if completed {
		after = 2
	}
	return map[string]any{
		"subject_type": "ws_flow",
		"subject_id":   flowID,
		"op":           "set",
		"path":         "handshake",
		"before":       after - 1,
		"after":        after,
		"version":      after,
	}
}

// send converts a draft to a DecodeResponseV2 and sends it. ToResponse errors
// surface as an error-terminated response so a bad event never silently dies.
func send(stream pb.Decoder_DecodeV2Server, inputID string, draft event.Draft) error {
	resp, err := draft.ToResponse(inputID)
	if err != nil {
		return stream.Send(&pb.DecodeResponseV2{InputId: inputID, Done: true, Error: err.Error()})
	}
	return stream.Send(resp)
}

// truncate retains at most max bytes, flagging the loss.
func truncate(b []byte, max int) ([]byte, bool) {
	if len(b) > max {
		return b[:max], true
	}
	return b, false
}
