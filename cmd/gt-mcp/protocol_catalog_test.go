package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"gametrace/pkg/event"
	"gametrace/pkg/store"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

// catalogFixture 构造 get_protocol_catalog 测试数据（时间正序）：
//
//	t00   hb1 HeartBeat  C→S  push                  {seq:1}
//	t500  hb2 HeartBeat  C→S  push                  {seq:2}
//	t1000 hb3 HeartBeat  C→S  push                  {seq:3}
//	t2000 l1  Login      C→S  semantic=[request]    {account, hero_id}         （causation 目标）
//	t2086 l2  LoginAck   S→C  semantic=[response]   {code, hero_id, extra}     causation=l1
//	t3000 a1  Attack     C→S  semantic=[request]    {scene_id, target_id}
//	t3100 a2  Attack     C→S  semantic=[request,error] {scene_id}              （混合 semantic）
//	t4000 p1  PushNearby S→C  push, semantic=[notification] {npcs:[{id,hp}]}  （Context 方向兜底）
//	t5000 u1  无名称     S→C                                              （只计 unnamed）
func writeCatalogFixture(t *testing.T, st *store.SQLiteStore, sessionID string) {
	t.Helper()
	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	meta := func(dir, msg string, semantic []string, push *bool) event.Value {
		m := map[string]event.Value{
			"direction": event.ValueString(dir),
			"msg_name":  event.ValueString(msg),
		}
		if len(semantic) > 0 {
			labels := make([]event.Value, 0, len(semantic))
			for _, s := range semantic {
				labels = append(labels, event.ValueString(s))
			}
			m["semantic"] = event.ValueArray(labels)
		}
		if push != nil {
			m["is_push"] = event.ValueBool(*push)
		}
		return event.ValueObject(m)
	}
	true_, false_ := true, false
	mk := func(id string, at time.Time, ctxDir, dir, msg string, semantic []string, push *bool, payload map[string]any, caus string) *event.Event {
		ev := event.NewEventWithTime(sessionID, event.EventType("proto.msg"), "test",
			event.ValueFromMap(payload), at, event.EventContext{Direction: ctxDir})
		ev.Identity.ID = event.EventID(id)
		ev.Meta = meta(dir, msg, semantic, push)
		if caus != "" {
			ev.Trace = event.NewTraceContext(event.EventID(caus), "", "")
		}
		return ev
	}
	evs := []*event.Event{
		mk("hb1", base.Add(0*time.Millisecond), "client_to_server", "client_to_server", "HeartBeat", nil, &true_, map[string]any{"seq": int64(1)}, ""),
		mk("hb2", base.Add(500*time.Millisecond), "client_to_server", "client_to_server", "HeartBeat", nil, &true_, map[string]any{"seq": int64(2)}, ""),
		mk("hb3", base.Add(1000*time.Millisecond), "client_to_server", "client_to_server", "HeartBeat", nil, &true_, map[string]any{"seq": int64(3)}, ""),
		mk("l1", base.Add(2000*time.Millisecond), "client_to_server", "client_to_server", "Login", []string{"request"}, &false_, map[string]any{"account": "alice", "hero_id": int64(7)}, ""),
		mk("l2", base.Add(2086*time.Millisecond), "server_to_client", "server_to_client", "LoginAck", []string{"response"}, &false_, map[string]any{"code": int64(0), "hero_id": int64(7), "extra": "ok"}, "l1"),
		mk("a1", base.Add(3000*time.Millisecond), "client_to_server", "client_to_server", "Attack", []string{"request"}, &false_, map[string]any{"scene_id": int64(100), "target_id": int64(9)}, ""),
		mk("a2", base.Add(3100*time.Millisecond), "client_to_server", "client_to_server", "Attack", []string{"request", "error"}, &false_, map[string]any{"scene_id": int64(100)}, ""),
		// PushNearby 只写 Context.Direction：验证方向兜底仍正确聚合。
		mk("p1", base.Add(4000*time.Millisecond), "server_to_client", "server_to_client", "PushNearby", []string{"notification"}, &true_, map[string]any{"npcs": []any{map[string]any{"id": int64(1), "hp": int64(50)}}}, ""),
		// 无 msg_name：只进 summary.unnamed_events。
		mk("u1", base.Add(5000*time.Millisecond), "server_to_client", "server_to_client", "", nil, nil, map[string]any{"x": int64(1)}, ""),
	}
	if err := st.AppendEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// newCatalogMCP 构造带 catalog fixture 的 mcpCapture 测试环境。
func newCatalogMCP(t *testing.T) (*mcpCapture, string) {
	t.Helper()
	workDir := t.TempDir()
	sessionMgr := newSessionManager(workDir)
	m := &mcpCapture{
		workDir:      workDir,
		sessionMgr:   sessionMgr,
		authz:        newProjectAuthorizer(nil),
		readerOpener: sqliteReaderOpener(),
	}
	sessionID := sessionMgr.generateSessionID()
	if err := os.MkdirAll(sessionMgr.sessionDir(sessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := sessionMgr.absDBPath(sessionID)
	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	writeCatalogFixture(t, st, sessionID)
	st.Close()
	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID,
		StartedAt: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Status:    "stopped",
		DBPath:    dbPath,
	}); err != nil {
		t.Fatal(err)
	}
	return m, sessionID
}

// callCatalogTool 调用 get_protocol_catalog 并解码 JSON；IsError 时返回原样错误文本。
func callCatalogTool(t *testing.T, m *mcpCapture, ctx context.Context, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := m.handleGetProtocolCatalog(ctx, req)
	if err != nil {
		t.Fatalf("get_protocol_catalog: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(contentText(res)), &out); err != nil {
		t.Fatalf("unmarshal get_protocol_catalog response: %v", err)
	}
	return out
}

// catalogByMsg 按 msg_name 索引协议项。
func catalogByMsg(t *testing.T, protocols []any) map[string]map[string]any {
	t.Helper()
	byMsg := map[string]map[string]any{}
	for _, p := range protocols {
		pm := p.(map[string]any)
		byMsg[pm["msg_name"].(string)] = pm
	}
	return byMsg
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	}
	return 0
}

// TestProtocolCatalogAggregation 场景 1/3/7：聚合、count、first/last、interval、samples。
func TestProtocolCatalogAggregation(t *testing.T) {
	m, sessionID := newCatalogMCP(t)
	out := callCatalogTool(t, m, context.Background(), map[string]any{"session_id": sessionID})

	if got := num(out["total_protocols"]); got != 5 {
		t.Fatalf("total_protocols = %v, want 5", got)
	}
	if got := out["has_more"]; got != false {
		t.Fatalf("has_more = %v, want false", got)
	}
	sum := out["summary"].(map[string]any)
	if got := num(sum["event_count"]); got != 9 {
		t.Fatalf("event_count = %v, want 9", got)
	}
	if got := num(sum["client_to_server_events"]); got != 6 {
		t.Fatalf("client_to_server_events = %v, want 6", got)
	}
	if got := num(sum["server_to_client_events"]); got != 3 {
		t.Fatalf("server_to_client_events = %v, want 3", got)
	}
	if got := num(sum["unnamed_events"]); got != 1 {
		t.Fatalf("unnamed_events = %v, want 1", got)
	}

	// 排序：first_seen ASC（HeartBeat < Login < LoginAck < Attack < PushNearby）。
	protos := out["protocols"].([]any)
	wantOrder := []string{"HeartBeat", "Login", "LoginAck", "Attack", "PushNearby"}
	for i, w := range wantOrder {
		got := protos[i].(map[string]any)["msg_name"].(string)
		if got != w {
			t.Fatalf("protocol order[%d] = %s, want %s", i, got, w)
		}
	}

	hb := catalogByMsg(t, protos)["HeartBeat"]
	if got := num(hb["count"]); got != 3 {
		t.Fatalf("HeartBeat count = %v, want 3", got)
	}
	iv := hb["interval_ms"].(map[string]any)
	if got := num(iv["count"]); got != 2 {
		t.Fatalf("HeartBeat interval count = %v, want 2", got)
	}
	if got := num(iv["min"]); got != 500 {
		t.Fatalf("HeartBeat interval min = %v, want 500", got)
	}
	if got := num(iv["max"]); got != 500 {
		t.Fatalf("HeartBeat interval max = %v, want 500", got)
	}
	samples := hb["sample_event_ids"].([]any)
	if len(samples) != 3 || samples[0] != "hb1" || samples[2] != "hb3" {
		t.Fatalf("HeartBeat samples = %v", samples)
	}

	// 单次出现协议：interval count=0 且全 null。
	login := catalogByMsg(t, protos)["Login"]
	iv = login["interval_ms"].(map[string]any)
	if got := num(iv["count"]); got != 0 {
		t.Fatalf("Login interval count = %v, want 0", got)
	}
	for _, k := range []string{"min", "p50", "p95", "max"} {
		if iv[k] != nil {
			t.Fatalf("Login interval %s = %v, want null", k, iv[k])
		}
	}
}

// TestProtocolCatalogDirectionFilter 场景 2：direction=client_to_server 不含 S→C。
func TestProtocolCatalogDirectionFilter(t *testing.T) {
	m, sessionID := newCatalogMCP(t)
	out := callCatalogTool(t, m, context.Background(), map[string]any{
		"session_id": sessionID, "direction": "client_to_server",
	})
	if got := num(out["total_protocols"]); got != 3 {
		t.Fatalf("c2s total_protocols = %v, want 3", got)
	}
	for _, p := range out["protocols"].([]any) {
		pm := p.(map[string]any)
		if pm["direction"] != "client_to_server" {
			t.Fatalf("direction filter leaked %v", pm["direction"])
		}
	}
	sum := out["summary"].(map[string]any)
	if got := num(sum["client_to_server_events"]); got != 6 {
		t.Fatalf("c2s filtered client_to_server_events = %v, want 6", got)
	}
	if got := num(sum["server_to_client_events"]); got != 0 {
		t.Fatalf("c2s filtered server_to_client_events = %v, want 0", got)
	}
	if got := num(sum["unnamed_events"]); got != 0 {
		t.Fatalf("c2s filtered unnamed_events = %v, want 0", got)
	}

	// 反向过滤。
	out = callCatalogTool(t, m, context.Background(), map[string]any{
		"session_id": sessionID, "direction": "server_to_client",
	})
	if got := num(out["total_protocols"]); got != 2 {
		t.Fatalf("s2c total_protocols = %v, want 2", got)
	}
}

// TestProtocolCatalogFields 场景 4 + 字段路径提取（嵌套 / 数组折叠）。
func TestProtocolCatalogFields(t *testing.T) {
	m, sessionID := newCatalogMCP(t)
	out := callCatalogTool(t, m, context.Background(), map[string]any{"session_id": sessionID})
	protos := out["protocols"].([]any)

	attack := catalogByMsg(t, protos)["Attack"]
	fields := map[string]any{}
	for _, f := range attack["fields"].([]any) {
		fm := f.(map[string]any)
		fields[fm["path"].(string)] = fm["observed_count"]
	}
	// scene_id 在 2 个 event 出现（a1, a2），target_id 仅 a1。
	if got := num(fields["scene_id"]); got != 2 {
		t.Fatalf("Attack scene_id observed_count = %v, want 2", got)
	}
	if got := num(fields["target_id"]); got != 1 {
		t.Fatalf("Attack target_id observed_count = %v, want 1", got)
	}

	// 数组折叠：npcs 数组项叶子为 npcs[].id / npcs[].hp，不暴露下标。
	push := catalogByMsg(t, protos)["PushNearby"]
	fields = map[string]any{}
	for _, f := range push["fields"].([]any) {
		fm := f.(map[string]any)
		fields[fm["path"].(string)] = fm["observed_count"]
	}
	if got := num(fields["npcs[].id"]); got != 1 {
		t.Fatalf("PushNearby npcs[].id = %v, want 1", got)
	}
	if got := num(fields["npcs[].hp"]); got != 1 {
		t.Fatalf("PushNearby npcs[].hp = %v, want 1", got)
	}
	if _, ok := fields["npcs[0].id"]; ok {
		t.Fatalf("array index path leaked npcs[0].id")
	}
}

// TestProtocolCatalogSemanticAndPush 场景：semantic 求唯一值集合；is_push 三态。
func TestProtocolCatalogSemanticAndPush(t *testing.T) {
	m, sessionID := newCatalogMCP(t)
	out := callCatalogTool(t, m, context.Background(), map[string]any{"session_id": sessionID})
	protos := out["protocols"].([]any)
	byMsg := catalogByMsg(t, protos)

	attack := byMsg["Attack"]
	sem := []string{}
	for _, s := range attack["semantic"].([]any) {
		sem = append(sem, s.(string))
	}
	if len(sem) != 2 || sem[0] != "error" || sem[1] != "request" {
		t.Fatalf("Attack semantic union = %v, want [error request]", sem)
	}

	hb := byMsg["HeartBeat"]
	if isPush := hb["is_push"]; isPush != true {
		t.Fatalf("HeartBeat is_push = %v, want true", isPush)
	}
	login := byMsg["Login"]
	if isPush := login["is_push"]; isPush != false {
		t.Fatalf("Login is_push = %v, want false", isPush)
	}
	// PushNearby 方向来自 Context 兜底，仍应正确聚合并标记 push。
	push := byMsg["PushNearby"]
	if push["direction"] != "server_to_client" {
		t.Fatalf("PushNearby direction = %v", push["direction"])
	}
	if isPush := push["is_push"]; isPush != true {
		t.Fatalf("PushNearby is_push = %v, want true", isPush)
	}
}

// TestProtocolCatalogPairing 场景 5/6：causation 配对与 latency；无 pair 时为空数组。
func TestProtocolCatalogPairing(t *testing.T) {
	m, sessionID := newCatalogMCP(t)
	out := callCatalogTool(t, m, context.Background(), map[string]any{"session_id": sessionID})
	protos := out["protocols"].([]any)
	byMsg := catalogByMsg(t, protos)

	// Login → LoginAck：causation 配对（l2.causation_id=l1）。
	login := byMsg["Login"]
	pairs := login["paired_responses"].([]any)
	if len(pairs) != 1 {
		t.Fatalf("Login paired_responses = %d, want 1", len(pairs))
	}
	pr := pairs[0].(map[string]any)
	if pr["msg_name"] != "server_to_client|LoginAck" {
		t.Fatalf("pair msg_name = %v", pr["msg_name"])
	}
	if got := num(pr["count"]); got != 1 {
		t.Fatalf("pair count = %v, want 1", got)
	}
	if got := num(pr["paired_count"]); got != 1 {
		t.Fatalf("pair paired_count = %v, want 1", got)
	}
	lat := pr["latency_ms"].(map[string]any)
	if got := num(lat["count"]); got != 1 {
		t.Fatalf("pair latency count = %v, want 1", got)
	}
	if got := num(lat["min"]); got != 86 {
		t.Fatalf("pair latency min = %v, want 86", got)
	}

	// 没有 pair 事实的协议：paired_responses 为空数组，不自行猜测。
	for _, name := range []string{"Attack", "PushNearby"} {
		if pairs := byMsg[name]["paired_responses"].([]any); len(pairs) != 0 {
			t.Fatalf("%s paired_responses = %v, want []", name, pairs)
		}
	}
}

// TestProtocolCatalogPagination 场景：分页单位是协议，统计基于全量范围。
func TestProtocolCatalogPagination(t *testing.T) {
	m, sessionID := newCatalogMCP(t)
	ctx := context.Background()

	page1 := callCatalogTool(t, m, ctx, map[string]any{"session_id": sessionID, "limit": 2, "offset": 0})
	if got := num(page1["count"]); got != 2 {
		t.Fatalf("page1 count = %v, want 2", got)
	}
	if got := num(page1["total_protocols"]); got != 5 {
		t.Fatalf("page1 total_protocols = %v, want 5", got)
	}
	if page1["has_more"] != true {
		t.Fatalf("page1 has_more = %v, want true", page1["has_more"])
	}
	names1 := []string{}
	for _, p := range page1["protocols"].([]any) {
		names1 = append(names1, p.(map[string]any)["msg_name"].(string))
	}
	if len(names1) != 2 || names1[0] != "HeartBeat" || names1[1] != "Login" {
		t.Fatalf("page1 names = %v", names1)
	}

	// 第二页。
	page2 := callCatalogTool(t, m, ctx, map[string]any{"session_id": sessionID, "limit": 2, "offset": 2})
	names2 := []string{}
	for _, p := range page2["protocols"].([]any) {
		names2 = append(names2, p.(map[string]any)["msg_name"].(string))
	}
	if len(names2) != 2 || names2[0] != "LoginAck" || names2[1] != "Attack" {
		t.Fatalf("page2 names = %v", names2)
	}

	// 越界 offset：空协议数组，total 不变（分页统计仍基于全量）。
	page3 := callCatalogTool(t, m, ctx, map[string]any{"session_id": sessionID, "limit": 2, "offset": 99})
	if got := num(page3["count"]); got != 0 {
		t.Fatalf("page3 count = %v, want 0", got)
	}
	if got := num(page3["total_protocols"]); got != 5 {
		t.Fatalf("page3 total_protocols = %v, want 5", got)
	}
	if page3["has_more"] != false {
		t.Fatalf("page3 has_more = %v, want false", page3["has_more"])
	}
}

// TestProtocolCatalogTimeRange 场景：start_time/end_time 限制统计范围。
func TestProtocolCatalogTimeRange(t *testing.T) {
	m, sessionID := newCatalogMCP(t)
	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	out := callCatalogTool(t, m, context.Background(), map[string]any{
		"session_id": sessionID,
		"start_time": base.Add(1500 * time.Millisecond).Format(time.RFC3339Nano),
		"end_time":   base.Add(3500 * time.Millisecond).Format(time.RFC3339Nano),
	})
	if got := num(out["total_protocols"]); got != 3 {
		t.Fatalf("time range total_protocols = %v, want 3", got)
	}
	sum := out["summary"].(map[string]any)
	if got := num(sum["event_count"]); got != 4 {
		t.Fatalf("time range event_count = %v, want 4", got)
	}
	if got := num(sum["unnamed_events"]); got != 0 {
		t.Fatalf("time range unnamed_events = %v, want 0", got)
	}
	// 窗口内 Attack 仍是完整 2 个 event（a1 在窗口内）。
	attack := catalogByMsg(t, out["protocols"].([]any))["Attack"]
	if got := num(attack["count"]); got != 2 {
		t.Fatalf("time range Attack count = %v, want 2", got)
	}
}

// TestProtocolCatalogEmptyAndErrors 场景 7 + 参数校验（section 24）。
func TestProtocolCatalogEmptyAndErrors(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	sessionMgr := newSessionManager(workDir)
	m := &mcpCapture{
		workDir:      workDir,
		sessionMgr:   sessionMgr,
		authz:        newProjectAuthorizer(nil),
		readerOpener: sqliteReaderOpener(),
	}
	// 空会话：合法空 catalog（无 decoded event 是正常结果）。
	sessionID := sessionMgr.generateSessionID()
	if err := os.MkdirAll(sessionMgr.sessionDir(sessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := sessionMgr.absDBPath(sessionID)
	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := sessionMgr.writeSessionMetadata(sessionID, sessionMetadata{
		SessionID: sessionID,
		Status:    "stopped",
		DBPath:    dbPath,
	}); err != nil {
		t.Fatal(err)
	}
	out := callCatalogTool(t, m, ctx, map[string]any{"session_id": sessionID})
	if got := num(out["total_protocols"]); got != 0 {
		t.Fatalf("empty total_protocols = %v, want 0", got)
	}
	if got := num(out["count"]); got != 0 {
		t.Fatalf("empty count = %v, want 0", got)
	}
	obs := out["observed_time_range"].(map[string]any)
	if obs["start"] != nil || obs["end"] != nil {
		t.Fatalf("empty observed_time_range = %v, want null/start/null", obs)
	}
	if protos := out["protocols"].([]any); len(protos) != 0 {
		t.Fatalf("empty protocols = %v, want []", protos)
	}

	// 参数非法：limit 越界 / offset 负数 / direction 非法 / 时间解析失败 / start>end。
	badCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"limit zero", map[string]any{"session_id": sessionID, "limit": 0}, "limit out of range"},
		{"limit too big", map[string]any{"session_id": sessionID, "limit": 501}, "limit out of range"},
		{"offset negative", map[string]any{"session_id": sessionID, "offset": -1}, "offset < 0"},
		{"direction invalid", map[string]any{"session_id": sessionID, "direction": "bogus"}, "direction invalid"},
		{"start invalid", map[string]any{"session_id": sessionID, "start_time": "bogus"}, "invalid start_time"},
		{"end invalid", map[string]any{"session_id": sessionID, "end_time": "not-a-time"}, "invalid end_time"},
		{"range inverted", map[string]any{
			"session_id": sessionID,
			"start_time": "2026-09-19T11:00:00Z",
			"end_time":   "2026-09-19T10:00:00Z",
		}, "start_time > end_time"},
	}
	for _, bc := range badCases {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = bc.args
		res, err := m.handleGetProtocolCatalog(ctx, req)
		if err != nil {
			t.Fatalf("%s: handler error: %v", bc.name, err)
		}
		if got := errorText(res); got == "" || !containsSub(got, bc.want) {
			t.Fatalf("%s: error = %q, want substring %q", bc.name, got, bc.want)
		}
	}

	// session 不存在。
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"session_id": "sess-does-not-exist"}
	res, err := m.handleGetProtocolCatalog(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := errorText(res); got == "" || !containsSub(got, "not found") {
		t.Fatalf("unknown session error = %s", contentText(res))
	}
}

// errorText 提取 errorResult 的 error 文本。
func errorText(res *mcp.CallToolResult) string {
	var out map[string]any
	if err := json.Unmarshal([]byte(contentText(res)), &out); err != nil {
		return ""
	}
	e, _ := out["error"].(string)
	return e
}

func containsSub(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}