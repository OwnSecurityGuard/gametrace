package main

// SDK 语义规则（Protocol Semantic Rule）在 GameTrace 侧的执行适配器。
//
// 设计前提：规则由插件在 manifest.semantic_rules 声明，平台只负责执行，
// GameTrace 自身不定义任何规则（第一层自有引擎 pkg/protocol、pkg/analyze 已删除）。
//
// 三种效果：
//   annotate: 给事件打语义标签（request/response/notification/error）→ Meta.semantic
//   pair:     按键配对请求与响应 → Trace.CorrelationID / CausationID
//   name:     从 payload 提取消息名 → Meta.msg_name
//
// 宿主默认语义：annotate 是插件覆盖默认值的途径，不是获得 request/response 的前提——
// 事件没有角色标签时，宿主按方向推导（client_to_server→request、
// server_to_client→response），显式角色优先于默认值。
//
// 状态：SDK 的语义规则层已就绪，随迁移入 cmd/gt-pipeline/ 并在
// capture_task.go 接线（annotate/pair/name 三种效果的宿主侧执行）。
//
// 历史：曾有一个 extract 效果（一拆多产出子事件），已于 2026-09-18 删除——
// 拆多事件本就是 DecodeV2 响应的原生能力（r.Events 是切片），走解码器比走
// 规则声明更完整（子事件有 Meta、能过规则、能带状态变更）。

import (
	"log/slog"
	"sync"
	"time"

	sdk "github.com/OwnSecurityGuard/gametrace/sdk"
	sdkevent "github.com/OwnSecurityGuard/gametrace/sdk/event"
	"github.com/OwnSecurityGuard/gametrace/sdk/rule"

	"gametrace/pkg/event"
	"gametrace/pkg/plugin"
)

const (
	// metaKeySemantic 是 annotate 效果写入的 meta 字段名。
	metaKeySemantic = "semantic"

	// 方向取值词汇表，与 pkg/decode（fields.go）保持一致：
	// dispatcher 已按五元组/serverPort 推断并允许插件 _meta.direction 覆盖。
	dirClientToServer = "client_to_server"
	dirServerToClient = "server_to_client"

	// metaKeyMsgName 是 name 效果写入的 meta 字段名（规则从 payload 提取的消息名）。
	metaKeyMsgName = "msg_name"

	// metaKeyCorrelationKey 是 pair 覆盖 Trace.CorrelationID 前，
	// 插件声明的业务关联键（Draft.CorrelationKey）的转存位置。
	metaKeyCorrelationKey = "corr_key"

	// maxPendingPairs 是待配对池上限，防止异常流量下无限增长。
	maxPendingPairs = 4096

	// pendingTTL 是待配对项的过期时间，超时视为配对失败并丢弃。
	pendingTTL = 30 * time.Second
)

// pendingPair 是一条等待对侧事件的 pair 命中。
//
// 注意：evt 持有的是内存中的事件指针。若对侧事件迟迟不到、本事件已被 flush
// 落库，则后续配对成功时对该指针的修改不会回写到库里（已知限制）。
// 实际请求-响应间隔通常远小于 flush 周期，同批次内可正常生效。
type pendingPair struct {
	hit rule.PairHit
	evt *event.Event
	// conn 是事件所属连接实例（EventContext.ConnID）。配对池按连接分片，
	// 这里冗余存一份用于显式校验两侧同源（分片键已带 conn，属双保险）。
	conn string
	at   time.Time
}

// semanticEngine 执行当前插件声明的语义规则。
// 规则随解码器重建（插件注册/重启/热切换）重新载入。
type semanticEngine struct {
	logger   *slog.Logger
	registry *plugin.RegistryServer

	mu      sync.RWMutex
	rules   []rule.Rule
	pending map[string]pendingPair
	inserts int
}

func newSemanticEngine(logger *slog.Logger, registry *plugin.RegistryServer) *semanticEngine {
	return &semanticEngine{
		logger:   logger,
		registry: registry,
		pending:  make(map[string]pendingPair),
	}
}

// refreshRules 从注册中心重新载入插件的 semantic_rules。
// 插件未声明规则或解析失败时降级为空规则集（no-op），不影响抓包主链路。
func (e *semanticEngine) refreshRules(owner, name string) {
	if e.registry == nil {
		return
	}
	raw, err := e.registry.GetPluginManifestFor(owner, name)
	if err != nil {
		e.logger.Warn("semantic rules: fetch manifest failed", "plugin", name, "error", err)
		e.setRules(nil)
		return
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		e.logger.Warn("semantic rules: parse manifest failed", "plugin", name, "error", err)
		e.setRules(nil)
		return
	}
	if issues := rule.RulesReport(m.SemanticRules); len(issues) > 0 {
		for _, iss := range issues {
			e.logger.Warn("semantic rules: declaration issue",
				"plugin", name, "rule_id", iss.RuleID, "path", iss.Path,
				"severity", iss.Severity, "message", iss.Message)
		}
	}
	e.setRules(m.SemanticRules)
	e.logger.Info("semantic rules refreshed", "plugin", name, "owner", owner, "rules", len(m.SemanticRules))
}

func (e *semanticEngine) setRules(rules []rule.Rule) {
	e.mu.Lock()
	e.rules = rules
	e.mu.Unlock()
}

func (e *semanticEngine) getRules() []rule.Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.rules
}

// enrichSemantics 执行语义规则，结果就地写入 ev（annotate 标签、pair 关联、name 消息名）。
// 规则为空时跳过求值，但 host 默认的 request/response 推导（applyDefaultSemantics）
// 始终执行——插件不写任何 annotate 规则也能拿到角色。
func (e *semanticEngine) enrichSemantics(ev *event.Event) {
	if ev == nil {
		return
	}
	rules := e.getRules()
	if len(rules) == 0 {
		e.logger.Debug("semantic: no rules to evaluate", "event_id", ev.Identity.ID, "event_type", ev.Identity.Type)
		e.applyDefaultSemantics(ev)
		return
	}

	// 规则求值视图：payload 合并 _meta。新模型下 Meta 独立传输（不在 payload），
	// 合并后规则仍可引用 _meta.msg_name 等路径，与旧插件（payload 内 _meta）行为一致。
	evalVal := withMetaObject(ev.Payload.Value, ev.Meta)
	sdkVal, err := toSDKValue(evalVal)
	if err != nil {
		e.logger.Debug("semantic: convert payload", "event_id", ev.Identity.ID, "error", err)
		return
	}
	res, err := rule.Evaluate(rules, sdkVal)
	if err != nil {
		e.logger.Warn("semantic: evaluate", "event_id", ev.Identity.ID, "error", err)
		return
	}
	// 诊断：观测 Evaluate 是否产出 name/semantic，以及写入后 meta 是否含 msg_name。
	e.logger.Info("semantic eval",
		"event_id", ev.Identity.ID,
		"event_type", ev.Identity.Type,
		"n_rules", len(rules),
		"n_names", len(res.Names),
		"names", func() []string {
			out := make([]string, 0, len(res.Names))
			for _, n := range res.Names {
				out = append(out, n.Value)
			}
			return out
		}(),
		"n_sems", len(res.Semantics),
	)

	e.applyMsgNames(ev, res.Names)
	e.applySemantics(ev, res.Semantics)
	e.applyPairs(ev, res.Pairs)
	e.applyDefaultSemantics(ev)

	if mn, ok := ev.Meta.Get("msg_name"); ok {
		e.logger.Info("semantic msg_name set", "event_id", ev.Identity.ID, "msg_name", mn.String())
	} else {
		e.logger.Info("semantic msg_name absent", "event_id", ev.Identity.ID)
	}
}

// withMetaObject 把独立的 Meta（新模型）并进求值视图的 _meta 键。
// 旧插件（payload 已含 _meta）或 Meta 为空时原样返回，不覆盖、不新增空 _meta。
func withMetaObject(payload, meta event.Value) event.Value {
	if payload.Kind != event.Object {
		return payload
	}
	if _, has := payload.Object["_meta"]; has {
		return payload
	}
	if meta.Kind != event.Object || len(meta.Object) == 0 {
		return payload
	}
	merged := make(map[string]event.Value, len(payload.Object)+1)
	for k, v := range payload.Object {
		merged[k] = v
	}
	merged["_meta"] = meta
	return event.ValueObject(merged)
}

// applyMsgNames 把 name 规则提取的消息名写入 Meta（规则优先于解码器硬编码）。
// 取首个命中（规则声明顺序）；旧插件 payload 里已有 _meta 时同步写入，保证旧数据展示一致。
func (e *semanticEngine) applyMsgNames(ev *event.Event, names []rule.NameHit) {
	if len(names) == 0 {
		return
	}
	name := names[0].Value
	if name == "" {
		return
	}

	// 新模型：消息名写入 ev.Meta.msg_name。
	meta := map[string]event.Value{}
	if cur, ok := ev.Meta.AsObject(); ok {
		for k, v := range cur {
			meta[k] = v
		}
	}
	meta[metaKeyMsgName] = event.ValueString(name)
	ev.Meta = event.ValueObject(meta)

	// 旧模型兼容：payload 已带 _meta 时同步写 msg_name 键。
	if cur, ok := ev.Payload.Value.Get("_meta"); ok && cur.Object != nil {
		oldMeta := make(map[string]event.Value, len(cur.Object)+1)
		for k, v := range cur.Object {
			oldMeta[k] = v
		}
		oldMeta[metaKeyMsgName] = event.ValueString(name)
		root := make(map[string]event.Value, len(ev.Payload.Value.Object)+1)
		if ev.Payload.Value.Object != nil {
			for k, v := range ev.Payload.Value.Object {
				root[k] = v
			}
		}
		root["_meta"] = event.ValueObject(oldMeta)
		ev.Payload.Value = event.ValueObject(root)
	}
}

// applySemantics 把 annotate 命中的语义标签写进独立 Meta（新模型）。
// 旧插件 payload 里已有 _meta 时同步写入，保证旧数据展示不受影响。
func (e *semanticEngine) applySemantics(ev *event.Event, sems []rule.Semantic) {
	if len(sems) == 0 {
		return
	}
	labels := make([]event.Value, 0, len(sems))
	for _, s := range sems {
		labels = append(labels, event.ValueString(string(s)))
	}

	// 新模型：语义标签写入 ev.Meta.semantic。
	meta := map[string]event.Value{}
	if cur, ok := ev.Meta.AsObject(); ok {
		for k, v := range cur {
			meta[k] = v
		}
	}
	meta[metaKeySemantic] = event.ValueArray(labels)
	ev.Meta = event.ValueObject(meta)

	// 旧模型兼容：payload 已带 _meta 时同步写 semantic 键。
	if cur, ok := ev.Payload.Value.Get("_meta"); ok && cur.Object != nil {
		oldMeta := make(map[string]event.Value, len(cur.Object)+1)
		for k, v := range cur.Object {
			oldMeta[k] = v
		}
		oldMeta[metaKeySemantic] = event.ValueArray(labels)
		root := make(map[string]event.Value, len(ev.Payload.Value.Object)+1)
		if ev.Payload.Value.Object != nil {
			for k, v := range ev.Payload.Value.Object {
				root[k] = v
			}
		}
		root["_meta"] = event.ValueObject(oldMeta)
		ev.Payload.Value = event.ValueObject(root)
	}
}

// applyDefaultSemantics 落实宿主默认的 request/response 语义：
// 事件没有角色标签（request/response/notification——error 是状态属性，不算角色）时，
// 按方向推导：client_to_server → request、server_to_client → response，已有标签
// 追加而不覆盖。插件显式 annotate 出任何角色、或在 Meta 自报角色，默认值不再介入；
// 方向判不出来（unknown/缺失）且无角色时保持原样，不猜。
// 代价是纯服务端推送若无人标注，会被默认标成 response——需要 notification
// 的插件仍需自己写 annotate 规则，这是方向能提供的全部信息。
func (e *semanticEngine) applyDefaultSemantics(ev *event.Event) {
	var label rule.Semantic
	switch ev.Context.Direction {
	case dirClientToServer:
		label = rule.SemRequest
	case dirServerToClient:
		label = rule.SemResponse
	default:
		return
	}
	labels := semanticLabels(ev)
	for _, l := range labels {
		if l == string(rule.SemRequest) || l == string(rule.SemResponse) || l == string(rule.SemNotification) {
			return // 已有角色（annotate 或插件自报），默认值让位
		}
	}
	all := make([]rule.Semantic, 0, len(labels)+1)
	for _, l := range labels {
		all = append(all, rule.Semantic(l))
	}
	e.applySemantics(ev, append(all, label))
}

// semanticLabels 读出事件当前的 meta.semantic 标签集（兼容数组与单字符串两种历史形状）。
func semanticLabels(ev *event.Event) []string {
	v, ok := ev.Meta.Get(metaKeySemantic)
	if !ok {
		return nil
	}
	switch v.Kind {
	case event.Array:
		out := make([]string, 0, len(v.Array))
		for _, item := range v.Array {
			if s, ok := item.AsString(); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case event.String:
		if v.Str != "" {
			return []string{v.Str}
		}
	}
	return nil
}

// applyPairs 处理 pair 命中：与本规则下等待中的对侧事件配对。
// 配对成功后双方写同一个 CorrelationID（取请求方事件 ID），响应方写
// CausationID 指向请求方。角色由 pair 规则 sides 的声明顺序决定：
// Side 0 = 请求方、Side 1 = 响应方（SDK 新 pair 模型：每侧自带 key，
// 不再有规则级 key），与到达先后无关。
// 配对池按**连接实例**分片：分片键 = ConnID + 规则 ID + 配对键。
// 协议里 per-connection 自增的配对键（每条连接各自从 1 开始的 seq）只在连接内
// 唯一，不做分片时两条连接的同值键会互相覆盖、跨连接错配（F6）。
// 没有连接标识的事件（无五元组 / 未派生 conn_id）退化为全局池，行为与修复前一致。
func (e *semanticEngine) applyPairs(ev *event.Event, hits []rule.PairHit) {
	if len(hits) == 0 {
		return
	}
	conn := ev.Context.ConnID
	for _, h := range hits {
		mapKey := conn + "\x00" + h.RuleID + "\x00" + h.Key

		e.mu.Lock()
		prev, ok := e.pending[mapKey]
		if ok && prev.evt != nil && prev.conn == conn && rule.MatchPair(prev.hit, h) {
			delete(e.pending, mapKey)
			e.mu.Unlock()

			// MatchPair 保证两侧 side 不同（pair 恰好 2 side，Side ∈ {0,1}）：
			// Side 0 是请求方、Side 1 是响应方；以请求方事件 ID 作为 trace id。
			var req, resp *event.Event
			if h.Side == 0 {
				req, resp = ev, prev.evt
			} else {
				req, resp = prev.evt, ev
			}
			corr := string(req.Identity.ID)
			// CorrelationKey 是业务会话标识（一次对局 / 一次事务），pair 的
			// 分组键是更紧的一问一答。两者不同粒度，覆盖前把业务键转存到
			// Meta.corr_key，避免插件声明的业务会话标识被静默吃掉。
			preserveCorrelationKey(req, corr)
			preserveCorrelationKey(resp, corr)
			req.Trace.CorrelationID = corr
			resp.Trace.CorrelationID = corr
			resp.Trace.CausationID = req.Identity.ID
			continue
		}
		if len(e.pending) >= maxPendingPairs {
			e.evictExpired(time.Now())
		}
		e.pending[mapKey] = pendingPair{hit: h, evt: ev, conn: conn, at: time.Now()}
		e.inserts++
		if e.inserts%512 == 0 {
			e.evictExpired(time.Now())
		}
		e.mu.Unlock()
	}
}

// preserveCorrelationKey 在 pair 覆盖 Trace.CorrelationID 前，把插件声明的
// 业务关联键转存到 Meta.corr_key。原值为空或与新键相同时不写。
func preserveCorrelationKey(ev *event.Event, pairKey string) {
	old := ev.Trace.CorrelationID
	if old == "" || old == pairKey {
		return
	}
	meta := map[string]event.Value{}
	if cur, ok := ev.Meta.AsObject(); ok {
		for k, v := range cur {
			meta[k] = v
		}
	}
	meta[metaKeyCorrelationKey] = event.ValueString(old)
	ev.Meta = event.ValueObject(meta)
}

// evictExpired 丢弃超时的待配对项；调用方必须持有写锁。
func (e *semanticEngine) evictExpired(now time.Time) {
	for k, p := range e.pending {
		if now.Sub(p.at) > pendingTTL {
			delete(e.pending, k)
		}
	}
	// 仍超上限时整体丢弃，宁可丢配对也不能无限增长。
	if len(e.pending) >= maxPendingPairs {
		e.pending = make(map[string]pendingPair)
		return
	}
}

// toSDKValue 把 GameTrace 的 Value 转成 SDK 的 Value。
// 两者结构同构但类型不同，走 JSON 互转是最稳的桥（不依赖内部字段布局）。
func toSDKValue(v event.Value) (sdkevent.Value, error) {
	raw, err := v.MarshalJSON()
	if err != nil {
		return sdkevent.Value{}, err
	}
	return sdkevent.ValueFromJSON(raw)
}
