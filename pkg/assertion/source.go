package assertion

import (
	"context"
	"sort"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"
)

// SourceQuery selects decoded events from an assertion source.
type SourceQuery struct {
	SessionID string
	From, To  time.Time
}

// EventSource is the assertion engine's read-only upstream boundary.
type EventSource interface {
	Events(context.Context, SourceQuery) ([]*event.Event, error)
}

// MemorySource is useful for unit tests and for adapters that already hold a
// capture session in memory. It always returns deterministic timestamp/ID order.
type MemorySource []*event.Event

func (s MemorySource) Events(_ context.Context, q SourceQuery) ([]*event.Event, error) {
	out := make([]*event.Event, 0, len(s))
	for _, e := range s {
		if e == nil || (q.SessionID != "" && e.Identity.SessionID != q.SessionID) {
			continue
		}
		if !q.From.IsZero() && e.Identity.Timestamp.Before(q.From) {
			continue
		}
		if !q.To.IsZero() && e.Identity.Timestamp.After(q.To) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Identity.Timestamp.Equal(out[j].Identity.Timestamp) {
			return out[i].Identity.ID < out[j].Identity.ID
		}
		return out[i].Identity.Timestamp.Before(out[j].Identity.Timestamp)
	})
	return out, nil
}

// CaptureSessionSource adapts the existing immutable event store to the
// assertion input boundary. A non-positive Limit uses a bounded default so a
// malformed case cannot accidentally load an unbounded capture session.
type CaptureSessionSource struct {
	Reader store.EventReader
	Limit  int
}

func (s CaptureSessionSource) Events(ctx context.Context, q SourceQuery) ([]*event.Event, error) {
	limit := s.Limit
	if limit <= 0 {
		limit = 10_000
	}
	if !q.From.IsZero() || !q.To.IsZero() {
		return s.Reader.QueryEventsInRange(ctx, q.SessionID, q.From, q.To, limit)
	}
	return s.Reader.QueryEvents(ctx, q.SessionID, limit, 0)
}
