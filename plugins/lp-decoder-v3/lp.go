package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// lp 帧定界器与 cmd 字典。线格式依据见 decode.go 文件头协议结论。
//
// 帧 = 0x4c 'L' | 0x01 ver | flags u8 | pad u8(实测恒 0x00，解析不校验) | len | JSON body
// len 宽度：flags&0x10→4B，flags&0x08→2B，否则 1B；端序：flags&0x80→LE，否则 BE。

const (
	frameMagic   = 0x4c
	frameVersion = 0x01
	flagLenLE    = 0x80 // 长度小端
	flagLen2     = 0x08 // 长度 2 字节
	flagLen4     = 0x10 // 长度 4 字节

	// maxBody 是防御性上限：实测最大 body 121B（抓包证据），失步时长度字段
	// 可能是垃圾值（实测出现过 13824 / 1509949440 这类误读），超限即判失步而不是等待。
	maxBody = 1 << 20
)

// cmd 字典（抓包中真实出现的取值；event_type = "lp."+cmd，消息名由 name 规则提取）
const (
	cmdLoginReq     = 1001 // C→S 登录 {account, device_id}
	cmdLoginResp    = 1002 // S→C 登录响应 {player_id, nickname, level} / error_code=4
	cmdHeartbeat    = 1003 // C→S 心跳
	cmdHeartbeatRsp = 1004 // S→C 心跳响应 {items[]} / error_code=3
	cmdUseItemReq   = 1005 // C→S 使用道具 {item_id, count}
	cmdUseItemResp  = 1006 // S→C 使用结果 {item_id, used, remain} / error_code=5..8
	cmdResourceReq  = 1007 // C→S 资源操作 {resource, amount}
	cmdResourceResp = 1008 // S→C 资源结果 {resource, gained}（只有增量，不做状态 set）
	cmdPlayerPush   = 2001 // S→C 玩家档案推送 {player_id, nickname, level, exp, online}
	cmdItemPush     = 2002 // S→C 物品变更推送 {item_id, count, delta}
	cmdCurrencyPush = 2003 // S→C 货币推送 {gold, diamond}（无显式主体）
)

var (
	errFrameIncomplete = errors.New("lp: incomplete frame")
	errFrameDesync     = errors.New("lp: frame desync (bad header or length)")
)

// parseFrame 从流缓冲头部切出一个完整帧体，返回 consumed（含帧头的总字节数）。
// 不足一帧返回 errFrameIncomplete；头部非法或长度超 maxBody 返回 errFrameDesync。
func parseFrame(buf []byte) (body []byte, consumed int, err error) {
	if len(buf) >= 2 && (buf[0] != frameMagic || buf[1] != frameVersion) {
		return nil, 0, fmt.Errorf("%w: magic=0x%02x ver=0x%02x", errFrameDesync, buf[0], buf[1])
	}
	if len(buf) < 4 {
		return nil, 0, errFrameIncomplete // 至少要有 magic/ver/flags/pad
	}
	flags := buf[2]
	width := 1
	switch {
	case flags&flagLen4 != 0:
		width = 4
	case flags&flagLen2 != 0:
		width = 2
	}
	hdr := 4 + width
	if len(buf) < hdr {
		return nil, 0, errFrameIncomplete
	}
	var bodyLen uint64
	switch {
	case width == 1:
		bodyLen = uint64(buf[4])
	case flags&flagLenLE != 0:
		if width == 2 {
			bodyLen = uint64(binary.LittleEndian.Uint16(buf[4:]))
		} else {
			bodyLen = uint64(binary.LittleEndian.Uint32(buf[4:]))
		}
	case width == 2:
		bodyLen = uint64(binary.BigEndian.Uint16(buf[4:]))
	default:
		bodyLen = uint64(binary.BigEndian.Uint32(buf[4:]))
	}
	if bodyLen > maxBody {
		return nil, 0, fmt.Errorf("%w: body length %d exceeds cap %d", errFrameDesync, bodyLen, maxBody)
	}
	if uint64(len(buf)-hdr) < bodyLen {
		return nil, 0, errFrameIncomplete
	}
	return buf[hdr : hdr+int(bodyLen)], hdr + int(bodyLen), nil
}

// buildFrame 按线格式编码一个帧（测试固件用）。
func buildFrame(body []byte, flags byte) []byte {
	width := 1
	switch {
	case flags&flagLen4 != 0:
		width = 4
	case flags&flagLen2 != 0:
		width = 2
	}
	le := flags&flagLenLE != 0
	out := make([]byte, 4+width+len(body))
	out[0], out[1], out[2], out[3] = frameMagic, frameVersion, flags, 0x00
	n := uint64(len(body))
	switch {
	case width == 1:
		out[4] = byte(n)
	case width == 2 && le:
		binary.LittleEndian.PutUint16(out[4:], uint16(n))
	case width == 2:
		binary.BigEndian.PutUint16(out[4:], uint16(n))
	case le:
		binary.LittleEndian.PutUint32(out[4:], uint32(n))
	default:
		binary.BigEndian.PutUint32(out[4:], uint32(n))
	}
	copy(out[4+width:], body)
	return out
}
