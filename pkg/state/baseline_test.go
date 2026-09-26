package state

import (
	"fmt"
	"testing"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// scEvent 构造一个携带 _state_changes 的事件；每个 raw 是一条 StateChange 的字段集合。
func scEvent(id, flow string, raw ...map[string]any) *event.Event {
	return scEventCtx(id, event.EventContext{FlowID: flow}, raw...)
}

// scEventCtx 同上，但可以自带上下文（对端标识等）。
func scEventCtx(id string, ctx event.EventContext, raw ...map[string]any) *event.Event {
	items := make([]event.Value, 0, len(raw))
	for _, m := range raw {
		obj := make(map[string]event.Value, len(m))
		for k, v := range m {
			obj[k] = event.ValueFromAny(v)
		}
		items = append(items, event.ValueObject(obj))
	}
	return &event.Event{
		Identity: event.Identity{ID: event.EventID(id), Timestamp: time.Unix(0, 0)},
		Context:  ctx,
		Analysis: event.ValueObject(map[string]event.Value{"_state_changes": event.ValueArray(items)}),
	}
}

// TestApplyNoopSuppression 覆盖 Apply 的「无实际变化不产出变更记录」规则：
// 只有基线上已解析出旧值、且本次上报值与之相等时才抑制；首见与 delete 必须保留。
func TestApplyNoopSuppression(t *testing.T) {
	bm := NewBaselineManager(nil)
	const sess = "s1"

	steps := []struct {
		name          string
		ev            *event.Event
		wantCount     int
		wantResolved  bool
		wantBeforeAny any
	}{
		{
			name: "首见实体：无基线，必须保留",
			ev: scEvent("e1", "f1", map[string]any{
				"subject_type": "Player", "subject_id": "1001", "op": "set", "path": "hp", "after": 100,
			}),
			wantCount: 1, wantResolved: false,
		},
		{
			name: "同值再报：抑制",
			ev: scEvent("e2", "f1", map[string]any{
				"subject_type": "Player", "subject_id": "1001", "op": "set", "path": "hp", "after": 100,
			}),
			wantCount: 0,
		},
		{
			name: "值变化：保留且 before 来自基线",
			ev: scEvent("e3", "f1", map[string]any{
				"subject_type": "Player", "subject_id": "1001", "op": "set", "path": "hp", "after": 80,
			}),
			wantCount: 1, wantResolved: true, wantBeforeAny: 100,
		},
		{
			name: "抑制不破坏基线：回到旧值应被识别为变化",
			ev: scEvent("e4", "f1", map[string]any{
				"subject_type": "Player", "subject_id": "1001", "op": "set", "path": "hp", "after": 100,
			}),
			wantCount: 1, wantResolved: true, wantBeforeAny: 80,
		},
		{
			name: "delete：after 为空，与旧值不等，保留",
			ev: scEvent("e5", "f1", map[string]any{
				"subject_type": "Player", "subject_id": "1001", "op": "delete", "path": "hp",
			}),
			wantCount: 1, wantResolved: true, wantBeforeAny: 100,
		},
		{
			name: "换 flow：基线不共享，首见保留",
			ev: scEvent("e6", "f2", map[string]any{
				"subject_type": "Player", "subject_id": "1001", "op": "set", "path": "hp", "after": 100,
			}),
			wantCount: 1, wantResolved: false,
		},
		{
			name: "对象值相等：整体比较后抑制",
			ev: scEvent("e7", "f1", map[string]any{
				"subject_type": "Equip", "subject_id": "e1", "op": "merge", "path": "stats",
				"after": map[string]any{"atk": 10, "def": 5},
			}),
			wantCount: 1, wantResolved: false,
		},
		{
			name: "对象值相同再报：抑制",
			ev: scEvent("e8", "f1", map[string]any{
				"subject_type": "Equip", "subject_id": "e1", "op": "merge", "path": "stats",
				"after": map[string]any{"atk": 10, "def": 5},
			}),
			wantCount: 0,
		},
		{
			name: "对象字段变化：保留",
			ev: scEvent("e9", "f1", map[string]any{
				"subject_type": "Equip", "subject_id": "e1", "op": "merge", "path": "stats",
				"after": map[string]any{"atk": 12, "def": 5},
			}),
			wantCount: 1, wantResolved: true,
		},
	}

	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			got, err := bm.Apply(st.ev, sess)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(got) != st.wantCount {
				t.Fatalf("变更条数 = %d, want %d", len(got), st.wantCount)
			}
			if st.wantCount == 0 {
				return
			}
			if got[0].BeforeResolved != st.wantResolved {
				t.Errorf("BeforeResolved = %v, want %v", got[0].BeforeResolved, st.wantResolved)
			}
			if st.wantBeforeAny != nil {
				want := event.ValueFromAny(st.wantBeforeAny)
				if !got[0].Before.Equal(want) {
					t.Errorf("Before = %v, want %v", got[0].Before, want)
				}
			}
		})
	}

	// 抑制计数：首见 3 条（Player/f1、Player/f2、Equip）之外，2 条同值回吐被丢。
	if n := bm.NoopSuppressed(); n != 2 {
		t.Errorf("NoopSuppressed = %d, want 2", n)
	}
}

// scApply 是测试里的 Apply 简写：构造单条变更的事件并断言无 error。
func scApply(t *testing.T, bm *BaselineManager, id, flow, sess string, raw map[string]any) []store.EnrichedStateChange {
	t.Helper()
	got, err := bm.Apply(scEvent(id, flow, raw), sess)
	if err != nil {
		t.Fatalf("Apply(%v): %v", raw, err)
	}
	return got
}

// baselineOf 取出实体的当前基线状态，供断言基线内容。
func baselineOf(t *testing.T, bm *BaselineManager, sess, flow, stype, sid string) map[string]event.Value {
	t.Helper()
	base, ok := bm.store.Get(EntityKey{Scope: sess, FlowID: flow, SubjectType: stype, SubjectID: sid})
	if !ok {
		return nil
	}
	return base.State
}

// TestApplyDeleteClearsBaseline 覆盖 delete 的基线语义：
// after 为空必须把该 path 从基线移除，否则「删掉再设回原值」会被 noop 抑制误判成没变化。
func TestApplyDeleteClearsBaseline(t *testing.T) {
	bm := NewBaselineManager(nil)
	const sess = "s1"

	scApply(t, bm, "e1", "f1", sess, map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 100,
	})

	del := scApply(t, bm, "e2", "f1", sess, map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "delete", "path": "hp",
	})
	if len(del) != 1 || !del[0].BeforeResolved {
		t.Fatalf("delete 应产出 1 条带 before 的变更，got %+v", del)
	}

	if state := baselineOf(t, bm, sess, "f1", "Player", "1"); state != nil {
		if _, exists := state["hp"]; exists {
			t.Fatalf("delete 后基线仍残留 hp = %v", state["hp"])
		}
	}

	// 重新设回原值：基线里已无该 path，必须识别成一次新变化而不是 noop。
	again := scApply(t, bm, "e3", "f1", sess, map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 100,
	})
	if len(again) != 1 {
		t.Fatalf("删掉再设回应产出 1 条变更，got %d（疑似被 noop 误抑制）", len(again))
	}
	if again[0].BeforeResolved {
		t.Error("delete 之后的 before 不该 resolved")
	}
	if n := bm.NoopSuppressed(); n != 0 {
		t.Errorf("NoopSuppressed = %d, want 0（delete 场景不该产生抑制）", n)
	}
}

// TestApplyMergeKeepsUntouchedKeys 覆盖 merge 的基线语义：
// after 是片段，只能浅合并，整块覆盖会把本次没提及的字段从基线抹掉。
func TestApplyMergeKeepsUntouchedKeys(t *testing.T) {
	bm := NewBaselineManager(nil)
	const sess = "s1"

	scApply(t, bm, "e1", "f1", sess, map[string]any{
		"subject_type": "Equip", "subject_id": "w1", "op": "set", "path": "stats",
		"after": map[string]any{"atk": 10, "def": 5},
	})

	got := scApply(t, bm, "e2", "f1", sess, map[string]any{
		"subject_type": "Equip", "subject_id": "w1", "op": "merge", "path": "stats",
		"after": map[string]any{"atk": 12},
	})
	if len(got) != 1 {
		t.Fatalf("merge 应产出 1 条变更，got %d", len(got))
	}
	wantBefore := event.ValueFromAny(map[string]any{"atk": 10, "def": 5})
	if !got[0].Before.Equal(wantBefore) {
		t.Errorf("merge 的 before = %v, want %v（应为合并前的完整对象）", got[0].Before, wantBefore)
	}

	state := baselineOf(t, bm, sess, "f1", "Equip", "w1")
	merged, ok := state["stats"].AsObject()
	if !ok {
		t.Fatalf("基线 stats 不是对象: %v", state["stats"])
	}
	if _, ok := merged["def"]; !ok {
		t.Error("merge 后基线丢了未提及的 def 字段（被整块覆盖）")
	}
	if v, _ := merged["atk"].AsInt(); v != 12 {
		t.Errorf("merge 后 atk = %d, want 12", v)
	}

	// 再报同一片段：合并后与基线相等，应被 noop 抑制（而不是产出一条假变化）。
	same := scApply(t, bm, "e3", "f1", sess, map[string]any{
		"subject_type": "Equip", "subject_id": "w1", "op": "merge", "path": "stats",
		"after": map[string]any{"atk": 12},
	})
	if len(same) != 0 {
		t.Errorf("重复 merge 同值应被抑制，got %d 条", len(same))
	}
}

// TestApplySkipsInvalidInsteadOfDroppingBatch 覆盖校验策略：
// 一条脏数据只能带走它自己，不能整批返回 error（那会让合法变更凭空消失）。
func TestApplySkipsInvalidInsteadOfDroppingBatch(t *testing.T) {
	bm := NewBaselineManager(nil)

	ev := scEvent("e1", "f1",
		map[string]any{"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 100},
		map[string]any{"subject_type": "Player", "subject_id": "", "op": "set", "path": "hp", "after": 1},
		map[string]any{"subject_type": "Player", "subject_id": "1", "op": "upsert", "path": "hp", "after": 1},
	)

	got, err := bm.Apply(ev, "s1")
	if err != nil {
		t.Fatalf("Apply 不该因个别非法变更整批报错: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("合法变更数 = %d, want 1", len(got))
	}
	if n := bm.InvalidSkipped(); n != 2 {
		t.Errorf("InvalidSkipped = %d, want 2", n)
	}
}

// TestApplyTruncatesOversizedBatch 覆盖信任边界：单个事件的变更条数必须被上限截断。
func TestApplyTruncatesOversizedBatch(t *testing.T) {
	bm := NewBaselineManager(nil)

	const extra = 10
	raw := make([]map[string]any, 0, maxStateChangesPerEvent+extra)
	for i := 0; i < maxStateChangesPerEvent+extra; i++ {
		raw = append(raw, map[string]any{
			"subject_type": "Player", "subject_id": "1", "op": "set",
			"path": fmt.Sprintf("p%d", i), "after": i,
		})
	}

	got, err := bm.Apply(scEvent("e1", "f1", raw...), "s1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != maxStateChangesPerEvent {
		t.Errorf("变更条数 = %d, want %d", len(got), maxStateChangesPerEvent)
	}
	if n := bm.Truncated(); n != extra {
		t.Errorf("Truncated = %d, want %d", n, extra)
	}
}

// countingStore 记录 Put 次数，用于断言「一个事件里同一实体只回写一次」。
type countingStore struct {
	inner BaselineStore
	puts  int
	keys  map[EntityKey]int
}

func newCountingStore() *countingStore {
	return &countingStore{inner: NewMemoryBaselineStore(), keys: map[EntityKey]int{}}
}

func (c *countingStore) Get(key EntityKey) (*EntityBaseline, bool) { return c.inner.Get(key) }

func (c *countingStore) Put(base *EntityBaseline) {
	c.puts++
	c.keys[base.Key]++
	c.inner.Put(base)
}

// TestApplyWritesBaselineOncePerEntity 覆盖回写策略：
// 同一实体的多条字段变更只回写一次（逐条 Put 是在同一个指针上重复过锁），
// 被抑制、未产生变化的实体不写。
func TestApplyWritesBaselineOncePerEntity(t *testing.T) {
	cs := newCountingStore()
	bm := NewBaselineManager(cs)

	raw := make([]map[string]any, 0, 30)
	for i := 0; i < 30; i++ {
		raw = append(raw, map[string]any{
			"subject_type": "Player", "subject_id": "1", "op": "set",
			"path": fmt.Sprintf("p%d", i), "after": i,
		})
	}
	raw = append(raw, map[string]any{
		"subject_type": "Equip", "subject_id": "e1", "op": "set", "path": "atk", "after": 1,
	})

	got, err := bm.Apply(scEvent("e1", "f1", raw...), "s1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 31 {
		t.Fatalf("变更条数 = %d, want 31", len(got))
	}
	if cs.puts != 2 {
		t.Errorf("Put 次数 = %d, want 2（每实体一次）", cs.puts)
	}

	// 全部同值回吐：没有一条变更，就一个实体都不该回写。
	cs.puts = 0
	same, err := bm.Apply(scEvent("e2", "f1", raw...), "s1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(same) != 0 {
		t.Fatalf("同值回吐应全部抑制，got %d 条", len(same))
	}
	if cs.puts != 0 {
		t.Errorf("全部抑制时 Put 次数 = %d, want 0", cs.puts)
	}
}

// TestApplySeqFollowsDeclaredOrder 覆盖 seq：它必须记「在插件声明数组里的位置」，
// 非法条目被跳过后仍保留原始下标（否则「谁先谁后」会被过滤动作改掉）。
func TestApplySeqFollowsDeclaredOrder(t *testing.T) {
	bm := NewBaselineManager(nil)

	got, err := bm.Apply(scEvent("e1", "f1",
		map[string]any{"subject_type": "Player", "subject_id": "1", "op": "set", "path": "a", "after": 1},
		map[string]any{"subject_type": "Player", "subject_id": "1", "op": "upsert", "path": "bad", "after": 2},
		map[string]any{"subject_type": "Player", "subject_id": "1", "op": "set", "path": "c", "after": 3},
	), "s1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("变更条数 = %d, want 2", len(got))
	}
	if got[0].Seq != 1 || got[1].Seq != 3 {
		t.Errorf("seq = (%d, %d), want (1, 3)：1 基声明位次，且被跳过项要留下空档", got[0].Seq, got[1].Seq)
	}
}

// TestApplyPeerScopeCarriesAcrossSessions 覆盖 ScopePeer：
// 同一对端的实体在换会话后延续基线；对端标识缺失时退回会话维度，绝不跨客户端混算。
func TestApplyPeerScopeCarriesAcrossSessions(t *testing.T) {
	bm := NewBaselineManager(nil, WithScope(ScopePeer))
	peer := event.EventContext{FlowID: "f1", PeerKey: "10.0.0.1|10.0.0.2:9250"}
	other := event.EventContext{FlowID: "f9", PeerKey: "10.0.0.9|10.0.0.2:9250"}

	first, err := bm.Apply(scEventCtx("e1", peer, map[string]any{
		"subject_type": "Player", "subject_id": "1001", "op": "set", "path": "exp", "after": 100,
	}), "sess-A")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(first) != 1 || first[0].BeforeResolved {
		t.Fatalf("首见应保留且不 resolved: %+v", first)
	}

	// 换会话、换连接（flow 变）但同一对端：必须接上上一轮的终值。
	next := peer
	next.FlowID = "f2"
	second, err := bm.Apply(scEventCtx("e2", next, map[string]any{
		"subject_type": "Player", "subject_id": "1001", "op": "set", "path": "exp", "after": 150,
	}), "sess-B")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(second) != 1 || !second[0].BeforeResolved {
		t.Fatalf("换会话后应复用对端基线: %+v", second)
	}
	if v, _ := second[0].Before.AsInt(); v != 100 {
		t.Errorf("before = %d, want 100", v)
	}

	// 另一个客户端（不同对端）即便 subject_id 相同也不能共享基线。
	third, err := bm.Apply(scEventCtx("e3", other, map[string]any{
		"subject_type": "Player", "subject_id": "1001", "op": "set", "path": "exp", "after": 150,
	}), "sess-B")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(third) != 1 || third[0].BeforeResolved {
		t.Errorf("不同对端不该共享基线: %+v", third)
	}

	// 无对端标识：退回会话维度（同会话内仍连续，跨会话不延续）。
	noPeer := event.EventContext{FlowID: "f1"}
	bm.Apply(scEventCtx("e4", noPeer, map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 10,
	}), "sess-C")
	fallback, err := bm.Apply(scEventCtx("e5", noPeer, map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 20,
	}), "sess-D")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fallback) != 1 || fallback[0].BeforeResolved {
		t.Errorf("无对端标识时应退回会话维度，不该跨会话延续: %+v", fallback)
	}
}

// TestApplyExpiresStaleBaseline 覆盖基线保鲜期：
// 超过 TTL 未更新的基线不能再充当 before（把很久以前的值当成「变更前」比标首见更误导）。
func TestApplyExpiresStaleBaseline(t *testing.T) {
	bm := NewBaselineManager(nil)
	bm.ttl = time.Minute

	old := scEvent("e1", "f1", map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 100,
	})
	if _, err := bm.Apply(old, "s1"); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	fresh := scEvent("e2", "f1", map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 80,
	})
	fresh.Identity.Timestamp = old.Identity.Timestamp.Add(30 * time.Second)
	got, err := bm.Apply(fresh, "s1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 1 || !got[0].BeforeResolved {
		t.Fatalf("TTL 内应复用基线: %+v", got)
	}

	stale := scEvent("e3", "f1", map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 60,
	})
	stale.Identity.Timestamp = fresh.Identity.Timestamp.Add(2 * time.Hour)
	got, err = bm.Apply(stale, "s1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("过期后仍应产出 1 条变更，got %d", len(got))
	}
	if got[0].BeforeResolved {
		t.Error("过期基线不该充当 before")
	}
}

// TestForgetScopeReleasesBaselines 覆盖基线回收：会话结束时该会话的基线必须能整体释放，
// 否则进程级共享的 store 只会单调增长。
func TestForgetScopeReleasesBaselines(t *testing.T) {
	store := NewMemoryBaselineStore()
	bm := NewBaselineManager(store)

	for _, sess := range []string{"s1", "s2"} {
		if _, err := bm.Apply(scEvent("e1", "f1", map[string]any{
			"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 1,
		}), sess); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	if n := store.Len(); n != 2 {
		t.Fatalf("基线数 = %d, want 2", n)
	}
	if n := bm.ForgetScope("s1"); n != 1 {
		t.Errorf("ForgetScope(s1) = %d, want 1", n)
	}
	if n := store.Len(); n != 1 {
		t.Errorf("回收后基线数 = %d, want 1", n)
	}
}

// TestMemoryBaselineStoreEvictsOldest 覆盖条数上限：
// 超限时丢最久未更新的实体，并留下可观测的淘汰计数。
func TestMemoryBaselineStoreEvictsOldest(t *testing.T) {
	store := NewMemoryBaselineStore()
	store.maxEntries = 4
	store.sweepAt = 4

	base := time.Unix(0, 0)
	for i := 0; i < 6; i++ {
		store.Put(&EntityBaseline{
			Key:      EntityKey{Scope: "s", SubjectType: "P", SubjectID: fmt.Sprint(i)},
			State:    map[string]event.Value{},
			LastSeen: base.Add(time.Duration(i) * time.Second),
		})
	}
	if n := store.Len(); n > 5 {
		t.Errorf("淘汰后条数 = %d, 应回到上限附近（含 1/8 滞后水位）", n)
	}
	if store.Evicted() == 0 {
		t.Error("发生了淘汰却没有计数")
	}
	if _, ok := store.Get(EntityKey{Scope: "s", SubjectType: "P", SubjectID: "0"}); ok {
		t.Error("最旧的实体应被淘汰")
	}
	if _, ok := store.Get(EntityKey{Scope: "s", SubjectType: "P", SubjectID: "5"}); !ok {
		t.Error("最新的实体必须保留")
	}
}

// TestApplyMergeRecordsMergedAfter 覆盖 merge 的下游语义：
// 产出的 After 必须是与基线浅合并后的完整对象，而不是插件上报的片段——
// state_changes 表与 FieldHistory.LastValue 等消费方据此读「变更后的真实值」。
func TestApplyMergeRecordsMergedAfter(t *testing.T) {
	bm := NewBaselineManager(nil)
	const sess = "s1"

	scApply(t, bm, "e1", "f1", sess, map[string]any{
		"subject_type": "Equip", "subject_id": "e1", "op": "merge", "path": "stats",
		"after": map[string]any{"atk": 10, "def": 5},
	})
	got := scApply(t, bm, "e2", "f1", sess, map[string]any{
		"subject_type": "Equip", "subject_id": "e1", "op": "merge", "path": "stats",
		"after": map[string]any{"atk": 12},
	})
	if len(got) != 1 {
		t.Fatalf("merge 片段应产出 1 条变更，got %d", len(got))
	}
	wantAfter := event.ValueFromAny(map[string]any{"atk": 12, "def": 5})
	if !got[0].After.Equal(wantAfter) {
		t.Errorf("After = %v, want 合并后的 %v（不是插件片段）", got[0].After, wantAfter)
	}
	if !got[0].BeforeResolved || !got[0].AfterResolved {
		t.Errorf("resolved 标记 = (%v,%v), want (true,true)", got[0].BeforeResolved, got[0].AfterResolved)
	}
}

// TestApplyDeleteOnlyByOp 覆盖「删除只由 op 决定」：
// set 携带 null after 是非法条目（跳过并计数），不得像旧行为那样静默删 path；
// delete 即使携带与旧值相同的 after 也必须生效（删除不能被 noop 抑制吞掉）。
func TestApplyDeleteOnlyByOp(t *testing.T) {
	bm := NewBaselineManager(nil)
	const sess = "s1"

	scApply(t, bm, "e1", "f1", sess, map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": 100,
	})

	got, err := bm.Apply(scEvent("e2", "f1", map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "set", "path": "hp", "after": nil,
	}), sess)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("set 成 null 不该产出变更，got %+v", got)
	}
	if bm.InvalidSkipped() != 1 {
		t.Errorf("InvalidSkipped = %d, want 1", bm.InvalidSkipped())
	}
	if state := baselineOf(t, bm, sess, "f1", "Player", "1"); state["hp"].Kind != event.Int {
		t.Errorf("非法 set-null 被跳过后，基线 hp 应仍是 100，got %v", state["hp"])
	}

	del := scApply(t, bm, "e3", "f1", sess, map[string]any{
		"subject_type": "Player", "subject_id": "1", "op": "delete", "path": "hp", "after": 100,
	})
	if len(del) != 1 {
		t.Fatalf("delete 携带同值 after 也必须产出变更，got %d", len(del))
	}
	if del[0].After.Kind != event.Null {
		t.Errorf("delete 行的 after 应恒为 null，got %v", del[0].After)
	}
	if state := baselineOf(t, bm, sess, "f1", "Player", "1"); state != nil {
		if _, exists := state["hp"]; exists {
			t.Errorf("delete 后基线仍残留 hp = %v", state["hp"])
		}
	}
}

// TestApplyMergeRejectsNonObjectAfter 覆盖 merge/after 契约的运行时侧：
// after 不是 Object 的 merge 条目在 Validate 层就被跳过，不会污染基线。
func TestApplyMergeRejectsNonObjectAfter(t *testing.T) {
	bm := NewBaselineManager(nil)
	const sess = "s1"

	got, err := bm.Apply(scEvent("e1", "f1", map[string]any{
		"subject_type": "Equip", "subject_id": "e1", "op": "merge", "path": "stats", "after": 7,
	}), sess)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("非对象 merge 不该产出变更，got %+v", got)
	}
	if bm.InvalidSkipped() != 1 {
		t.Errorf("InvalidSkipped = %d, want 1", bm.InvalidSkipped())
	}
}
