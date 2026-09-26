package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"

	"github.com/OwnSecurityGuard/gametrace/sdk"
	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	"github.com/OwnSecurityGuard/gametrace/sdk/framing"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// lp 线格式（每帧自描述，长度字段宽度与端序逐帧可变）：
//
//	b0 magic=0x4C | b1 version=1 | b2 param(bit7 端序, bits4-3 宽度码) | b3 reserved=0
//	| length(width 字节, 按 param 端序) | payload(JSON 信封, length 字节)
const (
	magic      byte = 0x4C
	version    byte = 0x01
	headerSize      = 4
	paramEndian     = 0x80
	paramWidth      = 0x18
	paramWidthShift = 3
	paramReserved   = 0x67 // bit6 与 bits2-0 保留位，必须为 0
)

var widths = [4]int{1, 2, 4}

var cmdNames = map[int]string{
	1001: "LoginRequest", 1002: "LoginResponse",
	1003: "GetBagRequest", 1004: "GetBagResponse",
	1005: "UseItemRequest", 1006: "UseItemResponse",
	1007: "GatherRequest", 1008: "GatherResponse",
	2001: "PlayerInfoNotify", 2002: "ItemCountNotify", 2003: "ResourceNotify",
	9001: "BadRequest",
}

type envelope struct {
	Cmd       int             `json:"cmd"`
	Seq       int             `json:"seq"`
	ErrorCode int             `json:"error_code,omitempty"`
	ErrorMsg  string          `json:"error_msg,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type lpFrame struct {
	env          envelope
	body         []byte
	littleEndian bool
	width        int
	consumed     int
}

// decoder 持有跨 DecodeRequest 存活的状态。
type decoder struct {
	reasm *framing.Reassembler
}

var dec = &decoder{reasm: framing.NewReassembler()}

func main() {
	// 平台统一隧道模式：注册/心跳/解码帧共用同一条到 registry 的连接。
	sdk.RunRegisterLoopWithOptions(dec.decodePacket, sdk.RegisterOptions{
		AuthToken: os.Getenv("GT_AUTH_TOKEN"),
	})
}

func (d *decoder) decodePacket(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
	seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
	if !ok || len(seg.Payload) == 0 {
		return done(stream, req)
	}

	s := d.reasm.Push(seg)
	for {
		buf := s.Bytes()
		f, err := parseFrame(buf)
		if err != nil {
			// 帧头非法（magic/版本/保留位错位）：丢弃已确认非 lp 的缓冲，
			// 避免整条流被一帧坏数据卡死。
			s.Consume(1)
			continue
		}
		if f == nil {
			break // 不完整，等下一个 segment
		}
		s.Consume(f.consumed)
		if err := emit(f, stream, req); err != nil {
			return err
		}
	}
	return done(stream, req)
}

// parseFrame 返回 nil,nil 表示数据不足；nil,err 表示帧头非法。
func parseFrame(buf []byte) (*lpFrame, error) {
	if len(buf) < headerSize {
		return nil, nil
	}
	if buf[0] != magic {
		return nil, fmt.Errorf("lp: bad magic 0x%02x", buf[0])
	}
	if buf[1] != version {
		return nil, fmt.Errorf("lp: unsupported version %d", buf[1])
	}
	if buf[3] != 0 {
		return nil, fmt.Errorf("lp: reserved byte must be 0, got %d", buf[3])
	}
	param := buf[2]
	if param&paramReserved != 0 {
		return nil, fmt.Errorf("lp: param reserved bits must be 0, got 0x%02x", param)
	}
	code := int((param & paramWidth) >> paramWidthShift)
	if code > 2 {
		return nil, fmt.Errorf("lp: invalid width code %d", code)
	}
	width := widths[code]
	if len(buf) < headerSize+width {
		return nil, nil
	}
	little := param&paramEndian != 0
	var length int
	lenField := buf[headerSize : headerSize+width]
	switch width {
	case 1:
		length = int(lenField[0])
	case 2:
		if little {
			length = int(binary.LittleEndian.Uint16(lenField))
		} else {
			length = int(binary.BigEndian.Uint16(lenField))
		}
	default:
		if little {
			length = int(binary.LittleEndian.Uint32(lenField))
		} else {
			length = int(binary.BigEndian.Uint32(lenField))
		}
	}
	total := headerSize + width + length
	if len(buf) < total {
		return nil, nil
	}
	body := buf[headerSize+width : total]

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		env = envelope{Cmd: 0} // 帧完整但信封不可解析：cmd=0 走 unknown 语义
	}
	return &lpFrame{env: env, body: body, littleEndian: little, width: width, consumed: total}, nil
}

func emit(f *lpFrame, stream pb.Decoder_DecodeV2Server, req *pb.DecodeRequest) error {
	env := f.env
	name, known := cmdNames[env.Cmd]
	if !known {
		name = "unknown"
	}

	role := "response"
	direction := "server_to_client"
	isPush := false
	switch {
	case env.Cmd >= 1000 && env.Cmd < 2000 && env.Cmd%2 == 1:
		role = "request"
		direction = "client_to_server"
	case env.Cmd >= 2000 && env.Cmd < 3000:
		role = "notification"
		isPush = true
	case env.Cmd == 0:
		role = "unknown"
	}

	business := map[string]any{
		"cmd":      int64(env.Cmd),
		"cmd_name": name,
		"seq":      int64(env.Seq),
	}
	if env.ErrorCode != 0 {
		business["error_code"] = int64(env.ErrorCode)
	}
	if env.ErrorMsg != "" {
		business["error_msg"] = env.ErrorMsg
	}
	if len(env.Data) > 0 {
		var data any
		if err := json.Unmarshal(env.Data, &data); err == nil {
			business["data"] = data
		}
	}

	metaFields := map[string]any{
		"direction":     direction,
		"msg_name":      name,
		"role":          role,
		"is_push":       isPush,
		"len_width":     int64(f.width),
		"little_endian": f.littleEndian,
	}
	if env.Cmd == 0 {
		// 帧完整但信封不可解析：记入 Meta 供排查，不打断流。
		metaFields["decode_err"] = "payload is not a valid lp JSON envelope"
	}

	draft := event.Draft{
		Type:  "lp.message",
		Value: event.ValueFromMap(business),
		Meta:  event.ValueFromMap(metaFields),
	}

	resp, err := draft.ToResponse(req.GetInputId())
	if err != nil {
		return err
	}
	return stream.Send(resp)
}

func done(stream pb.Decoder_DecodeV2Server, req *pb.DecodeRequest) error {
	return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true})
}
