package event

import (
	"testing"

	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
)

func TestDraftToResponse(t *testing.T) {
	d := Draft{
		Type:             "game.player.moved",
		Value:            ValueObject(map[string]Value{"player_id": ValueInt(7), "hp": ValueInt(100)}),
		CorrelationKey:   "sess-42",
		CausationInputID: "in-0",
	}
	resp, err := d.ToResponse("in-1")
	if err != nil {
		t.Fatalf("ToResponse: %v", err)
	}
	// input-id-echo
	if resp.GetInputId() != "in-1" {
		t.Errorf("input_id echo: got %q", resp.GetInputId())
	}
	// payload-non-empty
	if resp.GetEventType() != "game.player.moved" {
		t.Errorf("event_type not propagated: %+v", resp)
	}
	if len(resp.GetPayloadMsgpack()) == 0 {
		t.Errorf("payload must be non-empty")
	}
	// 可经 Value 解码回 object
	v, err := UnmarshalValueMsgpack(resp.GetPayloadMsgpack())
	if err != nil || v.Kind != Object {
		t.Errorf("round-trip failed: %v %v", err, v.Kind)
	}
	// 关联/因果 hint 必须透传：宿主 dispatcher 据此做 WithCorrelation /
	// WithCausation，漏发是静默故障（无报错，但因果链断）。
	if resp.GetCorrelationKey() != "sess-42" {
		t.Errorf("correlation_key dropped: got %q", resp.GetCorrelationKey())
	}
	if resp.GetCausationInputId() != "in-0" {
		t.Errorf("causation_input_id dropped: got %q", resp.GetCausationInputId())
	}
}

func TestDraftValidateRootMustBeObject(t *testing.T) {
	d := Draft{Type: "x", Value: ValueInt(1)}
	if err := d.Validate(); err == nil {
		t.Fatal("expected root-must-be-object error")
	}
	if _, err := d.ToResponse("in-1"); err == nil {
		t.Fatal("ToResponse should fail on non-object root")
	}
}

func TestDraftDone(t *testing.T) {
	resp := Done("in-9")
	if !resp.GetDone() || resp.GetInputId() != "in-9" {
		t.Errorf("Done response malformed: %+v", resp)
	}
	var _ = pb.DecodeResponseV2{}
}
