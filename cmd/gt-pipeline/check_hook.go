package main

// check_hook.go — 项目级「检查规则」(check rule) 在 GameTrace 流水线侧的执行器。
//
// 与 semantic_hook.go 的区别：语义规则由插件在 manifest 声明、产出 annotate/pair/name
// 富化；检查规则由用户在项目内配置，命中后 ① 落库到会话的 session_alerts（平台端 Web
// 回看「是哪些数据导致了通知」的数据源，所有会话都记）② 尽力向「抓到该数据的探针」下发
// 一条富通知（触发记录 + 各方向触发前最近 N 条解码记录），供探针本地详情页展示。
//
// 设计约束：
//   - inspect 只在 run() 主循环 goroutine 上被调用（processDecoded 内），事件全局有序，
//     故每方向滑动窗口无需加锁。
//   - 下发告警经 probe.Manager.SendAlert → sendAndWait，最长阻塞 ~10s；主循环绝不能内联，
//     否则 decodedCh(64)→decodeCh(1024) 连环堵塞甚至丢包。因此命中后只做「快照 + 非阻塞
//     入队」，序列化与下发交给单 worker goroutine。
//   - 同规则·同会话冷却去重（lastFired），避免逐事件刷屏。

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gametrace/pkg/checkrule"
	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// notifyQueueSize 是命中通知的有界队列长度；满则丢弃并计数（桌面告警是尽力而为）。
const notifyQueueSize = 64

// alertTimeFormat 与 gt-mcp 的 formatEventTime 对齐（微秒精度 RFC3339）。
const alertTimeFormat = "2006-01-02T15:04:05.000000Z07:00"

// notifyJob 是一次命中在主循环侧拍下的快照（仅持事件指针，序列化推迟到 worker）。
//
// 事件在 enrichSemantics 之后不再被改写，故 worker 异步读取这些指针是安全的。
type notifyJob struct {
	alertID string
	rule    checkrule.CheckRule
	trigger *event.Event
	// ctx 是各方向「触发前最近 N 条」事件（归一方向 → 旧→新有序），已按 rule.Context() 裁好。
	ctx map[string][]*event.Event
}

// checkEngine 在每条解码事件上求值项目检查规则，命中则异步下发告警。
type checkEngine struct {
	logger    *slog.Logger
	sessionID string
	rules     []checkrule.CheckRule // Active 过滤后，构造即不可变
	// maxContext 是所有规则里 Context() 的最大值，决定滑动窗口每方向的容量。
	maxContext int
	notify     func(alertID string, b checkrule.AlertBundle)

	// window 是每方向滑动窗口（归一方向 → 最近 maxContext 条，旧→新）。仅主循环访问，无锁。
	window map[string][]*event.Event

	ch     chan notifyJob
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// stopped 防止 stop() 关闭 ch 后仍有 inspect 入队（send-on-closed-channel panic）。
	stopped atomic.Bool

	mu        sync.Mutex
	lastFired map[string]time.Time
	dropped   int
}

// newCheckEngine 构造检查引擎。rules 应已 Active 过滤；notify 为下发闭包（不可为 nil）。
func newCheckEngine(logger *slog.Logger, sessionID string, rules []checkrule.CheckRule, notify func(string, checkrule.AlertBundle)) *checkEngine {
	maxCtx := 0
	for _, r := range rules {
		if c := r.Context(); c > maxCtx {
			maxCtx = c
		}
	}
	return &checkEngine{
		logger:     logger,
		sessionID:  sessionID,
		rules:      rules,
		maxContext: maxCtx,
		notify:     notify,
		window:     make(map[string][]*event.Event),
		ch:         make(chan notifyJob, notifyQueueSize),
		lastFired:  make(map[string]time.Time),
	}
}

// start 启动 worker goroutine（幂等）。
func (e *checkEngine) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			select {
			case <-ctx.Done():
				// 排空已在队列里的命中后退出（尽力交付，但不无限等）。
				for {
					select {
					case job := <-e.ch:
						e.deliver(job)
					default:
						return
					}
				}
			case job := <-e.ch:
				e.deliver(job)
			}
		}
	}()
}

// stop 停止 worker。close(ch) 安全：inspect 与 stop 同在主循环 goroutine，循环退出后才
// 执行 defer stop，不存在并发 send。给 worker 一点时间排空，但不无限等（单条下发最长 ~12s）。
func (e *checkEngine) stop() {
	if e.stopped.Swap(true) {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		e.logger.Warn("check engine stop: worker drain timeout", "session_id", e.sessionID)
	}
	if d := e.droppedCount(); d > 0 {
		e.logger.Warn("check engine: notifications dropped (queue full)", "session_id", e.sessionID, "dropped", d)
	}
}

func (e *checkEngine) droppedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dropped
}

// deliver 在 worker goroutine 上序列化并下发一条命中告警。
func (e *checkEngine) deliver(job notifyJob) {
	bundle := e.buildBundle(job)
	raw, err := json.Marshal(bundle)
	if err != nil {
		e.logger.Warn("check rule: marshal bundle failed", "rule_id", job.rule.ID, "error", err)
		return
	}
	// 兜底：极端配置（N 很大 + 多方向 + 满负载）下整包超限时，丢弃上下文只保留触发记录。
	if len(raw) > checkrule.MaxBundleBytes {
		bundle.Context = nil
	}
	e.notify(job.alertID, bundle)
}

// inspect 在一条解码事件上求值全部规则；仅主循环 goroutine 调用，永不阻塞。
func (e *checkEngine) inspect(ev *event.Event) {
	if e == nil || len(e.rules) == 0 || ev == nil || e.stopped.Load() {
		return
	}

	// 求值视图：payload 合并 _meta（与语义引擎一致），使规则可引用 _meta.msg_name 等路径。
	// 放在 enrichSemantics 之后调用，name/annotate 注入的 _meta.* 在此可见。
	evalVal := withMetaObject(ev.Payload.Value, ev.Meta)
	raw, err := evalVal.MarshalJSON()
	if err != nil {
		e.logger.Debug("check rule: marshal eval view failed", "event_id", ev.Identity.ID, "error", err)
		return
	}

	now := time.Now()
	dir := normalizeDir(eventDirection(ev))
	for i := range e.rules {
		r := &e.rules[i]
		if !checkrule.Match(r.When, raw) {
			continue
		}
		// 冷却闸门：入队前先占位，防止一次下发在途（最长 ~10s）期间同规则连发。
		e.mu.Lock()
		if last, ok := e.lastFired[r.ID]; ok && now.Sub(last) < r.Cooldown() {
			e.mu.Unlock()
			continue
		}
		e.lastFired[r.ID] = now
		e.mu.Unlock()

		job := notifyJob{
			alertID: string(event.NewEventID()),
			rule:    *r,
			trigger: ev,
			ctx:     e.snapshotContext(dir, r.Context()),
		}
		select {
		case e.ch <- job:
		default:
			e.mu.Lock()
			e.dropped++
			e.mu.Unlock()
			e.logger.Debug("check rule: notify queue full, dropped", "rule_id", r.ID)
		}
	}

	// 求值后把当前事件并入滑动窗口（窗口只装「触发前」的事件，故放在最后）。
	e.pushWindow(dir, ev)
}

// snapshotContext 拷贝各方向窗口末尾 n 条（触发前最近 n 条）。仅主循环调用。
func (e *checkEngine) snapshotContext(excludeDir string, n int) map[string][]*event.Event {
	if n <= 0 || len(e.window) == 0 {
		return nil
	}
	out := make(map[string][]*event.Event, len(e.window))
	for dir, evs := range e.window {
		if len(evs) == 0 {
			continue
		}
		take := n
		if take > len(evs) {
			take = len(evs)
		}
		// 取末尾 take 条（最近），保持旧→新顺序。
		sl := make([]*event.Event, take)
		copy(sl, evs[len(evs)-take:])
		out[dir] = sl
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pushWindow 把事件追加进对应方向窗口，超容量丢最旧。仅主循环调用。
func (e *checkEngine) pushWindow(dir string, ev *event.Event) {
	if e.maxContext <= 0 {
		return
	}
	w := append(e.window[dir], ev)
	if len(w) > e.maxContext {
		w = w[len(w)-e.maxContext:]
	}
	e.window[dir] = w
}

// buildBundle 把命中快照渲染成可下发的 AlertBundle（worker goroutine 调用）。
func (e *checkEngine) buildBundle(job notifyJob) checkrule.AlertBundle {
	b := checkrule.AlertBundle{
		AlertID:     job.alertID,
		RuleID:      job.rule.ID,
		RuleName:    job.rule.Name,
		SessionID:   e.sessionID,
		Title:       job.rule.NotifyTitle(),
		Message:     job.rule.NotifyMessage(),
		GeneratedAt: time.Now().Format(alertTimeFormat),
		Trigger:     toAlertRecord(job.trigger),
	}
	if len(job.ctx) > 0 {
		b.Context = make(map[string][]checkrule.AlertRecord, len(job.ctx))
		for dir, evs := range job.ctx {
			recs := make([]checkrule.AlertRecord, 0, len(evs))
			for _, ev := range evs {
				recs = append(recs, toAlertRecord(ev))
			}
			b.Context[dir] = recs
		}
	}
	return b
}

// toAlertRecord 把一条解码事件渲染成告警记录（镜像 list_decoded_data 的 per-record 形状）。
func toAlertRecord(ev *event.Event) checkrule.AlertRecord {
	if ev == nil {
		return checkrule.AlertRecord{}
	}
	biz, flatMeta, _ := event.SplitReservedKeys(ev.Payload.Value)
	var data any
	if raw, err := biz.ToJSON(); err == nil {
		data = checkrule.TruncateData(raw, checkrule.MaxRecordDataBytes)
	}
	meta := ev.Meta.ToAny()
	if meta == nil {
		meta = flatMeta.ToAny()
	}
	return checkrule.AlertRecord{
		ID:        string(ev.Identity.ID),
		Timestamp: ev.Identity.Timestamp.Format(alertTimeFormat),
		Type:      string(ev.Identity.Type),
		Direction: eventDirection(ev),
		Data:      data,
		Meta:      meta,
	}
}

// eventDirection 解析事件方向：优先 meta.direction，回退 Context.Direction（与 gt-mcp
// decodedEventMap 的兜底一致）。
func eventDirection(ev *event.Event) string {
	if v, ok := ev.MetaValue("direction"); ok {
		if s, ok := v.AsString(); ok && s != "" {
			return s
		}
	}
	return ev.Context.Direction
}

// normalizeDir 把方向归一到 request/response/unknown 三桶（折叠语义同 pkg/state messageKind）。
func normalizeDir(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "client_to_server", "c2s", "request", "outgoing", "up":
		return "request"
	case "server_to_client", "s2c", "response", "incoming", "down":
		return "response"
	case "":
		return "unknown"
	default:
		return "unknown"
	}
}

// alertRowFromBundle 把一次命中渲染成平台端落库行。
//
// Timestamp 取「触发记录」的时间而非 generated_at：checkEngine 的 worker 可能滞后于事件
// 本身，而 Web 侧要按这个时间在 events 表里补「触发后 N 条」上下文，锚点必须是事件时间。
// 解析不出（老数据/异常）则回退当前时间，宁可排序稍偏也不写 0 值。
func alertRowFromBundle(b checkrule.AlertBundle) store.AlertRow {
	row := store.AlertRow{
		AlertID:     b.AlertID,
		RuleID:      b.RuleID,
		RuleName:    b.RuleName,
		Title:       b.Title,
		Message:     b.Message,
		Timestamp:   parseAlertTime(b.Trigger.Timestamp, b.GeneratedAt),
		GeneratedAt: b.GeneratedAt,
	}
	if raw, err := json.Marshal(b.Trigger); err == nil {
		row.TriggerJSON = string(raw)
	}
	if len(b.Context) > 0 {
		if raw, err := json.Marshal(b.Context); err == nil {
			row.ContextJSON = string(raw)
		}
	}
	return row
}

// parseAlertTime 依次尝试多个 RFC3339 字符串，全部失败则返回当前时间。
func parseAlertTime(values ...string) time.Time {
	for _, v := range values {
		if v == "" {
			continue
		}
		if t, err := time.Parse(alertTimeFormat, v); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Now()
}
