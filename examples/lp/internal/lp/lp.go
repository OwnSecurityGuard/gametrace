// Package lp implements a length-prefixed framing (LPF) wire format for the
// examples/lp simulator. It is a deliberately generic template protocol: the
// framing is header + length field + payload, but the length field width and
// its endianness are chosen PER FRAME so the paired decoder has to cope with
// every variant (1/2/4 bytes, big/little endian).
//
// The wire format is INVENTED for this example. It is NOT a real game
// protocol. Real projects must inspect their own captured bytes first
// (sample_bytes_plugin) and define their own magic / header / length
// semantics — copying this header verbatim is exactly the kind of mistake the
// template is meant to prevent.
package lp

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Wire format (每帧均携带完整帧头，长度字段可跨 TCP 段拆分):
//
//	b0:      magic = 0x4C ('L')     协议识别，防串台
//	b1:      version = 1            版本（演进预留）
//	b2:      param:  bit7   端序 0=大端 1=小端
//	                 bits4-3 长度字段宽度码 0→1B 1→2B 2→4B
//	                 bits2-0 保留位（必须为 0）
//	b3:      reserved = 0           预留，解码器校验以早期发现错位
//	then:    length 字段（width 字节，按 param 端序）
//	then:    payload（JSON，length 字节）
const (
	Magic           byte = 0x4C // 'L'
	Version         byte = 0x01
	HeaderSize      int  = 4
	paramEndian     byte = 0x80    // 置位=小端
	paramWidth      byte = 0x18    // bits 4..3
	paramWidthShift      = uint(3) // 宽度码偏移（码值→字节数：[1,2,4]）
)

// widths maps the 2-bit width code to the length field size in bytes.
var widths = [4]int{1, 2, 4}

// Frame is one decoded length-prefixed message.
type Frame struct {
	Payload      []byte // JSON envelope bytes
	LittleEndian bool
	Width        int // 1 | 2 | 4
	Length       int // len(Payload)
}

// FrameOptions controls how EncodeFrame writes the length field.
type FrameOptions struct {
	Width        int // 1 | 2 | 4；0 表示自动按 payload 长度选择
	LittleEndian bool
}

// pickWidth bounds-check width and applies the auto rule.
func pickWidth(o FrameOptions, payloadLen int) int {
	switch {
	case o.Width == 1 || o.Width == 2 || o.Width == 4:
		return o.Width
	case payloadLen <= 0xFF:
		return 1
	case payloadLen <= 0xFFFF:
		return 2
	}
	return 4
}

// EncodeFrame builds one lp frame: header + length field + payload.
func EncodeFrame(payload []byte, o FrameOptions) []byte {
	width := pickWidth(o, len(payload))

	param := byte(0)
	if o.LittleEndian {
		param |= paramEndian
	}
	switch width {
	case 2:
		param |= 1 << paramWidthShift
	case 4:
		param |= 2 << paramWidthShift
	}

	out := make([]byte, 0, HeaderSize+width+len(payload))
	out = append(out, Magic, Version, param, 0)
	out = appendLength(out, len(payload), width, o.LittleEndian)
	return append(out, payload...)
}

// appendLength writes the length field with the requested width/endianness.
func appendLength(dst []byte, n int, width int, little bool) []byte {
	switch width {
	case 1:
		return append(dst, byte(n))
	case 2:
		var raw [2]byte
		if little {
			binary.LittleEndian.PutUint16(raw[:], uint16(n))
		} else {
			binary.BigEndian.PutUint16(raw[:], uint16(n))
		}
		return append(dst, raw[:]...)
	default:
		var raw [4]byte
		if little {
			binary.LittleEndian.PutUint32(raw[:], uint32(n))
		} else {
			binary.BigEndian.PutUint32(raw[:], uint32(n))
		}
		return append(dst, raw[:]...)
	}
}

// ReadFrame reads one complete lp frame from r. It is the simulator-side
// counterpart of the decoder's parseFrame — both implement the same wire
// format independently (the decoder module cannot import this package).
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	if hdr[0] != Magic {
		return Frame{}, fmt.Errorf("lp: bad magic 0x%02x", hdr[0])
	}
	if hdr[1] != Version {
		return Frame{}, fmt.Errorf("lp: unsupported version %d", hdr[1])
	}
	if hdr[3] != 0 {
		return Frame{}, fmt.Errorf("lp: reserved byte must be 0, got %d", hdr[3])
	}

	code := int((hdr[2] & paramWidth) >> paramWidthShift)
	if code > 2 {
		return Frame{}, fmt.Errorf("lp: invalid width code %d", code)
	}
	width := widths[code]
	little := hdr[2]&paramEndian != 0

	lenField := make([]byte, width)
	if _, err := io.ReadFull(r, lenField); err != nil {
		return Frame{}, err
	}
	var length int
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

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, err
	}
	return Frame{
		Payload:      payload,
		LittleEndian: little,
		Width:        width,
		Length:       length,
	}, nil
}
