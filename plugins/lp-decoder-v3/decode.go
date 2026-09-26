package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	"github.com/OwnSecurityGuard/gametrace/sdk/framing"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// ============================================================
// lp 协议处理结论（§1.1 五问，依据 = 会话 8998 抓包 20 个数据帧逐一复核）
//
// 1. 定界：TCP 长度前缀帧。帧头 4 字节固定 + 变宽长度字段：
//      [0] 0x4c 'L' 魔数  [1] 0x01 版本  [2] flags u8  [3] pad 0x00
//      长度宽度：flags&0x10→4B，flags&0x08→2B，否则 1B
//      长度端序：flags&0x80→LE，否则 BE（实测 flags ∈ {0x00,0x08,0x10,0x80,0x88,0x90}）
//      长度值 = 随后 JSON body 的字节数。无分隔符、非定长。
// 2. 重组：SDK framing.Reassembler（每连接每方向隔离）；无应用层握手
//      （SYN 段无 L4 数据，实测）；魔数/版本不符视为字节流失步 → Forget 该方向缓冲。
// 3. 业务字节：ExtractL7 剥链路层/IP/TCP 后，整个 L4 数据即帧流；帧体为 UTF-8 JSON：
//      {"cmd":int,"seq":int[,"data":{...}][,"error_code":int,"error_msg":str]}
//      payload 原样透传 JSON（ValueFromJSONMap），不增删字段。
// 4. JSON 字段来源：即线格式 JSON 本身，字段名与线上完全一致；cmd 保持数字枚举不转名
//      （消息名由 name 规则 key=cmd 提取）。
// 5. push：存在——cmd ∈ {2001,2002,2003} 且 seq=0，服务端主动下发（证据：抓包无前置请求）。
//    压缩/加密：不存在（帧体均为明文 UTF-8 JSON，20/20 复核）。
//
// direction 依据：服务器固定监听 8998（会话事实）；dst 端口=8998 → client_to_server，
// src 端口=8998 → server_to_client；两者都不是则留空由宿主补齐，不猜。
//
// 实体状态（发现制，证据在 plugin.yaml 规则注释与各 cmd 处）：
//   2001 player 档案推送 → player.level/exp/online（subject 用 data.player_id）
//   2002 item 变更推送   → item.count（subject 用 data.item_id；delta 是增量说明，set 用绝对值 count）
//   2003 货币推送        → player.currency.gold/diamond；线上无显式主体，
//        归属依据 = 本连接（FlowKey.Canonical，方向无关）上 1002 登录响应或 2001 推送
//        出现过的 player_id；尚未建立则不产出状态（不猜主体）。
//   1008 resource 响应只有 gained 增量、无绝对值 → 不产出 set（after 必须是权威新值）。
// ============================================================

const serverPort = 8998

// decoder 持有跨 DecodeRequest 存活的状态。Reassembler 必须是生命周期级
// 单例（按 FlowKey 每连接每方向隔离、并发安全）；每次新建会导致跨段消息拼不回来。
type decoder struct {
	reasm *framing.Reassembler

	// playerIDs 记录每条连接（FlowKey.Canonical，方向无关）上出现过的 player_id，
	// 用于 2003 货币推送的实体归属（协议本身不带主体，见文件头结论第 5 点）。
	mu        sync.Mutex
	playerIDs map[string]string
}

var dec = &decoder{reasm: framing.NewReassembler(), playerIDs: map[string]string{}}

// Decode 处理一个链路层帧：剥封装 → 重组 → 切帧 → 产出事件草稿。
// 与 decodePacket 分开，便于全链路单测不经 gRPC stream。
func (d *decoder) Decode(req *pb.DecodeRequest) ([]event.Draft, error) {
	seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
	if !ok {
		return nil, nil // 非 IP / 抓包截断等，无应用层内容，不算错误
	}
	// Push 对空 payload 的 SYN/FIN/RST 段同样有效（缓冲簿记由 Reassembler 内部处理），不得提前过滤。
	// 插件自维护的每连接状态要跟着连接生命周期走：SYN=新连接、RST=重置，
	// 清空该会话（两方向）的货币主体，避免重连后沿用上一条连接的 player_id。
	if seg.Flags.SYN || seg.Flags.RST {
		d.forgetFlow(seg.Flow)
	}
	s := d.reasm.Push(seg)

	var drafts []event.Draft
	for {
		buf := s.Bytes()
		body, n, err := parseFrame(buf)
		if err == errFrameIncomplete {
			break // 残帧：等下一段
		}
		if err != nil {
			// 魔数/版本不符或长度越界：字节流失步，丢弃该方向缓冲并上抛（宿主记为解码错误，可观测）。
			d.reasm.Forget(seg.Flow)
			d.forgetFlow(seg.Flow)
			return drafts, err
		}
		s.Consume(n)
		draft, err := d.toDraft(body, seg)
		if err != nil {
			return drafts, fmt.Errorf("cmd frame: %w", err)
		}
		drafts = append(drafts, draft)
	}
	return drafts, nil
}

func (d *decoder) forgetFlow(f framing.FlowKey) {
	d.mu.Lock()
	delete(d.playerIDs, f.Canonical())
	d.mu.Unlock()
}

// toDraft 把一个完整帧体转成事件草稿。
func (d *decoder) toDraft(body []byte, seg framing.Segment) (event.Draft, error) {
	var head struct {
		Cmd int64 `json:"cmd"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return event.Draft{}, err
	}
	payload, err := event.ValueFromJSONMap(body)
	if err != nil {
		return event.Draft{}, err
	}

	meta := map[string]event.Value{}
	if dir := directionOf(seg); dir != "" {
		meta["direction"] = event.ValueString(dir)
	}

	draft := event.Draft{
		Type:  event.EventType("lp." + strconv.FormatInt(head.Cmd, 10)),
		Value: payload,
	}
	if len(meta) > 0 {
		draft.Meta = event.ValueObject(meta)
	}
	if scs := d.stateChanges(body, seg); len(scs) > 0 {
		draft.Analysis = event.ValueObject(map[string]event.Value{
			"_state_changes": event.ValueArray(scs),
		})
	}
	return draft, nil
}

// directionOf 依据固定服务端口 8998 判方向；判不了返回 ""（宿主补齐）。
func directionOf(seg framing.Segment) string {
	switch {
	case seg.Flow.Dst.Port() == serverPort:
		return "client_to_server"
	case seg.Flow.Src.Port() == serverPort:
		return "server_to_client"
	default:
		return ""
	}
}

// stateChanges 只对有证据的三种推送 cmd 产出状态投影（见文件头结论）。
func (d *decoder) stateChanges(body []byte, seg framing.Segment) []event.Value {
	var msg struct {
		Cmd  int64 `json:"cmd"`
		Data struct {
			PlayerID string `json:"player_id"`
			Nickname string `json:"nickname"`
			Level    *int64 `json:"level"`
			Exp      *int64 `json:"exp"`
			Online   *bool  `json:"online"`
			ItemID   *int64 `json:"item_id"`
			Count    *int64 `json:"count"`
			Gold     *int64 `json:"gold"`
			Diamond  *int64 `json:"diamond"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil
	}

	flow := seg.Flow.Canonical()
	switch msg.Cmd {
	case cmdLoginResp: // 1002：登录响应带 player_id，建立本连接的货币主体
		if msg.Data.PlayerID != "" {
			d.mu.Lock()
			d.playerIDs[flow] = msg.Data.PlayerID
			d.mu.Unlock()
		}
	case cmdPlayerPush: // 2001：玩家档案推送
		id := msg.Data.PlayerID
		if id == "" {
			return nil
		}
		d.mu.Lock()
		d.playerIDs[flow] = id
		d.mu.Unlock()
		var out []event.Value
		if msg.Data.Level != nil {
			out = append(out, stateSet("player", id, "player.level", event.ValueInt(*msg.Data.Level)))
		}
		if msg.Data.Exp != nil {
			out = append(out, stateSet("player", id, "player.exp", event.ValueInt(*msg.Data.Exp)))
		}
		if msg.Data.Online != nil {
			out = append(out, stateSet("player", id, "player.online", event.ValueBool(*msg.Data.Online)))
		}
		return out
	case cmdItemPush: // 2002：物品数量变更（count 为绝对值，delta 只是说明）
		if msg.Data.ItemID == nil || msg.Data.Count == nil {
			return nil
		}
		return []event.Value{
			stateSet("item", strconv.FormatInt(*msg.Data.ItemID, 10), "item.count", event.ValueInt(*msg.Data.Count)),
		}
	case cmdCurrencyPush: // 2003：货币推送，主体取本连接已建立的 player_id
		d.mu.Lock()
		id := d.playerIDs[flow]
		d.mu.Unlock()
		if id == "" {
			return nil // 主体未建立：不猜，跳过状态投影
		}
		var out []event.Value
		if msg.Data.Gold != nil {
			out = append(out, stateSet("player", id, "currency.gold", event.ValueInt(*msg.Data.Gold)))
		}
		if msg.Data.Diamond != nil {
			out = append(out, stateSet("player", id, "currency.diamond", event.ValueInt(*msg.Data.Diamond)))
		}
		return out
	}
	return nil
}

// stateSet 构造 op=set 的状态变更（op/after 契约见 sdk event.StateChange 文档）。
func stateSet(subjectType, subjectID, path string, after event.Value) event.Value {
	return event.ValueObject(map[string]event.Value{
		"subject_type": event.ValueString(subjectType),
		"subject_id":   event.ValueString(subjectID),
		"op":           event.ValueString("set"),
		"path":         event.ValueString(path),
		"after":        after,
	})
}
