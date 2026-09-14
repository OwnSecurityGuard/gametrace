package main

import "encoding/binary"

// lp（length-prefixed framing）模板协议的帧格式。⚠️ 这是为示例发明的通用帧，
// 不是任何真实游戏协议。以下格式划分也非通用真理：
//
//	b0:      magic = 0x4C ('L')     协议识别，防串台
//	b1:      version = 1            版本（演进预留）
//	b2:      param:  bit7   端序 0=大端 1=小端
//	                 bits4-3 长度字段宽度码 0→1B 1→2B 2→4B
//	                 bits2-0 保留位（必须为 0）
//	b3:      reserved = 0           预留字节，解析器校验它以早期发现错位
//	then:    length 字段（width 字节，按 param 端序）
//	then:    payload（JSON，length 字节）
//
// 模板要点（各项目差异最大、最容易照抄出错的部分）：
//   - 长度字段可以是 1/2/4 字节，端序可能是大端或小端——必须逐帧从帧头读取，
//     不能假设固定宽度/端序；
//   - 长度字段本身可能跨越 TCP 段（4 字节宽时尤其常见），必须等完整长度字段再读 payload；
//   - 魔数只用于尽早丢弃无关流量；不要把它当成"协议一定是这样"的依据。
//
// 照抄本模板前，请先用 sample_bytes_plugin 确认真实字节流再定义自己的帧头。
const (
	lpMagic           byte = 0x4C // 'L'
	lpVersion         byte = 0x01
	lpHeaderSize      int  = 4
	lpParamEndian     byte = 0x80 // 置位=小端
	lpParamWidth      byte = 0x18 // bits 4..3
	lpParamWidthShift      = uint(3)
)

// maxPayloadBytes caps a plausible payload length. Larger values are treated
// as corrupt (a corrupt length field must not stall the flow forever).
const maxPayloadBytes = 1 << 24 // 16MB

// lpWidths maps the 2-bit width code to the length field size in bytes.
var lpWidths = [4]int{1, 2, 4}

// lpHeader is the decoded frame header carrying the wire facts for the Meta
// channel. Endianness and width belong to the wire layer, not to business data.
type lpHeader struct {
	version  byte
	little   bool // 长度字段端序（0=大端 1=小端）
	lenWidth int  // 长度字段字节数：1 | 2 | 4
	length   int  // payload 长度
}

// lpFrame is one complete frame: header plus the (possibly truncated) payload.
type lpFrame struct {
	header  lpHeader
	payload []byte
}

// parseFrame tries to read exactly one lp frame from the front of buf.
//
// It returns the frame, the number of bytes consumed, and whether a complete
// frame was available. When ok is false the caller must NOT consume anything:
// the bytes so far are an incomplete frame (more segments will complete it),
// a corrupt header (bad magic/version/reserved/length), or a foreign protocol
// that the flow-level byte cap will eventually drop.
func parseFrame(buf []byte) (f lpFrame, consumed int, ok bool) {
	if len(buf) < lpHeaderSize {
		return lpFrame{}, 0, false // 帧头不完整：等待更多段
	}
	if buf[0] != lpMagic || buf[1] != lpVersion || buf[3] != 0 {
		return lpFrame{}, 0, false // 非本协议 / 版本不符 / 保留位错位
	}

	hdr := lpHeader{
		version:  buf[1],
		little:   buf[2]&lpParamEndian != 0,
		lenWidth: 0,
	}
	code := int((buf[2] & lpParamWidth) >> lpParamWidthShift)
	if code > 2 {
		return lpFrame{}, 0, false // 非法宽度码：视为不可解析
	}
	hdr.lenWidth = lpWidths[code]

	// 长度字段可能跨段（宽 2/4 字节时常见）：先等足宽度字节再解码。
	lenField := lpHeaderSize + hdr.lenWidth
	if len(buf) < lenField {
		return lpFrame{}, 0, false
	}
	length := 0
	switch hdr.lenWidth {
	case 1:
		length = int(buf[lpHeaderSize])
	case 2:
		if hdr.little {
			length = int(binary.LittleEndian.Uint16(buf[lpHeaderSize:lenField]))
		} else {
			length = int(binary.BigEndian.Uint16(buf[lpHeaderSize:lenField]))
		}
	default:
		if hdr.little {
			length = int(binary.LittleEndian.Uint32(buf[lpHeaderSize:lenField]))
		} else {
			length = int(binary.BigEndian.Uint32(buf[lpHeaderSize:lenField]))
		}
	}
	if length < 0 || length > maxPayloadBytes {
		return lpFrame{}, 0, false // 不合理的长度：视为不可解析
	}
	hdr.length = length

	total := lenField + length
	if len(buf) < total {
		return lpFrame{}, 0, false // payload 不完整：等待更多段
	}

	return lpFrame{header: hdr, payload: buf[lenField:total]}, total, true
}
