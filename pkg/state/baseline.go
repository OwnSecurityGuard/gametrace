// Package state 实现插件 State 层的运行时投影：
// 从事件 payload 的 _state_changes 声明中提取 StateChange，并维护实体基线
// （before/after 富化），供 state_changes 投影表写入。
//
// 这是 Contract state 层的宿主侧运行时，与语义分析/证据图无关。
package state

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// maxStateChangesPerEvent 是单个事件允许进入投影的变更条数上限。
// 插件在信任边界之外：一个事件里塞进海量变更会同时打爆内存、基线与写库，
// 因此超限部分直接截断并计数（见 Truncated），不做静默膨胀。
const maxStateChangesPerEvent = 4096

// baselineTTL 是实体基线的保鲜期：超过这么久没被再更新过，就当作没有旧值
//（下次上报算首见）。只有 ScopePeer 需要它——基线跨会话共享后，一个把三天前的值
// 当作「变更前」的富化，比老老实实标成首见更容易误导人。ScopeSession 由会话结束
// 时的 ForgetScope 回收，通常碰不到。
const baselineTTL = 6 * time.Hour

// maxBaselineEntries 是进程内保留的实体基线条数上限，超过时丢 LastSeen 最旧的。
// 基线从「每会话一份、随会话消亡」改成进程级共享后，无界增长就成了必然：
// 这个上限是那次改动的必要配套，不是假想的未来。
const maxBaselineEntries = 100_000

// StateChange.op 的取值（与 event.StateChange.Validate 允许的集合一致）。
// 三种 op 的基线写入语义不同，见 Apply。
const (
	opDelete = "delete"
	opMerge  = "merge"
)

// Scope 决定实体基线的隔离边界。
type Scope int

const (
	// ScopeSession 按抓包会话隔离：同一会话内的连续包共享基线，换会话/重解码从零开始。
	// 默认值。重放同一批流量（pcap 回放、离线重解码）时必须用它，否则重放会被判成
	//「值没变」而整批抑制掉，历史凭空缺一块。
	ScopeSession Scope = iota
	// ScopePeer 按「对端」隔离，跨会话、跨重连延续：同一客户端对同一服务端的实体，
	// 上一轮抓包看到的终值就是这一轮的 before，而不是整份数据视图重新标成首见。
	// 需要事件带 PeerKey（见 decode.peerKeyFromPacket），缺失时退回 ScopeSession。
	ScopePeer
)

func (s Scope) String() string {
	if s == ScopePeer {
		return "peer"
	}
	return "session"
}

// ParseScope 解析作用域配置，未知值返回 ScopeSession（默认）。
func ParseScope(v string) Scope {
	if v == "peer" {
		return ScopePeer
	}
	return ScopeSession
}

// EntityKey 唯一确定一个实体基线的隔离上下文。
//
// Scope 是「这个实体属于谁」的稳定标识（会话 ID 或对端标识），不是行主键；
// FlowID 只在 ScopeSession 下参与（对端作用域下连接本身就在身份里，再叠一层
// 临时端口 hash 会让重连变成换实体）。
type EntityKey struct {
	Scope       string
	FlowID      string
	SubjectType string
	SubjectID   string
}

// EntityBaseline 是某个实体在某一时刻的完整状态快照。
type EntityBaseline struct {
	Key       EntityKey
	Version   int64
	State     map[string]event.Value
	FirstSeen time.Time
	LastSeen  time.Time
}

// BaselineStore 维护按上下文隔离的实体基线。
type BaselineStore interface {
	// Get 查询实体基线；不存在时返回 (nil, false)。
	Get(key EntityKey) (*EntityBaseline, bool)
	// Put 写入或更新实体基线。
	Put(base *EntityBaseline)
}

// MemoryBaselineStore 是内存中的实体基线存储，带条数上限。
type MemoryBaselineStore struct {
	mu         sync.RWMutex
	items      map[EntityKey]*EntityBaseline
	maxEntries int
	// sweepAt 是下一次淘汰的触发水位。淘汰后抬到上限的 9/8，避免刚清理完
	// 又被下一次 Put 触发一次全表排序。
	sweepAt int
	evicted int64
}

// NewMemoryBaselineStore 创建内存基线存储。
func NewMemoryBaselineStore() *MemoryBaselineStore {
	return &MemoryBaselineStore{
		items:      make(map[EntityKey]*EntityBaseline),
		maxEntries: maxBaselineEntries,
		sweepAt:    maxBaselineEntries,
	}
}

// Get 查询实体基线。
func (m *MemoryBaselineStore) Get(key EntityKey) (*EntityBaseline, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.items[key]
	if !ok {
		return nil, false
	}
	return b, true
}

// Put 写入实体基线；超出条数上限时淘汰 LastSeen 最旧的实体。
func (m *MemoryBaselineStore) Put(base *EntityBaseline) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[base.Key] = base
	if m.maxEntries > 0 && len(m.items) > m.sweepAt {
		m.evictLocked()
	}
}

// ForgetScope 删除某个作用域下的全部基线，返回删除条数。
// 会话结束时由调用方回收该会话的基线（ScopePeer 下不适用：作用域是对端，不是会话）。
func (m *MemoryBaselineStore) ForgetScope(scope string) int {
	if scope == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.items {
		if k.Scope == scope {
			delete(m.items, k)
			n++
		}
	}
	return n
}

// Len 返回当前保留的实体基线数。
func (m *MemoryBaselineStore) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}

// Evicted 返回因超出上限被淘汰的实体数（非 0 即发生过富化信息丢失，必须可见）。
func (m *MemoryBaselineStore) Evicted() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.evicted
}

// evictLocked 在超过上限时丢弃 LastSeen 最旧的若干实体。
// 调用方必须持写锁。
func (m *MemoryBaselineStore) evictLocked() {
	over := len(m.items) - m.maxEntries
	if over <= 0 {
		m.sweepAt = m.maxEntries + m.maxEntries/8
		return
	}
	keys := make([]EntityKey, 0, len(m.items))
	for k := range m.items {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return m.items[keys[i]].LastSeen.Before(m.items[keys[j]].LastSeen)
	})
	for _, k := range keys[:over] {
		delete(m.items, k)
	}
	m.evicted += int64(over)
	m.sweepAt = m.maxEntries + m.maxEntries/8
	slog.Warn("baseline store evicted oldest entities", "evicted", over, "kept", len(m.items))
}

// Option 配置 BaselineManager。
type Option func(*BaselineManager)

// WithScope 设置实体基线的隔离边界，见 Scope。
func WithScope(s Scope) Option {
	return func(bm *BaselineManager) { bm.scope = s }
}

// BaselineManager 负责维护实体基线并生成带 before/after 的 StateChange。
//
// 并发约定：Apply 设计为单写者（抓包主循环的后处理 goroutine 独占调用），
// mu 只是防止调用方误用时静默写坏 map，不是吞吐手段。需要多会话并行时，
// 各会话事件本身按 Scope 隔离，共享同一个实例即可。
type BaselineManager struct {
	store BaselineStore
	scope Scope
	ttl   time.Duration
	mu    sync.Mutex
	// 以下计数在持锁期间自增，读取走对应的访问器。
	noopSuppressed int64
	invalidSkipped int64
	truncated      int64
}

// NewBaselineManager 创建基线管理器。store 为 nil 时使用内存实现。
// 默认 ScopeSession（重放友好）；跨会话延续用 WithScope(ScopePeer)。
func NewBaselineManager(store BaselineStore, opts ...Option) *BaselineManager {
	if store == nil {
		store = NewMemoryBaselineStore()
	}
	bm := &BaselineManager{store: store, ttl: baselineTTL}
	for _, opt := range opts {
		opt(bm)
	}
	return bm
}

// Scope 返回当前隔离边界。
func (bm *BaselineManager) Scope() Scope { return bm.scope }

// NoopSuppressed 返回被抑制的「无实际变化」变更条数（见 Apply 的 noop 抑制）。
func (bm *BaselineManager) NoopSuppressed() int64 {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.noopSuppressed
}

// InvalidSkipped 返回因不满足最小必填项被跳过的变更条数。
func (bm *BaselineManager) InvalidSkipped() int64 {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.invalidSkipped
}

// Truncated 返回因超过 maxStateChangesPerEvent 被丢弃的变更条数。
func (bm *BaselineManager) Truncated() int64 {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.truncated
}

// ForgetScope 回收某个作用域下的全部实体基线，返回删除条数。
// 会话结束时调用（只在 ScopeSession 下有意义）。
func (bm *BaselineManager) ForgetScope(scope string) int {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if f, ok := bm.store.(interface{ ForgetScope(string) int }); ok {
		return f.ForgetScope(scope)
	}
	return 0
}

// Len 返回当前保留的实体基线数；store 不支持统计时返回 -1。
// 基线段进程级共享后，这是判断「有没有在涨」的唯一入口（日志/指标用）。
func (bm *BaselineManager) Len() int {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if l, ok := bm.store.(interface{ Len() int }); ok {
		return l.Len()
	}
	return -1
}

// Apply 将事件中的 StateChange 与当前基线对比，生成 EnrichedStateChange 并更新基线。
// 无基线时 BeforeResolved 为 false，不会伪造旧值。
//
// noop 抑制：基线上已有该 path 的真实旧值、且本次上报的 after 与之相等时，这次上报没有
// 造成任何变化（批量同步里大量同值回吐），直接丢弃不产出变更记录。仅在有真实旧值时判定
// ——首见实体没有基线，插件的首次赋值声明必须保留。
//
// 校验策略：逐条跳过非法变更（计数 + Warn），不让一条脏数据带走同一事件里其它合法变更。
// 原实现改为返回 error 会整批丢失，而基线已被前面几条推进，造成「事件在、变更丢、
// 之后的 before 从用户没看见的值出发」的静默错位。
func (bm *BaselineManager) Apply(ev *event.Event, sessionID string) ([]store.EnrichedStateChange, error) {
	if ev == nil {
		return nil, nil
	}

	changes := ev.ExtractStateChanges()
	if len(changes) == 0 {
		return nil, nil
	}
	// 信任边界：插件上报的条数不可信，超限只保留前 N 条。
	var truncated int64
	if len(changes) > maxStateChangesPerEvent {
		truncated = int64(len(changes) - maxStateChangesPerEvent)
		changes = changes[:maxStateChangesPerEvent]
	}

	bm.mu.Lock()
	defer bm.mu.Unlock()

	if truncated > 0 {
		bm.truncated += truncated
		slog.Warn("state changes truncated by limit",
			"event_id", ev.Identity.ID, "limit", maxStateChangesPerEvent, "dropped", truncated)
	}

	// declared 保留变更在插件声明数组里的原始位置：seq 要能反映上报次序，
	// 而不是「过滤掉非法条目之后的第几条」。
	// 1 基：0 留给「未设置」（迁移前的老行、外部写入），同时与同一份 JSON 里同样 1 基的
	// Change.Seq（视图内序号）保持一致，避免「视图第 1 条 / 消息内第 0 条」这种错位展示。
	type declared struct {
		seq int
		sc  event.StateChange
	}
	valid := make([]declared, 0, len(changes))
	for i, sc := range changes {
		if err := sc.Validate(); err != nil {
			bm.invalidSkipped++
			slog.Warn("skip invalid state change", "event_id", ev.Identity.ID, "error", err)
			continue
		}
		valid = append(valid, declared{seq: i + 1, sc: sc})
	}
	if len(valid) == 0 {
		return nil, nil
	}

	flowID := ev.Context.FlowID
	if flowID == "" {
		flowID = extractFlowIDFromEvent(ev)
	}
	scope, flowID := bm.entityScope(ev, sessionID, flowID)

	var result []store.EnrichedStateChange
	// touched 收集本事件里被改动的实体，循环结束统一回写：一个事件常带同一实体的几十条
	// 字段变更，逐条 Put 等于把同一个指针重复塞几十次、每次过一遍 store 的锁。
	// 同时它也是实体在本事件内的读写落点 —— 后续变更必须看到前一条的结果，
	// 而这一点不能依赖「内存 store 返回同一指针」这种实现细节。
	touched := make(map[EntityKey]*EntityBaseline, 4)
	now := ev.Identity.Timestamp

	for _, item := range valid {
		sc := item.sc
		key := EntityKey{
			Scope:       scope,
			FlowID:      flowID,
			SubjectType: sc.SubjectType,
			SubjectID:   sc.SubjectID,
		}

		base, pending := touched[key]
		exists := pending
		if !pending {
			var ok bool
			base, ok = bm.store.Get(key)
			if ok {
				if bm.ttl > 0 && now.Sub(base.LastSeen) > bm.ttl {
					// 过期基线：当作首见，避免把一个很久以前的值当作「变更前」。
					base, exists = nil, false
				} else {
					exists = true
				}
			}
			if base == nil {
				base = &EntityBaseline{
					Key:       key,
					State:     make(map[string]event.Value),
					FirstSeen: now,
					LastSeen:  now,
				}
			}
		}

		enriched := store.EnrichedStateChange{
			StateChange:    sc,
			EventID:        ev.Identity.ID,
			FlowID:         flowID,
			Timestamp:      now,
			EntityVersion:  sc.Version,
			BeforeResolved: false,
			AfterResolved:  false,
			Seq:            item.seq,
		}

		// 按 path 记录 before：基线里该 path 有值才算 resolved（首见不伪造旧值）。
		if exists {
			if oldVal, ok := base.State[sc.Path]; ok {
				enriched.Before = oldVal
				enriched.BeforeResolved = true
			}
		}

		// effective 是本次上报对基线的「有效新值」：
		//   - set：就是 after 本身。
		//   - merge：after 只是片段，必须与基线里的已有对象浅合并后才代表这次之后的真实值。
		//     （用片段做抑制判断会永远不等，重复 merge 同值会源源不断产出假变化；
		//      用片段整块覆盖则会把本次没提及的字段从基线抹掉。）
		//     merge 的 after 必为 Object 由 StateChange.Validate 保证。
		//   - delete：after 不参与语义（删除只由 op 决定），有效值归零。基线里经
		//     Validate 后不可能存 Null，所以「删掉再设回原值」不会被抑制误判。
		// 下游（state_changes 表、FieldHistory.LastValue 等）消费的就是这个有效新值：
		// merge 记录合并后的完整对象而不是插件片段，delete 行 after 恒为 null。
		effective := sc.After
		switch sc.Op {
		case opMerge:
			if oldVal, ok := base.State[sc.Path]; ok && oldVal.Kind == event.Object {
				if merged, err := oldVal.Merge(sc.After); err == nil {
					effective = merged
				}
			}
		case opDelete:
			effective = event.Value{}
		}
		enriched.After = effective

		// 无变化抑制：见 Apply 文档。被抑制时基线也不动（值本就没变）。
		if enriched.BeforeResolved && enriched.Before.Equal(effective) {
			bm.noopSuppressed++
			continue
		}

		// 写基线。删除只由 op==delete 决定（set 成 null 已在 Validate 层拒绝）。
		// delete 必须把 path 移除：残留旧值会让后续 before 撒谎，
		// 并让「删掉再设回原值」被上面的抑制误判成没变化。
		if sc.Op == opDelete {
			delete(base.State, sc.Path)
		} else {
			base.State[sc.Path] = effective
			enriched.AfterResolved = true
		}

		base.LastSeen = now
		if sc.Version > base.Version {
			base.Version = sc.Version
		}
		touched[key] = base

		result = append(result, enriched)
	}

	// 回写：每实体一次（delete 也要写，否则换持久化 store 时删不掉、LastSeen 停滞）。
	for _, base := range touched {
		bm.store.Put(base)
	}

	return result, nil
}

// entityScope 返回本次事件下实体的隔离上下文 (scope, flow)：
//   - ScopeSession：会话 + 流；
//   - ScopePeer：对端标识，不再叠 flow（见 EntityKey 注释）。
//
// 对端标识缺失（方向判不出来）时退回会话维度：宁可少延续，也不能把两个不同客户端
// 的实体并成一个。
func (bm *BaselineManager) entityScope(ev *event.Event, sessionID, flowID string) (scope, flow string) {
	if bm.scope == ScopePeer {
		if peer := ev.Context.PeerKey; peer != "" {
			return peer, ""
		}
	}
	return sessionID, flowID
}

// extractFlowIDFromEvent 从 Event 提取 flow_id：优先 Context，其次 _meta，最后顶层 payload。
func extractFlowIDFromEvent(ev *event.Event) string {
	if ev == nil {
		return ""
	}
	if ev.Context.FlowID != "" {
		return ev.Context.FlowID
	}
	obj, ok := ev.Payload.Value.AsObject()
	if !ok {
		return ""
	}
	if meta, ok := obj["_meta"]; ok {
		if metaObj, ok := meta.AsObject(); ok {
			if v, ok := metaObj["flow_id"]; ok {
				if s, ok := v.AsString(); ok {
					return s
				}
			}
		}
	}
	if v, ok := obj["flow_id"]; ok {
		if s, ok := v.AsString(); ok {
			return s
		}
	}
	return ""
}
