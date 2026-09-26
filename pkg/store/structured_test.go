package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gametrace/pkg/event"
)

// TestWriteEventsV2_StructuredFields 验证 Event 结构化字段正确落库。
func TestWriteEventsV2_StructuredFields(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	now := time.Now()

	// 创建包含结构化字段的 Event
	payload1 := event.Value{
		Kind: event.Object,
		Object: map[string]event.Value{
			"src":       {Kind: event.String, Str: "127.0.0.1:5000"},
			"dst":       {Kind: event.String, Str: "127.0.0.1:8080"},
			"flow_id":   {Kind: event.Uint, Uint: 12345},
			"direction": {Kind: event.String, Str: "client_to_server"},
			"msg_name":  {Kind: event.String, Str: "LoginReq"},
			"is_push":   {Kind: event.Bool, Bool: false},
			"type":      {Kind: event.String, Str: "request"},
			"method":    {Kind: event.String, Str: "POST"},
		},
	}

	payload2 := event.Value{
		Kind: event.Object,
		Object: map[string]event.Value{
			"src":       {Kind: event.String, Str: "127.0.0.1:8080"},
			"dst":       {Kind: event.String, Str: "127.0.0.1:5000"},
			"flow_id":   {Kind: event.Uint, Uint: 12345},
			"direction": {Kind: event.String, Str: "server_to_client"},
			"msg_name":  {Kind: event.String, Str: "LoginResp"},
			"is_push":   {Kind: event.Bool, Bool: false},
			"type":      {Kind: event.String, Str: "response"},
			"status":    {Kind: event.String, Str: "200"},
		},
	}

	events := []*event.Event{
		{
			Identity: event.Identity{
				ID:        "ev-1",
				SessionID: "test-session",
				Type:      "tcp",
				Source:    "test",
				Timestamp: now,
			},
			Trace: event.TraceContext{},
			Payload: event.Payload{
				Value:    payload1,
			},
		},
		{
			Identity: event.Identity{
				ID:        "ev-2",
				SessionID: "test-session",
				Type:      "tcp",
				Source:    "test",
				Timestamp: now.Add(50 * time.Millisecond),
			},
			Trace: event.TraceContext{},
			Payload: event.Payload{
				Value:    payload2,
			},
		},
	}

	if err := s.AppendEvents(ctx, events); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	// 查库验证 ev-1
	row := s.db.QueryRowContext(ctx,
		"SELECT payload FROM events WHERE id=?", "ev-1")
	var payloadBytes []byte
	if err := row.Scan(&payloadBytes); err != nil {
		t.Fatalf("scan ev-1: %v", err)
	}

	// 反序列化 payload
	storedPayload, err := event.UnmarshalValueMsgpack(payloadBytes)
	if err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	obj, ok := storedPayload.AsObject()
	if !ok {
		t.Fatal("payload is not an object")
	}

	// 验证字段
	if src, exists := obj["src"]; !exists || src.Str != "127.0.0.1:5000" {
		t.Errorf("src = %v, want 127.0.0.1:5000", src)
	}
	if direction, exists := obj["direction"]; !exists || direction.Str != "client_to_server" {
		t.Errorf("direction = %v, want client_to_server", direction)
	}
	if msgName, exists := obj["msg_name"]; !exists || msgName.Str != "LoginReq" {
		t.Errorf("msg_name = %v, want LoginReq", msgName)
	}
	if flowID, exists := obj["flow_id"]; !exists || flowID.Uint != 12345 {
		t.Errorf("flow_id = %v, want 12345", flowID)
	}
	if isPush, exists := obj["is_push"]; !exists || isPush.Bool != false {
		t.Errorf("is_push = %v, want false", isPush)
	}

	// 查库验证 ev-2
	row2 := s.db.QueryRowContext(ctx,
		"SELECT payload FROM events WHERE id=?", "ev-2")
	if err := row2.Scan(&payloadBytes); err != nil {
		t.Fatalf("scan ev-2: %v", err)
	}

	storedPayload2, err := event.UnmarshalValueMsgpack(payloadBytes)
	if err != nil {
		t.Fatalf("unmarshal payload2: %v", err)
	}

	obj2, ok := storedPayload2.AsObject()
	if !ok {
		t.Fatal("payload2 is not an object")
	}

	if direction, exists := obj2["direction"]; !exists || direction.Str != "server_to_client" {
		t.Errorf("ev-2 direction = %v, want server_to_client", direction)
	}
	if msgName, exists := obj2["msg_name"]; !exists || msgName.Str != "LoginResp" {
		t.Errorf("ev-2 msg_name = %v, want LoginResp", msgName)
	}
}

// TestWriteEventsV2_EmptyPayload 验证 Event 空 payload 能正常落库。
func TestWriteEventsV2_EmptyPayload(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// 创建空 payload 的 Event
	events := []*event.Event{
		{
			Identity: event.Identity{
				ID:        "ev-empty",
				SessionID: "test-session",
				Type:      "tcp",
				Source:    "test",
				Timestamp: time.Now(),
			},
			Trace: event.TraceContext{},
			Payload: event.Payload{
				Value: event.Value{
					Kind:   event.Object,
					Object: map[string]event.Value{},
				},
			},
		},
	}

	if err := s.AppendEvents(ctx, events); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	// 验证能正常读取
	rows, err := s.QueryEvents(ctx, "test-session", 100, 0)
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rows))
	}
	if string(rows[0].Identity.ID) != "ev-empty" {
		t.Errorf("expected ID 'ev-empty', got '%s'", rows[0].Identity.ID)
	}
}

// TestWriteStateChanges 验证 WriteStateChanges 写入 state_changes 表与查询。
func TestWriteStateChanges(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	flowID := "999"
	payload := event.ValueObject(map[string]event.Value{
		"_meta": event.ValueObject(map[string]event.Value{
			"flow_id": event.ValueString(flowID),
		}),
		"_state_changes": {Kind: event.Array, Array: []event.Value{
			event.ValueObject(map[string]event.Value{
				"subject_type": event.ValueString("Building"),
				"subject_id":   event.ValueString("1001"),
				"op":           event.ValueString("set"),
				"path":         event.ValueString("level"),
				"after":        event.ValueInt(3),
			}),
			event.ValueObject(map[string]event.Value{
				"subject_type": event.ValueString("Building"),
				"subject_id":   event.ValueString("1001"),
				"op":           event.ValueString("set"),
				"path":         event.ValueString("cost"),
				"after":        event.ValueInt(100),
			}),
			event.ValueObject(map[string]event.Value{
				"subject_type": event.ValueString("Hero"),
				"subject_id":   event.ValueString("500"),
				"op":           event.ValueString("delete"),
				"path":         event.ValueString("name"),
			}),
		}},
	})
	events := []*event.Event{
		event.NewEvent("test-session", "tcp", "test", payload, event.EventContext{FlowID: flowID}),
	}

	if err := writeStateChangesFromEvents(t, s, "test-session", events); err != nil {
		t.Fatalf("WriteStateChanges: %v", err)
	}

	// 查询 state_changes 表验证
	rows, err := s.db.QueryContext(ctx,
		"SELECT subject_type, subject_id, op, path, after_value FROM state_changes WHERE session_id=? AND flow_id=? ORDER BY subject_type, path",
		"test-session", flowID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type record struct{ subjectType, subjectID, op, path, value string }
	var records []record
	for rows.Next() {
		var subjectType, subjectID, op, path string
		var afterValue sql.NullString
		if err := rows.Scan(&subjectType, &subjectID, &op, &path, &afterValue); err != nil {
			t.Fatalf("scan: %v", err)
		}
		val := ""
		if afterValue.Valid {
			val = afterValue.String
		}
		records = append(records, record{subjectType, subjectID, op, path, val})
	}

	if len(records) != 3 {
		t.Fatalf("records count = %d, want 3", len(records))
	}

	// 验证第一条：Building/1001/cost
	r := records[0]
	if r.subjectType != "Building" || r.path != "cost" || r.op != "set" {
		t.Errorf("record[0] = %+v, want Building/cost/set", r)
	}
	if r.value != "100" {
		t.Errorf("record[0].after_value = %q, want 100", r.value)
	}

	// 验证 delete 操作 after_value 为 JSON null
	rDelete := records[2]
	if rDelete.op != "delete" {
		t.Errorf("record[2].op = %q, want delete", rDelete.op)
	}
	if rDelete.value != "null" {
		t.Errorf("record[2].after_value = %q, want null (JSON null)", rDelete.value)
	}
}

// TestWriteStateChanges_SkipsInvalid 验证不完整的 StateChange 会被跳过，不会写入投影。
func TestWriteStateChanges_SkipsInvalid(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	flowID := "999"
	payload := event.ValueObject(map[string]event.Value{
		"_state_changes": {Kind: event.Array, Array: []event.Value{
			// 有效
			event.ValueObject(map[string]event.Value{
				"subject_type": event.ValueString("Building"),
				"subject_id":   event.ValueString("1001"),
				"op":           event.ValueString("set"),
				"path":         event.ValueString("level"),
				"after":        event.ValueInt(3),
			}),
			// 无效：缺少 subject_id
			event.ValueObject(map[string]event.Value{
				"subject_type": event.ValueString("Building"),
				"op":           event.ValueString("set"),
				"path":         event.ValueString("cost"),
			}),
			// 无效：非法 op
			event.ValueObject(map[string]event.Value{
				"subject_type": event.ValueString("Hero"),
				"subject_id":   event.ValueString("500"),
				"op":           event.ValueString("patch"),
				"path":         event.ValueString("name"),
			}),
		}},
	})
	events := []*event.Event{
		event.NewEvent("test-session", "tcp", "test", payload, event.EventContext{FlowID: flowID}),
	}

	if err := writeStateChangesFromEvents(t, s, "test-session", events); err != nil {
		t.Fatalf("WriteStateChanges: %v", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM state_changes WHERE session_id=? AND flow_id=?",
		"test-session", flowID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 valid state change, got %d", count)
	}
}

// TestSchemaMigration_Idempotent 验证 schema 迁移幂等（多次启动不报错）。
func TestSchemaMigration_Idempotent(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")

	// 第一次创建
	s1, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("first NewSQLiteStore: %v", err)
	}
	s1.Close()

	// 第二次打开（模拟重启），迁移应幂等
	s2, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("second NewSQLiteStore: %v", err)
	}
	defer s2.Close()

	// 验证表存在
	ctx := context.Background()
	var name string
	err = s2.db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name='state_changes'").Scan(&name)
	if err != nil {
		t.Errorf("state_changes table not found after migration: %v", err)
	}
}

// TestQueryStateChangesResolvedFlags 验证 before_resolved / after_resolved 写库后能读回来。
// 这两个标记是「首见」与「真的从 null 变过来」的唯一区分依据，消费方（前端/MCP）依赖它，
// 所以查询侧必须把它们一起取出来。
func TestQueryStateChangesResolvedFlags(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	ts := time.Unix(1700000000, 0)
	changes := []EnrichedStateChange{
		{
			StateChange: event.StateChange{
				SubjectType: "Player", SubjectID: "1", Op: "set", Path: "hp",
				Before: event.ValueInt(100), After: event.ValueInt(80),
			},
			EventID: "ev1", FlowID: "f1", Timestamp: ts,
			BeforeResolved: true, AfterResolved: true,
		},
		{
			// 首见：没有基线，before 未解析。
			StateChange: event.StateChange{
				SubjectType: "Player", SubjectID: "1", Op: "set", Path: "exp",
				After: event.ValueInt(5),
			},
			EventID: "ev1", FlowID: "f1", Timestamp: ts,
		},
	}
	if err := s.WriteEnrichedStateChanges(ctx, "sess", changes); err != nil {
		t.Fatalf("WriteEnrichedStateChanges: %v", err)
	}

	rows, err := s.QueryStateChanges(ctx, StateChangeQuery{SessionID: "sess"})
	if err != nil {
		t.Fatalf("QueryStateChanges: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("行数 = %d, want 2", len(rows))
	}
	for _, r := range rows {
		switch r.Path {
		case "hp":
			if !r.BeforeResolved || !r.AfterResolved {
				t.Errorf("hp: resolved = (%v,%v), want (true,true)", r.BeforeResolved, r.AfterResolved)
			}
		case "exp":
			if r.BeforeResolved || r.AfterResolved {
				t.Errorf("exp 是首见，resolved 应为 false: (%v,%v)", r.BeforeResolved, r.AfterResolved)
			}
		default:
			t.Errorf("unexpected path %q", r.Path)
		}
	}
}

// writeStateChangesFromEvents 把事件里声明的 _state_changes 手工落成投影行。
//
// 生产路径是 pkg/state 的基线富化 → WriteEnrichedStateChanges；这里只验证 Store 层的
// 行校验与写入（非法条目跳过、列值正确），所以不绕道基线，直接构造投影条目。
func writeStateChangesFromEvents(t *testing.T, s *SQLiteStore, sessionID string, events []*event.Event) error {
	t.Helper()
	var changes []EnrichedStateChange
	for _, ev := range events {
		for _, sc := range ev.ExtractStateChanges() {
			changes = append(changes, EnrichedStateChange{
				StateChange: sc,
				EventID:     ev.Identity.ID,
				FlowID:      ev.Context.FlowID,
				Timestamp:   ev.Identity.Timestamp,
			})
		}
	}
	return s.WriteEnrichedStateChanges(context.Background(), sessionID, changes)
}

// TestQueryStateChangesOrdersBySeq 验证同一纳秒内的多条变化按 seq（插件上报次序）返回。
//
// 主键是 uuid，只按 id 排序等于随机序 —— 分页会重复/漏行，展示顺序也与上报不符。
// 这里刻意让 id 顺序与 seq 顺序相反：只有真的用 seq 排序才能得到 c,b,a。
func TestQueryStateChangesOrdersBySeq(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	ts := time.Unix(1700000000, 0).UnixNano()
	for _, r := range []struct {
		id   string
		path string
		seq  int
	}{{"zzz", "c", 0}, {"mmm", "b", 1}, {"aaa", "a", 2}} {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO state_changes(id, event_id, session_id, timestamp, subject_type, subject_id, op, path, after_value, before_resolved, after_resolved, seq)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.id, "ev1", "sess", ts, "Player", "1", "set", r.path, "1", 0, 1, r.seq); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	rows, err := s.QueryStateChanges(ctx, StateChangeQuery{SessionID: "sess"})
	if err != nil {
		t.Fatalf("QueryStateChanges: %v", err)
	}
	var gotPaths []string
	for _, r := range rows {
		gotPaths = append(gotPaths, r.Path)
	}
	if want := []string{"c", "b", "a"}; !reflect.DeepEqual(gotPaths, want) {
		t.Errorf("返回顺序 = %v, want %v（应按 seq，而不是 uuid）", gotPaths, want)
	}

	// 写入侧也要带上 seq。
	got, err := s.QueryStateChanges(ctx, StateChangeQuery{SessionID: "sess", Path: "a"})
	if err != nil {
		t.Fatalf("QueryStateChanges: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 2 {
		t.Errorf("seq 读回 = %+v, want 2", got)
	}
}

// TestWriteEnrichedStateChangesPersistsSeq 验证写入侧的 seq 真的落到列上（而不是恒 0）。
func TestWriteEnrichedStateChangesPersistsSeq(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	changes := []EnrichedStateChange{
		{
			StateChange: event.StateChange{
				SubjectType: "Player", SubjectID: "1", Op: "set", Path: "hp",
				After: event.ValueInt(80),
			},
			EventID: "ev1", Timestamp: time.Unix(1700000000, 0), Seq: 5,
		},
	}
	if err := s.WriteEnrichedStateChanges(ctx, "sess", changes); err != nil {
		t.Fatalf("WriteEnrichedStateChanges: %v", err)
	}
	rows, err := s.QueryStateChanges(ctx, StateChangeQuery{SessionID: "sess"})
	if err != nil {
		t.Fatalf("QueryStateChanges: %v", err)
	}
	if len(rows) != 1 || rows[0].Seq != 5 {
		t.Fatalf("落库的 seq = %+v, want 5", rows)
	}
}

// TestEventContextPeerKeyRoundTrip 验证 EventContext.PeerKey 能穿过存储层往返。
//
// 实体基线用 PeerKey 跨会话延续（见 pkg/state 的 Scope），这依赖 context 的 msgpack
// 编解码与落库读取都不丢字段；漏了任何一环，基线就会静默退回会话维度而没人发现。
func TestEventContextPeerKeyRoundTrip(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	const peer = "10.0.0.1|10.0.0.2:9250"
	ev := event.NewEvent("test-session", "tcp", "test",
		event.ValueObject(map[string]event.Value{"msg_name": event.ValueString("Login")}),
		event.EventContext{FlowID: "f1", PeerKey: peer})
	if err := s.AppendEvents(ctx, []*event.Event{ev}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	got, err := s.QueryEvents(ctx, "test-session", 10, 0)
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("事件数 = %d, want 1", len(got))
	}
	if got[0].Context.PeerKey != peer {
		t.Errorf("PeerKey = %q, want %q", got[0].Context.PeerKey, peer)
	}
}
