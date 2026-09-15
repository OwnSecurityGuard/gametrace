package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"gametrace/pkg/event"
)

// 连接实例标识（ConnectionID）的派生：把「网络事实」翻译成「业务连接事实」。
//
// 五元组只用来**识别**连接边界，不用来**标识**连接实例——两者混为一谈会导致
// 重连复用同一五元组时，新旧两代连接拿到同一个 conn_id，pair 待配对池把两代
// 请求混在一起（见 semanticEngine.applyPairs 的分片键）。
//
// 生命周期规则（TCP）：
//   - SYN（未带 ACK）= 主动打开 → 分配新的连接实例，即使五元组与上一代相同
//   - FIN = 半关闭，两侧都发过 FIN 才退役
//   - RST = 立即退役
//   - 退役后同五元组再来的包（残留包 / 抓包中途加入）→ 新实例
//
// UDP 无连接生命周期，退化为「五元组 + 空闲超时」。
// 移动代理在 Packet.Metadata 自带真实 conn_id（真正的业务连接事实），原样保留。

const (
	// tcpIdleTTL 是 TCP 连接实例的空闲退役时间。远大于 pair 待配对 TTL（30s），
	// 保证长静默连接恢复后不会误判成新连接而丢掉配对。
	tcpIdleTTL = 10 * time.Minute

	// udpIdleTTL 是 UDP「连接」的空闲退役时间（UDP 无 FIN/RST，只能按空闲判定）。
	udpIdleTTL = 2 * time.Minute

	// maxTrackedConns 是同时跟踪的连接实例上限，防止异常流量下无限增长。
	maxTrackedConns = 8192
)

// connInst 是一个连接实例（同一五元组的某一"代"）。
type connInst struct {
	id       string
	lastSeen time.Time
	finFrom  map[string]bool // 已发 FIN 的端点，双方到齐即退役（TCP 半关闭）
}

// connTracker 维护五元组 → 连接实例的当前映射，并分配 ConnectionID。
// 每个抓包任务一个实例（连接标识只在会话内有意义）。
type connTracker struct {
	mu      sync.Mutex
	seq     uint64
	active  map[string]*connInst
	assigns int
}

func newConnTracker() *connTracker {
	return &connTracker{active: make(map[string]*connInst)}
}

// assign 为包派生 conn_id（幂等：已有则保留），写入 Metadata["conn_id"]。
func (c *connTracker) assign(pkt *event.Packet) {
	switch pkt.Protocol {
	case "tcp", "udp":
	default:
		return
	}
	// L1：业务侧（移动代理）已给出真实连接身份，直接信任。
	if v, ok := pkt.Metadata["conn_id"].(string); ok && v != "" {
		return
	}
	if !pkt.Src.IsValid() || !pkt.Dst.IsValid() {
		return
	}
	key := canonicalTuple(pkt)
	// 用处理时刻而非包时间戳：pcap 回放的历史时间戳会让空闲判定立刻过期。
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	inst := c.active[key]
	switch {
	case pkt.Protocol == "tcp" && pkt.TCPFlags.SYN && !pkt.TCPFlags.ACK:
		// 主动打开：总是新一代，重连复用同一五元组也拿新 ID。
		inst = c.newInstanceLocked(key)
	case inst == nil:
		// 抓包中途加入（没看到握手）或上一代已退役后的残留包。
		inst = c.newInstanceLocked(key)
	}
	inst.lastSeen = now
	if pkt.Metadata == nil {
		pkt.Metadata = map[string]any{}
	}
	pkt.Metadata["conn_id"] = inst.id

	if pkt.Protocol == "tcp" {
		switch {
		case pkt.TCPFlags.RST:
			delete(c.active, key)
		case pkt.TCPFlags.FIN:
			inst.finFrom[pkt.Src.String()] = true
			if len(inst.finFrom) >= 2 {
				delete(c.active, key)
			}
		}
	}

	c.assigns++
	if c.assigns%256 == 0 {
		c.sweepLocked(now)
	}
}

// newInstanceLocked 分配新连接实例；调用方必须持锁。
func (c *connTracker) newInstanceLocked(key string) *connInst {
	c.seq++
	inst := &connInst{
		id:      key + "#" + strconv.FormatUint(c.seq, 10),
		finFrom: map[string]bool{},
	}
	c.active[key] = inst
	return inst
}

// sweepLocked 清理空闲连接实例；调用方必须持锁。
func (c *connTracker) sweepLocked(now time.Time) {
	for key, inst := range c.active {
		ttl := tcpIdleTTL
		if strings.HasPrefix(key, "udp:") {
			ttl = udpIdleTTL
		}
		if now.Sub(inst.lastSeen) > ttl {
			delete(c.active, key)
		}
	}
	// 仍超上限时整体丢弃：宁可让存量连接重新派生 ID，也不能无限增长。
	if len(c.active) > maxTrackedConns {
		c.active = make(map[string]*connInst)
	}
}

// canonicalTuple 返回双向排序后的规范五元组（同一连接两个方向结果相同）。
// 只用于识别连接边界，不作为连接实例标识（实例标识带 "#N" 代号后缀）。
func canonicalTuple(pkt *event.Packet) string {
	a, b := pkt.Src.String(), pkt.Dst.String()
	if b < a {
		a, b = b, a
	}
	// 协议不同则连接归一维度不同，故以 "protocol:..." 为前缀（tcp:/udp:）。
	return pkt.Protocol + ":" + a + "<->" + b
}
