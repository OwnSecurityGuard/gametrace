package main

import "encoding/binary"

// RFC 6455 §5.2 frame opcodes.
const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA
)

// wsFrame is one parsed WebSocket frame; the payload is already unmasked.
type wsFrame struct {
	opcode  byte
	fin     bool
	masked  bool // MASK 位：客户端必须掩码、服务端不得掩码（RFC 6455 §5.3），用于方向判定
	payload []byte
}

// parseFrame tries to read exactly one WebSocket frame from the front of buf.
//
// It returns the frame, the number of bytes consumed, and whether a complete
// frame was available. When ok is false the caller must NOT consume anything:
// the bytes so far are an incomplete frame (more segments will complete it)
// or an implausible frame that the flow-level byte cap will eventually drop.
func parseFrame(buf []byte) (f wsFrame, consumed int, ok bool) {
	if len(buf) < 2 {
		return wsFrame{}, 0, false
	}
	f.fin = buf[0]&0x80 != 0
	f.opcode = buf[0] & 0x0F
	f.masked = buf[1]&0x80 != 0
	length := int64(buf[1] & 0x7F)

	header := 2
	switch length {
	case 126:
		if len(buf) < 4 {
			return wsFrame{}, 0, false
		}
		length = int64(binary.BigEndian.Uint16(buf[2:4]))
		header = 4
	case 127:
		if len(buf) < 10 {
			return wsFrame{}, 0, false
		}
		length = int64(binary.BigEndian.Uint64(buf[2:10]))
		header = 10
	}

	maskOffset := header
	if f.masked {
		header += 4
	}
	if length < 0 || length > 1<<30 {
		return wsFrame{}, 0, false
	}
	total := header + int(length)
	if len(buf) < total {
		return wsFrame{}, 0, false
	}

	f.payload = buf[header:total]
	if f.masked {
		mask := buf[maskOffset : maskOffset+4]
		unmasked := make([]byte, len(f.payload))
		for i, b := range f.payload {
			unmasked[i] = b ^ mask[i%4]
		}
		f.payload = unmasked
	}
	return f, total, true
}
