package assertion

import (
	"testing"
	"time"

	"gametrace/pkg/event"
)

func testEvent(id, protocol string, at time.Time, payload map[string]any) *event.Event {
	e := event.NewEventWithTime("capture-1", "game.message", "decoder", event.ValueFromMap(payload), at, event.EventContext{Direction: "S2C", FlowID: "flow-1"})
	e.Identity.ID = event.EventID(id)
	e.Meta = event.ValueFromMap(map[string]any{"msg_name": protocol, "semantic": "response"})
	return e
}

func TestEngineImmediateEventuallyNeverAndCapture(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	created := testEvent("a", "CreateRoleRsp", start.Add(time.Second), map[string]any{"roleId": 10001, "code": 0})
	synced := testEvent("b", "QueryRolePush", start.Add(2*time.Second), map[string]any{"roleId": 10001})
	caseDef, err := Load([]byte(`
api_version: gametrace.assertion/v1
kind: GameInterfaceCase
source: {kind: capture_session, session_id: capture-1}
assertions:
  - name: create-role
    trigger: {protocol: CreateRoleRsp}
    check: [data.code == 0]
    capture: {roleId: {from: data.roleId}}
  - name: role-sync
    eventually: {timeout: 10s}
    trigger: {protocol: QueryRolePush}
    check: [data.roleId == $roleId]
  - name: no-error
    never: {timeout: 10s}
    trigger: {protocol: ErrorPush}
    check: [data.code == 100]
`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Engine{}).Evaluate(caseDef, []*event.Event{synced, created}, start)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Status != Pass {
			t.Errorf("%s: got %s (%s)", r.Name, r.Status, r.Reason)
		}
	}
}

func TestEngineNeverFailsOnlyWhenCheckMatches(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	bad := testEvent("e", "ErrorPush", start.Add(time.Second), map[string]any{"code": 100})
	c := &Case{Assertions: []Assertion{{Name: "no-error", Never: &Window{Timeout: time.Second}, Trigger: Trigger{Protocol: "ErrorPush"}, Check: []string{"data.code == 100"}}}}
	got, err := (Engine{}).Evaluate(c, []*event.Event{bad}, start)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != Fail {
		t.Fatalf("got %s, want fail", got[0].Status)
	}
}

func TestEventContextUsesPayloadAndMeta(t *testing.T) {
	e := testEvent("event-1", "LoginRsp", time.Unix(1, 0), map[string]any{"code": 0})
	c, err := NewEventContext(e)
	if err != nil {
		t.Fatal(err)
	}
	if c.Meta["protocol"] != "LoginRsp" {
		t.Fatalf("protocol = %v", c.Meta["protocol"])
	}
	data := c.Data.(map[string]any)
	if data["code"] != float64(0) {
		t.Fatalf("code = %#v", data["code"])
	}
}

func TestExpressionHelpers(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	e := testEvent("event-1", "LoginRsp", start, map[string]any{"name": "alice-100", "code": 0})
	c := &Case{Assertions: []Assertion{{Name: "helpers", Immediate: true, Trigger: Trigger{Protocol: "LoginRsp"}, Check: []string{
		"exists(data.code)",
		`contains(data.name, "alice")`,
		`match(data.name, "alice-[0-9]+")`,
	}}}}
	got, err := (Engine{}).Evaluate(c, []*event.Event{e}, start)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != Pass {
		t.Fatalf("got %s (%s)", got[0].Status, got[0].Reason)
	}
}
