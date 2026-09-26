package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gametrace/pkg/event"
)

// pageFixture 构造一个含 3 种类型、6 条事件的测试会话，时间严格递增。
func pageFixture(t *testing.T) (*SQLiteStore, time.Time) {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "page.db"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Truncate(time.Millisecond)
	mk := func(id, typ string, off time.Duration, corr string) *event.Event {
		return &event.Event{
			Identity: event.Identity{
				ID: event.EventID(id), SessionID: "s1", Type: event.EventType(typ),
				Source: "test", Timestamp: base.Add(off),
			},
			Trace: event.TraceContext{CorrelationID: corr},
			Payload: event.Payload{
				Value:    event.Value{Kind: event.Object, Object: map[string]event.Value{"n": {Kind: event.Int, Int: int64(len(id))}}},
			},
		}
	}
	events := []*event.Event{
		mk("e1", "tcp", 0, ""),
		mk("e2", "http", time.Millisecond, "c1"),
		mk("e3", "tcp", 2*time.Millisecond, "c1"),
		mk("e4", "http", 3*time.Millisecond, "c2"),
		mk("e5", "tcp", 4*time.Millisecond, "c2"),
		mk("e6", "dns", 5*time.Millisecond, ""),
	}
	if err := s.AppendEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	return s, base
}

func TestQueryEventPage_PaginationAndOrder(t *testing.T) {
	s, _ := pageFixture(t)
	defer s.Close()
	ctx := context.Background()
	q := EventPageQuery{SessionID: "s1"}

	// 第 1 页（最新在前）：e6, e5, e4
	page, total, err := s.QueryEventPage(ctx, q, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(page) != 3 {
		t.Fatalf("page1: total=%d len=%d", total, len(page))
	}
	if string(page[0].Identity.ID) != "e6" || string(page[2].Identity.ID) != "e4" {
		t.Fatalf("page1 order wrong: %s %s %s", page[0].Identity.ID, page[1].Identity.ID, page[2].Identity.ID)
	}

	// 第 3 页（越界）：空页 + 精确 total
	page, total, err = s.QueryEventPage(ctx, q, 3, 6)
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(page) != 0 {
		t.Fatalf("page3: total=%d len=%d", total, len(page))
	}

	// limit 非法
	if _, _, err := s.QueryEventPage(ctx, q, 0, 0); err == nil {
		t.Fatal("limit=0 should error")
	}
}

func TestQueryEventPage_TypePushdown(t *testing.T) {
	s, _ := pageFixture(t)
	defer s.Close()
	ctx := context.Background()

	// TypeEq：仅 http（e4, e2，倒序）
	page, total, err := s.QueryEventPage(ctx, EventPageQuery{SessionID: "s1", TypeEq: "http"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(page) != 2 {
		t.Fatalf("typeEq: total=%d len=%d", total, len(page))
	}
	if string(page[0].Identity.ID) != "e4" || string(page[1].Identity.ID) != "e2" {
		t.Fatalf("typeEq order: %s %s", page[0].Identity.ID, page[1].Identity.ID)
	}

	// TypeNot：排除 tcp（e6, e4, e2）
	page, total, err = s.QueryEventPage(ctx, EventPageQuery{SessionID: "s1", TypeNot: "tcp"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(page) != 3 {
		t.Fatalf("typeNot: total=%d len=%d", total, len(page))
	}
	for _, e := range page {
		if string(e.Identity.Type) == "tcp" {
			t.Fatalf("typeNot leaked tcp event %s", e.Identity.ID)
		}
	}
}

func TestStreamEventsDesc_BatchingAndEarlyStop(t *testing.T) {
	s, _ := pageFixture(t)
	defer s.Close()
	ctx := context.Background()

	// 批次遍历：batch=2 → 3 批，顺序保持 DESC。
	var ids []string
	batches := 0
	err := s.StreamEventsDesc(ctx, EventPageQuery{SessionID: "s1"}, 2, func(batch []*event.Event) (bool, error) {
		batches++
		for _, e := range batch {
			ids = append(ids, string(e.Identity.ID))
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if batches != 3 || len(ids) != 6 {
		t.Fatalf("batches=%d ids=%d", batches, len(ids))
	}
	if ids[0] != "e6" || ids[5] != "e1" {
		t.Fatalf("stream order wrong: %v", ids)
	}

	// 提前终止：第一批后停止，仅消费 2 行。
	consumed := 0
	err = s.StreamEventsDesc(ctx, EventPageQuery{SessionID: "s1"}, 2, func(batch []*event.Event) (bool, error) {
		consumed += len(batch)
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if consumed != 2 {
		t.Fatalf("early stop consumed=%d want 2", consumed)
	}

	// TypeEq 下推与流式组合。
	var httpIDs []string
	err = s.StreamEventsDesc(ctx, EventPageQuery{SessionID: "s1", TypeEq: "http"}, 10, func(batch []*event.Event) (bool, error) {
		for _, e := range batch {
			httpIDs = append(httpIDs, string(e.Identity.ID))
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(httpIDs) != 2 || httpIDs[0] != "e4" {
		t.Fatalf("stream typeEq: %v", httpIDs)
	}
}

func TestStreamEvents_AscOrderWithStableTieBreak(t *testing.T) {
	s, base := pageFixture(t)
	defer s.Close()
	ctx := context.Background()

	// 正序流式：batch=2 → 3 批，顺序 e1..e6（与 DESC 镜像）。
	var ids []string
	batches := 0
	err := s.StreamEvents(ctx, EventPageQuery{SessionID: "s1"}, 2, func(batch []*event.Event) (bool, error) {
		batches++
		for _, e := range batch {
			ids = append(ids, string(e.Identity.ID))
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if batches != 3 || len(ids) != 6 {
		t.Fatalf("batches=%d ids=%d", batches, len(ids))
	}
	if ids[0] != "e1" || ids[5] != "e6" {
		t.Fatalf("asc order wrong: %v", ids)
	}

	// 完全相同的纳秒时间戳：按 id ASC 稳定排序（v1 约束：不允许随机次序）。
	sameTs := base.Truncate(time.Second)
	evs := []*event.Event{
		&event.Event{Identity: event.Identity{ID: "z-id", SessionID: "s2", Type: "tcp", Source: "test", Timestamp: sameTs}},
		&event.Event{Identity: event.Identity{ID: "a-id", SessionID: "s2", Type: "tcp", Source: "test", Timestamp: sameTs}},
		&event.Event{Identity: event.Identity{ID: "m-id", SessionID: "s2", Type: "tcp", Source: "test", Timestamp: sameTs}},
	}
	if err := s.AppendEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	var tieIDs []string
	if err := s.StreamEvents(ctx, EventPageQuery{SessionID: "s2"}, 10, func(batch []*event.Event) (bool, error) {
		for _, e := range batch {
			tieIDs = append(tieIDs, string(e.Identity.ID))
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(tieIDs) != 3 || tieIDs[0] != "a-id" || tieIDs[1] != "m-id" || tieIDs[2] != "z-id" {
		t.Fatalf("tie-break order wrong: %v", tieIDs)
	}
}

func TestEventPageQuery_TimeRangePushdown(t *testing.T) {
	s, base := pageFixture(t)
	defer s.Close()
	ctx := context.Background()

	from := base.Add(2 * time.Millisecond) // e3 起
	to := base.Add(4 * time.Millisecond)   // 至 e5
	q := EventPageQuery{SessionID: "s1", From: from, To: to}

	// 正序流式：时间窗口命中 e3, e4, e5。
	var asc []string
	if err := s.StreamEvents(ctx, q, 10, func(batch []*event.Event) (bool, error) {
		for _, e := range batch {
			asc = append(asc, string(e.Identity.ID))
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"e3", "e4", "e5"}
	if got := len(asc); got != len(want) {
		t.Fatalf("time range asc len=%d want %d", got, len(want))
	}
	for i := range want {
		if asc[i] != want[i] {
			t.Fatalf("time range asc order %v want %v", asc, want)
		}
	}

	// 倒序流式：同一窗口 e5, e4, e3。
	var desc []string
	if err := s.StreamEventsDesc(ctx, q, 10, func(batch []*event.Event) (bool, error) {
		for _, e := range batch {
			desc = append(desc, string(e.Identity.ID))
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(desc) != 3 || desc[0] != "e5" || desc[2] != "e3" {
		t.Fatalf("time range desc order %v", desc)
	}

	// 分页也支持下推：窗口 [2ms,4ms] 含 e3/e4/e5，倒序第 1 页为 e5, e4。
	page, total, err := s.QueryEventPage(ctx, EventPageQuery{SessionID: "s1", From: from, To: base.Add(4 * time.Millisecond)}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(page) != 2 {
		t.Fatalf("page total=%d len=%d", total, len(page))
	}
	if string(page[0].Identity.ID) != "e5" || string(page[1].Identity.ID) != "e4" {
		t.Fatalf("page order %v", []string{string(page[0].Identity.ID), string(page[1].Identity.ID)})
	}
}
