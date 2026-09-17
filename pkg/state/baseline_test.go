package state

import (
	"testing"
	"time"

	"gametrace/pkg/event"
)

// scEvent 构造一个携带 _state_changes 的事件；每个 raw 是一条 StateChange 的字段集合。
func scEvent(id, flow string, raw ...map[string]any) *event.Event {
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
		Context:  event.EventContext{FlowID: flow},
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
