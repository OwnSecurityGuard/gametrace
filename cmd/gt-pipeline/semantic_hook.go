package main

// SDK 语义规则（Protocol Semantic Rule）在 GameTrace 侧的执行适配器。
//
// 设计前提：规则由插件在 manifest.semantic_rules 声明，平台只负责执行，
// GameTrace 自身不定义任何规则（第一层自有引擎 pkg/protocol、pkg/analyze 已删除）。
//
// 三种效果：
//   annotate: 给事件打语义标签（request/response/notification/error）→ payload._meta.semantic
//   pair:     按键配对请求与响应 → Trace.CorrelationID / CausationID
//   extract:  从数组/对象字段拆出子事件 → 子事件 Identity.ParentID 指向父事件
//
// 状态：gt-plugin-sdk v0.7.0 已发布，随迁移入 cmd/gt-pipeline/ 并在
// capture_task.go 接线（annotate/pair/extract 三种效果的宿主侧执行）。

import (
	"log/slog"
	"sync"
	"time"

	sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
	sdkevent "github.com/OwnSecurityGuard/gt-plugin-sdk/event"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/rule"

	"gametrace/pkg/event"
	"gametrace/pkg/plugin"
)

const (
	// metaKeySemantic 是 annotate 效果写入的 meta 字段名。
	metaKeySemantic = "semantic"

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
	at  time.Time
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

// enrichSemantics 执行语义规则，返回需要额外写入的子事件（extract 产出，已带 ParentID）。
// 插件未声明规则时是廉价的空返回。
func (e *semanticEngine) enrichSemantics(ev *event.Event) []*event.Event {
	rules := e.getRules()
	if len(rules) == 0 || ev == nil {
		return nil
	}

	// 规则求值视图：payload 合并 _meta。新模型下 Meta 独立传输（不在 payload），
	// 合并后规则仍可引用 _meta.msg_name 等路径，与旧插件（payload 内 _meta）行为一致。
	evalVal := withMetaObject(ev.Payload.Value, ev.Meta)
	sdkVal, err := toSDKValue(evalVal)
	if err != nil {
		e.logger.Debug("semantic: convert payload", "event_id", ev.Identity.ID, "error", err)
		return nil
	}
	res, err := rule.Evaluate(rules, sdkVal)
	if err != nil {
		e.logger.Warn("semantic: evaluate", "event_id", ev.Identity.ID, "error", err)
		return nil
	}

	e.applySemantics(ev, res.Semantics)
	e.applyPairs(ev, res.Pairs)
	return e.buildChildren(ev, res.Children)
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

// applyPairs 处理 pair 命中：与本规则下等待中的对侧事件配对，
// 配对成功时双方写同一个 CorrelationID，后到者写 CausationID 指向先到者。
func (e *semanticEngine) applyPairs(ev *event.Event, hits []rule.PairHit) {
	if len(hits) == 0 {
		return
	}
	for _, h := range hits {
		mapKey := h.RuleID + "\x00" + h.Key

		e.mu.Lock()
		prev, ok := e.pending[mapKey]
		if ok && prev.evt != nil && rule.MatchPair(prev.hit, h) {
			delete(e.pending, mapKey)
			e.mu.Unlock()

			// 先到者视为请求方：其事件 ID 充当这一组消息的 trace id。
			corr := string(prev.evt.Identity.ID)
			prev.evt.Trace.CorrelationID = corr
			ev.Trace.CorrelationID = corr
			ev.Trace.CausationID = prev.evt.Identity.ID
			continue
		}
		if len(e.pending) >= maxPendingPairs {
			e.evictExpired(time.Now())
		}
		e.pending[mapKey] = pendingPair{hit: h, evt: ev, at: time.Now()}
		e.inserts++
		if e.inserts%512 == 0 {
			e.evictExpired(time.Now())
		}
		e.mu.Unlock()
	}
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

// buildChildren 把 extract 产出的子事件转成 GameTrace 事件，
// 继承父事件的 Session/Source/Context，ParentID 指向父事件。
func (e *semanticEngine) buildChildren(parent *event.Event, children []rule.Child) []*event.Event {
	if len(children) == 0 {
		return nil
	}
	out := make([]*event.Event, 0, len(children))
	for _, c := range children {
		val, err := toGTValue(c.Value)
		if err != nil {
			e.logger.Warn("semantic: convert child value", "rule", c.RuleID, "error", err)
			continue
		}
		child := &event.Event{
			Identity: event.NewIdentity(
				parent.Identity.SessionID,
				event.EventType(c.EventType),
				c.SchemaID,
				parent.Identity.Source,
			),
			Trace: event.TraceContext{
				CorrelationID: parent.Trace.CorrelationID,
				OriginID:      parent.Identity.ID,
			},
			Context: parent.Context,
			Payload: event.Payload{SchemaID: c.SchemaID, Value: val},
		}
		child.Identity.ParentID = parent.Identity.ID
		out = append(out, child)
	}
	return out
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

// toGTValue 把 SDK 的 Value 转回 GameTrace 的 Value。
func toGTValue(v sdkevent.Value) (event.Value, error) {
	raw, err := v.MarshalJSON()
	if err != nil {
		return event.Value{}, err
	}
	return event.ValueFromJSON(raw)
}
