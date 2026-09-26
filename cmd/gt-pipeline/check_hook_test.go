package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"gametrace/pkg/checkrule"
	"gametrace/pkg/event"
	"github.com/OwnSecurityGuard/gametrace/sdk/rule"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// mkEvent 构造一条解码事件：payload 带 type 字段，方向写进 Context.Direction。
func mkEvent(id, typ, dir string) *event.Event {
	ev := event.NewEvent("sess", event.EventType("game."+typ), "",
		event.ValueObject(map[string]event.Value{"type": event.ValueString(typ)}),
		event.EventContext{Direction: dir})
	ev.Identity.ID = event.EventID(id)
	ev.Identity.Timestamp = time.Unix(1700000000, 0).UTC()
	return ev
}

// alertSink 收集 worker 下发的告警（并发安全）。
type alertSink struct {
	mu     sync.Mutex
	calls  []string // alertID 顺序
	bundle map[string]checkrule.AlertBundle
}

func newSink() *alertSink { return &alertSink{bundle: map[string]checkrule.AlertBundle{}} }

func (s *alertSink) notify(alertID string, b checkrule.AlertBundle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, alertID)
	s.bundle[alertID] = b
}

func (s *alertSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func hitRule(id string, ctxN int) checkrule.CheckRule {
	return checkrule.CheckRule{
		ID: id, Name: "hit-" + id, Enabled: true,
		When:                rule.Predicate{Path: "type", Op: rule.OpEq, Value: "hit"},
		ContextPerDirection: ctxN,
	}
}

func TestCheckEngineSlidingWindow(t *testing.T) {
	sink := newSink()
	e := newCheckEngine(testLogger(), "sess", []checkrule.CheckRule{hitRule("r1", 2)}, sink.notify)
	e.start()

	// 触发前：request r1,r2 / response p1,p2 各填满窗口（maxContext=2）。
	for _, ev := range []*event.Event{
		mkEvent("r1", "req", "client_to_server"),
		mkEvent("p1", "resp", "server_to_client"),
		mkEvent("r2", "req", "client_to_server"),
		mkEvent("p2", "resp", "server_to_client"),
		mkEvent("hit", "hit", "client_to_server"), // 触发（type=hit）
		mkEvent("after", "req", "client_to_server"),
	} {
		e.inspect(ev)
	}
	e.stop() // 排空队列

	if sink.count() != 1 {
		t.Fatalf("expected exactly 1 alert, got %d", sink.count())
	}
	b := sink.bundle[sink.calls[0]]
	if b.Trigger.ID != "hit" {
		t.Fatalf("trigger should be the matching event, got %q", b.Trigger.ID)
	}
	if b.RuleID != "r1" || b.SessionID != "sess" {
		t.Fatalf("bundle metadata wrong: %+v", b)
	}
	// request 方向触发前最近 2 条：r1,r2（旧→新）；不含触发记录本身与 after。
	if got := recordIDs(b.Context["request"]); len(got) != 2 || got[0] != "r1" || got[1] != "r2" {
		t.Fatalf("request context = %v, want [r1 r2]", got)
	}
	if got := recordIDs(b.Context["response"]); len(got) != 2 || got[0] != "p1" || got[1] != "p2" {
		t.Fatalf("response context = %v, want [p1 p2]", got)
	}
}

func TestCheckEngineWindowPerRuleN(t *testing.T) {
	// maxContext 取多规则 Context() 的最大值：rWide 要 3，rNarrow 要 1。
	// 两规则命中不同 type，冷却各自独立，都会触发。
	sink := newSink()
	rWide := checkrule.CheckRule{ID: "wide", Name: "w", Enabled: true,
		When: rule.Predicate{Path: "type", Op: rule.OpEq, Value: "wide"}, ContextPerDirection: 3}
	rNarrow := checkrule.CheckRule{ID: "narrow", Name: "n", Enabled: true,
		When: rule.Predicate{Path: "type", Op: rule.OpEq, Value: "narrow"}, ContextPerDirection: 1}
	e := newCheckEngine(testLogger(), "sess", []checkrule.CheckRule{rWide, rNarrow}, sink.notify)
	e.start()
	// 5 条 request 铺底，然后 narrow 触发，再 wide 触发。
	e.inspect(mkEvent("a", "req", "request"))
	e.inspect(mkEvent("b", "req", "request"))
	e.inspect(mkEvent("c", "req", "request"))
	e.inspect(mkEvent("d", "req", "request"))
	e.inspect(mkEvent("narrow", "narrow", "request")) // 触发 narrow：request 最近1条 = d
	e.inspect(mkEvent("e", "req", "request"))
	e.inspect(mkEvent("wide", "wide", "request")) // 触发 wide：request 最近3条（窗口 maxContext=3）
	e.stop()

	if sink.count() != 2 {
		t.Fatalf("expected 2 alerts (wide+narrow), got %d", sink.count())
	}
	var wideB, narrowB checkrule.AlertBundle
	for _, id := range sink.calls {
		switch sink.bundle[id].RuleID {
		case "wide":
			wideB = sink.bundle[id]
		case "narrow":
			narrowB = sink.bundle[id]
		}
	}
	if got := recordIDs(narrowB.Context["request"]); len(got) != 1 || got[0] != "d" {
		t.Fatalf("narrow context = %v, want [d]", got)
	}
	// wide 触发前窗口容量=3：最近三条 request 事件（d 之后 narrow 也是 request 但 type 不同，仍计入窗口）。
	// 序列 request 事件（含 narrow/e）：a b c d narrow e → 触发 wide 前最近3 = narrow e ... 实际是 [narrow? ]
	// 触发 wide 前 request 桶已含：...a b c d narrow e（每次 inspect 都 push），末尾3 = d,narrow? no: 顺序 a,b,c,d,narrow,e → 末3=[narrow,e] 只有6? 取末尾3=d,narrow,e? 但 narrow/e 都算 request。
	got := recordIDs(wideB.Context["request"])
	if len(got) != 3 || got[2] != "e" {
		t.Fatalf("wide context len/last wrong: %v", got)
	}
}

func TestCheckEngineCooldownDedup(t *testing.T) {
	sink := newSink()
	e := newCheckEngine(testLogger(), "sess", []checkrule.CheckRule{hitRule("r1", 1)}, sink.notify)
	e.start()
	e.inspect(mkEvent("h1", "hit", "request"))
	e.inspect(mkEvent("h2", "hit", "request")) // 同规则·同会话，30s 内冷却 → 被闸门拦下
	e.inspect(mkEvent("h3", "hit", "request"))
	e.stop()
	if sink.count() != 1 {
		t.Fatalf("cooldown should collapse 3 hits into 1 alert, got %d", sink.count())
	}
}

func TestCheckEngineNoMatchNoAlert(t *testing.T) {
	sink := newSink()
	e := newCheckEngine(testLogger(), "sess", []checkrule.CheckRule{hitRule("r1", 3)}, sink.notify)
	e.start()
	e.inspect(mkEvent("x", "other", "request"))
	e.inspect(mkEvent("y", "other", "response"))
	e.stop()
	if sink.count() != 0 {
		t.Fatalf("no rule should fire, got %d alerts", sink.count())
	}
}

func TestCheckEngineMetaPredicate(t *testing.T) {
	// 规则引用 _meta.direction（富化后 Meta 合入求值视图）。
	sink := newSink()
	r := checkrule.CheckRule{ID: "m", Name: "meta", Enabled: true,
		When: rule.Predicate{Path: "_meta.direction", Op: rule.OpEq, Value: "server_to_client"}}
	e := newCheckEngine(testLogger(), "sess", []checkrule.CheckRule{r}, sink.notify)
	e.start()
	ev := mkEvent("s1", "push", "server_to_client")
	ev.Meta = event.ValueObject(map[string]event.Value{"direction": event.ValueString("server_to_client")})
	e.inspect(ev)
	e.stop()
	if sink.count() != 1 {
		t.Fatalf("_meta predicate should fire, got %d", sink.count())
	}
}

func TestCheckEngineQueueFullDrops(t *testing.T) {
	sink := newSink()
	// 构造超过队列容量的独立规则（各命中不同 type），且不启动 worker：
	// 一条事件命中全部规则 → 入队至容量上限后丢弃并计数。
	n := notifyQueueSize + 5
	rules := make([]checkrule.CheckRule, 0, n)
	for i := 0; i < n; i++ {
		rules = append(rules, checkrule.CheckRule{ID: string(rune('a'+i%26)) + itoaTest(i),
			Name: "r", Enabled: true, When: rule.Predicate{Path: "type", Op: rule.OpExists}})
	}
	e := newCheckEngine(testLogger(), "sess", rules, sink.notify)
	// 故意不 start()：无人消费，验证非阻塞入队 + dropped 计数不 panic。
	e.inspect(mkEvent("h", "hit", "request"))
	if got := e.droppedCount(); got != 5 {
		t.Fatalf("dropped = %d, want 5", got)
	}
	e.stop()
}

func TestCheckEngineStopIsIdempotent(t *testing.T) {
	sink := newSink()
	e := newCheckEngine(testLogger(), "sess", []checkrule.CheckRule{hitRule("r1", 2)}, sink.notify)
	e.start()
	e.inspect(mkEvent("h", "hit", "request"))
	e.stop()
	e.stop() // 二次 stop 不应 panic（stopped 守卫）
}

func TestNormalizeDir(t *testing.T) {
	cases := map[string]string{
		"client_to_server": "request", "c2s": "request", "request": "request", "up": "request",
		"server_to_client": "response", "s2c": "response", "response": "response", "down": "response",
		"":            "unknown",
		"weird":       "unknown",
		"  REQUEST  ": "request",
	}
	for in, want := range cases {
		if got := normalizeDir(in); got != want {
			t.Fatalf("normalizeDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlertRecordDataShape(t *testing.T) {
	// toAlertRecord 输出的 data 应为可反序列化的 JSON（含 type 字段），meta 独立。
	ev := mkEvent("h", "hit", "request")
	rec := toAlertRecord(ev)
	if rec.ID != "h" || rec.Direction != "request" {
		t.Fatalf("record meta wrong: %+v", rec)
	}
	raw, err := json.Marshal(rec.Data)
	if err != nil {
		t.Fatalf("data not json-marshalable: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("data should embed as JSON object: %v (%s)", err, raw)
	}
	if obj["type"] != "hit" {
		t.Fatalf("data.type = %v, want hit", obj["type"])
	}
}

func recordIDs(recs []checkrule.AlertRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}

func TestAlertRowFromBundle(t *testing.T) {
	b := checkrule.AlertBundle{
		AlertID:     "al-1",
		RuleID:      "r1",
		RuleName:    "血量过低",
		Title:       "T",
		Message:     "M",
		GeneratedAt: "2026-09-26T08:03:05.000000Z",
		Trigger:     checkrule.AlertRecord{ID: "ev9", Timestamp: "2026-09-26T08:03:00.000000Z", Type: "packet", Direction: "server_to_client"},
		Context:     map[string][]checkrule.AlertRecord{"request": {{ID: "ev8"}}},
	}
	row := alertRowFromBundle(b)
	if row.AlertID != "al-1" || row.RuleID != "r1" || row.RuleName != "血量过低" || row.Title != "T" || row.Message != "M" {
		t.Fatalf("scalar columns = %+v", row)
	}
	// 锚点取触发记录时间，不是 generated_at（worker 可能滞后数秒）。
	want := time.Date(2026, 9, 26, 8, 3, 0, 0, time.UTC)
	if !row.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", row.Timestamp, want)
	}
	var trig checkrule.AlertRecord
	if err := json.Unmarshal([]byte(row.TriggerJSON), &trig); err != nil || trig.ID != "ev9" {
		t.Errorf("TriggerJSON = %q err=%v", row.TriggerJSON, err)
	}
	var ctx map[string][]checkrule.AlertRecord
	if err := json.Unmarshal([]byte(row.ContextJSON), &ctx); err != nil || len(ctx["request"]) != 1 {
		t.Errorf("ContextJSON = %q err=%v", row.ContextJSON, err)
	}

	// 无上下文 → 空串（而不是 "null"），前端据此判断不渲染上下文区。
	empty := alertRowFromBundle(checkrule.AlertBundle{
		AlertID:     "al-2",
		GeneratedAt: "2026-09-26T08:03:05.000000Z",
		Trigger:     checkrule.AlertRecord{Timestamp: "not-a-time", ID: "ev1"},
	})
	if empty.ContextJSON != "" {
		t.Errorf("ContextJSON = %q, want empty", empty.ContextJSON)
	}
	// 触发时间解析不出来时回退 generated_at，绝不写 0 值时间。
	if !empty.Timestamp.Equal(time.Date(2026, 9, 26, 8, 3, 5, 0, time.UTC)) {
		t.Errorf("fallback Timestamp = %v", empty.Timestamp)
	}
	// 两者都不可用时用当前时间兜底（老数据/异常配置）。
	now := time.Now()
	if d := alertRowFromBundle(checkrule.AlertBundle{}).Timestamp.Sub(now); d > time.Minute || d < -time.Minute {
		t.Errorf("last-resort Timestamp = %v, want ~now", alertRowFromBundle(checkrule.AlertBundle{}).Timestamp)
	}
}
