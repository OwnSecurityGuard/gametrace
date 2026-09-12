// Package main 是 wesnoth-decoder 解码插件：解析 Battle for Wesnoth 客户端
// <-> wesnothd 服务器的 WML 网络流量（gt.decoder/v2，SDK v0.8.2）。
//
// # 协议真源
//
//	src/network_asio.cpp              （客户端握手 / 帧格式 / 收发）
//	src/server/common/server_base.cpp （服务端握手 / 收发）
//	src/server/common/simple_wml.cpp  （WML 文本语法与 gzip/bzip2 压缩）
//	src/server/wesnothd/server.cpp    （消息分发，顶层 tag 枚举）
//
// # 线格式
//
//	连接：客户端发 4 字节大端握手（0=明文 / 1=请求 TLS），服务端回 4 字节
//	（0x00000000=接受 / 0xFFFFFFFF=拒绝 TLS），每个方向各一次。
//	消息：[4 字节大端长度 N][N 字节 payload]；payload 是 gzip 或 bzip2 压缩的
//	WML 文本（首字节 'B' 为 bzip2，否则 gzip）。
//	WML 文本：根无 tag，属性 key=value 或 key="value" 每行，子节点
//	[tag]...[/tag] 递归嵌套；消息类型 = 根的首个子 tag 名（version/login/...）。
package main

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/OwnSecurityGuard/gt-plugin-sdk/framing"
	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
)

const (
	// serverPort 是 wesnothd 默认监听端口（src/server/wesnothd/server.cpp），
	// 用于判定方向：目的端口是 15000 即上行，源端口是 15000 即下行。
	serverPort = 15000
	// maxFrame 是单帧 payload 长度上限，超过即视为失步（对齐服务端 40MB 文档上限）。
	maxFrame = 64 << 20
	// rawMax 是兜底事件里保留的原始文本/十六进制最大长度。
	rawMax = 8192
)

// 方向常量，写入 _meta.direction（宿主解析进 Context.Direction）。
const (
	dirC2S = "client_to_server"
	dirS2C = "server_to_client"
)

// decoder 持有跨 Decode 调用的流状态：TCP 重组器（帧跨段重组）与
// 每方向流的握手消费标记。宿主可能并发调用，共享状态用互斥锁保护。
type decoder struct {
	reasm      *framing.Reassembler
	mu         sync.Mutex
	handshakes map[string]bool // 方向流 key（FlowKey.String）→ 4 字节握手已消费
}

func newDecoder() *decoder {
	return &decoder{
		reasm:      framing.NewReassembler(),
		handshakes: make(map[string]bool),
	}
}

// Event 是一条解码结果。
type Event struct {
	EventType      string
	SchemaID       string
	Payload        map[string]any // 业务字段（payload 根对象）
	Meta           map[string]any // 元信息（direction 等）
	CorrelationKey string
}

// Decode 把一个捕获帧解成零到多条事件。永不 panic，畸形输入也不返回 error：
// 能解出多少就返回多少，剩余字节等下一个段（契约：malformed-input-safe、
// one-input-may-carry-many-messages）。
func (d *decoder) Decode(req *pb.DecodeRequest) (events []*Event, err error) {
	events = []*Event{} // 即使一条都解不出也返回非 nil
	defer func() {
		if r := recover(); r != nil {
			events = []*Event{}
		}
	}()

	seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
	if !ok {
		// 非 IP 流量或截断帧：正常现象，不是错误。
		return events, nil
	}

	dir := directionOf(seg)
	flowKey := seg.Flow.String() // 方向性流标识

	// 传输层连接生命周期：SYN（新连接或 5-tuple 复用）与 RST（异常终止）
	// 到来时重置本方向流的应用层握手簿记。否则旧连接的 seen=true 残留会让
	// 新连接的头 4 字节握手被误当帧长度（n==0/n==1 → 失步），多连接/重连
	// 场景必踩。必须在空 payload 提前返回之前处理：SYN/RST 段本身不带数据。
	if seg.IsTCP && (seg.Flags.SYN || seg.Flags.RST) {
		d.mu.Lock()
		delete(d.handshakes, flowKey)
		d.mu.Unlock()
	}

	d.mu.Lock()
	seen := d.handshakes[flowKey]
	d.mu.Unlock()

	// 生命周期段（SYN/RST/FIN，无应用数据）也必须交给 Reassembler：SDK 依
	// SYN 重置 sequence base、依 RST/FIN 退役流缓冲。跳过会破坏 5-tuple 复用
	// 时的序列空间对齐（跳变被当作数据空洞，后续段卡在 oob 永不释放）。
	if len(seg.Payload) == 0 {
		d.reasm.Push(seg)
		// 纯 ACK / SYN / FIN / 握手包：无应用数据，正常现象。
		return events, nil
	}

	s := d.reasm.Push(seg)
	for {
		raw := s.Bytes()
		if !seen {
			// 每方向流首 4 字节可能是连接握手（也可能是 mid-stream attach 的
			// 真实帧头）。按值判定：上行 0/1、下行 0/0xFFFFFFFF 视作握手并消费。
			if len(raw) < 4 {
				break // 头部没收全，等下一个段
			}
			if isHandshake(binary.BigEndian.Uint32(raw[:4]), dir) {
				s.Consume(4)
			}
			seen = true
			d.mu.Lock()
			d.handshakes[flowKey] = true
			d.mu.Unlock()
			continue
		}
		if len(raw) < 4 {
			break // 帧头没收全，等下一个段
		}
		n := binary.BigEndian.Uint32(raw[:4])
		if n == 0 || n > maxFrame {
			// 长度非法说明流已失步（抓包漏段或 mid-stream attach）：
			// 丢弃该方向的重排缓冲，等下一次连接/新数据重新对齐。
			d.reasm.Forget(seg.Flow)
			break
		}
		if len(raw) < 4+int(n) {
			break // 负载没收全，等下一个段
		}
		ev := d.decodePayload(raw[4:4+n], seg, dir)
		ev.CorrelationKey = seg.Flow.Canonical() // 方向无关的会话标识
		events = append(events, ev)
		s.Consume(4 + int(n)) // n>0，必然推进，不会死循环
	}
	return events, nil
}

// directionOf 按端口判定方向。端口不可用时（代理类输入没有传输头）返回空串，
// 事件方向留空，由宿主按流/上下文补齐。
func directionOf(seg framing.Segment) string {
	switch {
	case seg.Flow.Dst.Port() == serverPort:
		return dirC2S
	case seg.Flow.Src.Port() == serverPort:
		return dirS2C
	default:
		return ""
	}
}

// isHandshake 判断一帧头 4 字节是否更像连接握手而不是帧长度。只在每方向
// 流的首个 4 字节上咨询一次；真实消息的 gzip 压缩长度不会是 0/1/0xFFFFFFFF。
func isHandshake(v uint32, dir string) bool {
	switch dir {
	case dirC2S: // 客户端→服务端：0=明文，1=请求 TLS
		return v == 0 || v == 1
	case dirS2C: // 服务端→客户端：0=接受，0xFFFFFFFF=拒绝 TLS
		return v == 0 || v == 0xFFFFFFFF
	default:
		return false
	}
}

// decodePayload 解压并解析一帧 WML，产出一条事件。消息类型 = 根首个子 tag 名。
// 解压或解析失败时产出 wesnoth.unknown 兜底事件，保证字节流继续推进。
func (d *decoder) decodePayload(body []byte, seg framing.Segment, dir string) *Event {
	text, ok := decompressWML(body)
	if !ok {
		return newUnknownEvent(dir, "decompress_failed", fmt.Sprintf("0x%x", truncateBytes(body, rawMax)))
	}
	root, err := parseWML(string(text))
	if err != nil || len(root.children) == 0 {
		return newUnknownEvent(dir, "parse_failed", truncate(string(text)))
	}

	msg := root.children[0]
	payload := nodePayload(msg)
	payload["msg_type"] = msg.name // 判别字段，供 name 语义规则提取消息名
	payload["_raw"] = truncate(string(text))
	return &Event{
		EventType: "wesnoth." + msg.name,
		SchemaID:  "wesnoth.message.v1",
		Payload:   payload,
		Meta: map[string]any{
			"direction":      dir,
			"decompressed_len": len(text),
		},
	}
}

// nodePayload 把 WML 节点展平成事件载荷：属性扁平到顶层（字符串值），
// 子节点按 tag 名分组放入 _children。
func nodePayload(n *wmlNode) map[string]any {
	out := map[string]any{}
	for _, a := range n.attrs {
		out[a.key] = a.value
	}
	if len(n.children) > 0 {
		out["_children"] = childrenPayload(n.children)
	}
	return out
}

// childrenPayload 把子节点列表按 tag 名分组：{"unit":[{...}, ...], "side":[...]}，
// 每个子节点递归展平（属性 + 自己的 _children）。
func childrenPayload(children []*wmlNode) map[string]any {
	groups := map[string][]any{}
	for _, c := range children {
		obj := map[string]any{}
		for _, a := range c.attrs {
			obj[a.key] = a.value
		}
		if len(c.children) > 0 {
			obj["_children"] = childrenPayload(c.children)
		}
		groups[c.name] = append(groups[c.name], obj)
	}
	out := make(map[string]any, len(groups))
	for k, v := range groups {
		out[k] = v
	}
	return out
}

// newUnknownEvent 构造兜底事件：保留原因与原始内容，供协议演进排查。
func newUnknownEvent(dir, reason, raw string) *Event {
	return &Event{
		EventType: "wesnoth.unknown",
		SchemaID:  "wesnoth.message.v1",
		Payload: map[string]any{
			"msg_type": "unknown",
			"raw":      raw,
		},
		Meta: map[string]any{
			"direction": dir,
			"reason":    reason,
		},
	}
}

func truncate(s string) string {
	if len(s) > rawMax {
		return s[:rawMax]
	}
	return s
}

func truncateBytes(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}
