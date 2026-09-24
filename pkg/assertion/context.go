// Package assertion evaluates declarative game-interface assertions against
// decoded GameTrace events. It deliberately consumes events after semantic
// enrichment and does not participate in decoding or semantic rules.
package assertion

import (
	"encoding/json"
	"fmt"

	"gametrace/pkg/event"
)

// EventContext is the single expression document exposed to assertions.
// Meta carries event envelope facts and Data carries only business payload.
type EventContext struct {
	Meta     map[string]any `json:"meta"`
	Data     any            `json:"data"`
	Analysis any            `json:"analysis,omitempty"`
}

// NewEventContext converts a decoded event into the stable assertion view.
func NewEventContext(e *event.Event) (EventContext, error) {
	if e == nil {
		return EventContext{}, fmt.Errorf("nil event")
	}
	data, err := valueAny(e.Payload.Value)
	if err != nil {
		return EventContext{}, fmt.Errorf("payload: %w", err)
	}
	analysis, err := valueAny(e.Analysis)
	if err != nil {
		return EventContext{}, fmt.Errorf("analysis: %w", err)
	}
	metaValue, err := valueMap(e.Meta)
	if err != nil {
		return EventContext{}, fmt.Errorf("meta: %w", err)
	}
	direction := e.Context.Direction
	if direction == "" {
		if v, ok := metaValue["direction"].(string); ok {
			direction = v
		}
	}
	protocol, _ := metaValue["msg_name"].(string)
	semantic, _ := metaValue["semantic"].(string)
	return EventContext{Meta: map[string]any{
		"event_id":       string(e.Identity.ID),
		"session_id":     e.Identity.SessionID,
		"event_type":     string(e.Identity.Type),
		"timestamp":      e.Identity.Timestamp.UnixMilli(),
		"protocol":       protocol,
		"direction":      direction,
		"semantic":       semantic,
		"flow_id":        e.Context.FlowID,
		"conn_id":        e.Context.ConnID,
		"correlation_id": e.Trace.CorrelationID,
		"causation_id":   string(e.Trace.CausationID),
		"origin_id":      string(e.Trace.OriginID),
	}, Data: data, Analysis: analysis}, nil
}

func valueAny(v event.Value) (any, error) {
	b, err := v.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func valueMap(v event.Value) (map[string]any, error) {
	if v.Kind == event.Null {
		return map[string]any{}, nil
	}
	vv, err := valueAny(v)
	if err != nil {
		return nil, err
	}
	m, ok := vv.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object")
	}
	return m, nil
}
