package store

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"gametrace/pkg/event"
)

// benchEvent 构造一条最小可入库事件（与 pageFixture 同款形态）。
func benchEvent(id string, ts time.Time) *event.Event {
	return &event.Event{
		Identity: event.Identity{
			ID: event.EventID(id), SessionID: "bench", Type: event.EventType("tcp"),
			Source: "bench", Timestamp: ts,
		},
		Payload: event.Payload{
			Value: event.Value{Kind: event.Object, Object: map[string]event.Value{
				"len": {Kind: event.Int, Int: 1448},
			}},
		},
	}
}

// BenchmarkAppendEvents 是解码产物落库的写路径基准：每迭代一批（事务 +
// prepared stmt），模拟 pipeline 每秒 flush 的批量节奏。
func BenchmarkAppendEvents(b *testing.B) {
	// NewSQLiteStore 会经 slog 默认 logger 打 INFO 到 stdout，与 -count 多轮的
	// Benchmark 行交错会破坏 benchstat 解析；基准期间静音、结束后恢复。
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)

	s, err := NewSQLiteStore(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	const batch = 100
	ctx := context.Background()
	base := time.Now()
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		events := make([]*event.Event, batch)
		for j := range events {
			i++
			events[j] = benchEvent(fmt.Sprintf("e-%d", i), base.Add(time.Duration(i)*time.Millisecond))
		}
		if err := s.AppendEvents(ctx, events); err != nil {
			b.Fatal(err)
		}
	}
}
