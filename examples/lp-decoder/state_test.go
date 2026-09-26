package main

import (
	"fmt"
	"testing"

	sdkevent "github.com/OwnSecurityGuard/gametrace/sdk/event"
)

// entityLines 取出一条事件 Analysis 通道里属于实体状态（lp_player / lp_item）的变更，
// 压成 "subject:id.path before→after" 便于逐条比对；计数器变更（lp_request/lp_response）忽略。
func entityLines(t *testing.T, analysisMsgpack []byte) []string {
	t.Helper()
	v, err := sdkevent.UnmarshalValueMsgpack(analysisMsgpack)
	if err != nil {
		t.Fatalf("unmarshal analysis: %v", err)
	}
	root, ok := v.ToAny().(map[string]any)
	if !ok {
		t.Fatalf("analysis is not an object: %T", v.ToAny())
	}
	list, _ := root["_state_changes"].([]any)
	var lines []string
	for _, entry := range list {
		c, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		subject, _ := c["subject_type"].(string)
		if subject != subjectPlayer && subject != subjectItem {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s:%s.%s %v→%v",
			subject, c["subject_id"], c["path"], c["before"], c["after"]))
	}
	return lines
}

// emitBody 把一条 JSON 信封作为服务端→客户端帧喂进解码器，返回该事件的实体状态变更行。
func emitBody(t *testing.T, d *decoder, stream *captureStream, flowID, body string) []string {
	t.Helper()
	f, _, ok := parseFrame(rawLP([]byte(body), 1, false))
	if !ok {
		t.Fatalf("parse frame for %s", body)
	}
	before := len(stream.responses)
	if err := d.emit(stream, fmt.Sprintf("in-%d", before), flowID, f); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(stream.responses) != before+1 {
		t.Fatalf("emit %s: responses %d→%d, want one event", body, before, len(stream.responses))
	}
	return entityLines(t, stream.responses[before].AnalysisMsgpack)
}

// TestEntityStateProjection 钉住"推送更新数量"到状态视图的投影链路：
// 基线、before/after 链、重复推送去重、被拒绝的请求不改状态、
// 客户端方向的消息一律不投影、每条连接各自建立基线。
func TestEntityStateProjection(t *testing.T) {
	d := newDecoder()
	stream := &captureStream{}
	const flow = "tcp 127.0.0.1:40001=127.0.0.1:8998"
	const otherFlow = "tcp 127.0.0.1:40002=127.0.0.1:8998"

	cases := []struct {
		name     string
		flowID   string
		body     string
		wantLine string
		wantNone bool
	}{
		{
			name: "登录响应给出玩家号，等级首次观测没有 before", flowID: flow,
			body:     `{"cmd":1002,"seq":1,"data":{"player_id":"P-1","nickname":"n","level":1}}`,
			wantLine: "lp_player:P-1.level <nil>→1",
		},
		{
			name: "玩家档案推送补上经验与在线状态，等级同值不重复计", flowID: flow,
			body:     `{"cmd":2001,"seq":0,"data":{"player_id":"P-1","level":1,"exp":0,"online":true}}`,
			wantLine: "lp_player:P-1.exp <nil>→0",
		},
		{
			name: "背包快照为每件道具建立基线", flowID: flow,
			body:     `{"cmd":1004,"seq":2,"data":{"items":[{"item_id":5001,"count":12},{"item_id":5002,"count":6}]}}`,
			wantLine: "lp_item:5001.count <nil>→12",
		},
		{
			name: "道具数量推送带出前后值", flowID: flow,
			body:     `{"cmd":2002,"seq":0,"data":{"item_id":5001,"count":11,"delta":-1}}`,
			wantLine: "lp_item:5001.count 12→11",
		},
		{
			name: "资源推送按玩家号归属", flowID: flow,
			body:     `{"cmd":2003,"seq":0,"data":{"gold":190,"diamond":20}}`,
			wantLine: "lp_player:P-1.gold <nil>→190",
		},
		{
			name: "重复推送同一数值不产生变更", flowID: flow,
			body:     `{"cmd":2002,"seq":0,"data":{"item_id":5001,"count":11,"delta":0}}`,
			wantNone: true,
		},
		{
			name: "客户端请求里的 count 是使用数量，不是道具状态", flowID: flow,
			body:     `{"cmd":1005,"seq":3,"data":{"item_id":5001,"count":99}}`,
			wantNone: true,
		},
		{
			name: "被拒绝的道具使用不改变数量", flowID: flow,
			body:     `{"cmd":1006,"seq":4,"error_code":7,"error_msg":"道具 5001 只剩 11 个","data":{"item_id":5001,"remain":11}}`,
			wantNone: true,
		},
		{
			name: "新连接（新流）重新建立基线", flowID: otherFlow,
			body:     `{"cmd":2002,"seq":0,"data":{"item_id":5001,"count":11,"delta":-1}}`,
			wantLine: "lp_item:5001.count <nil>→11",
		},
	}

	for _, tc := range cases {
		lines := emitBody(t, d, stream, tc.flowID, tc.body)
		if tc.wantNone {
			if len(lines) != 0 {
				t.Errorf("%s: entity changes = %v, want none", tc.name, lines)
			}
			continue
		}
		if len(lines) == 0 || lines[0] != tc.wantLine {
			t.Errorf("%s: entity changes = %v, want first %q", tc.name, lines, tc.wantLine)
		}
	}
}

// TestPlayerInfoPushDedup 单独核对等级同值去重：玩家档案推送应只产出 exp / online 两条。
func TestPlayerInfoPushDedup(t *testing.T) {
	d := newDecoder()
	stream := &captureStream{}
	flow := "tcp 127.0.0.1:40003=127.0.0.1:8998"

	emitBody(t, d, stream, flow, `{"cmd":1002,"seq":1,"data":{"player_id":"P-9","level":1}}`)
	lines := emitBody(t, d, stream, flow, `{"cmd":2001,"seq":0,"data":{"player_id":"P-9","level":1,"exp":0,"online":true}}`)

	want := []string{"lp_player:P-9.exp <nil>→0", "lp_player:P-9.online <nil>→true"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

// TestScenarioStateChain 按 examples/lp 一轮场景的实际顺序回放服务端→客户端消息，
// 核对投影出的最终实体状态：道具 5001 12→8、5002 6→3、5003 3→0，
// 金币 200→30、钻石 20→25，玩家等级 1→4、经验 0→365、在线 true。
// 场景里的错误回包与"再次查询背包"都不该改状态，因此状态条目总数必须正好是这 8 项。
func TestScenarioStateChain(t *testing.T) {
	d := newDecoder()
	stream := &captureStream{}
	const flow = "tcp 127.0.0.1:40007=127.0.0.1:8998"

	seq := 0
	next := func() int { seq++; return seq }
	// 一次"请求 → 响应 + 道具数量推送 + 资源推送"；升级时服务端多推一条玩家档案。
	type useStep struct {
		item, count, remain, gold, diamond int
		exp, level                         int
		levelUp                            bool
	}
	use := func(s useStep) {
		n := next()
		request := fmt.Sprintf(`{"cmd":1005,"seq":%d,"data":{"item_id":%d,"count":%d}}`, n, s.item, s.count)
		if lines := emitBody(t, d, stream, flow, request); len(lines) != 0 {
			t.Fatalf("客户端请求不应投影出状态: %v", lines)
		}
		emitBody(t, d, stream, flow, fmt.Sprintf(
			`{"cmd":1006,"seq":%d,"data":{"item_id":%d,"used":%d,"remain":%d}}`, n, s.item, s.count, s.remain))
		emitBody(t, d, stream, flow, fmt.Sprintf(
			`{"cmd":2002,"seq":0,"data":{"item_id":%d,"count":%d,"delta":%d}}`, s.item, s.remain, -s.count))
		emitBody(t, d, stream, flow, fmt.Sprintf(`{"cmd":2003,"seq":0,"data":{"gold":%d,"diamond":%d}}`, s.gold, s.diamond))
		if s.levelUp {
			emitBody(t, d, stream, flow, fmt.Sprintf(
				`{"cmd":2001,"seq":0,"data":{"player_id":"P-1","nickname":"player_acct-1","level":%d,"exp":%d,"online":true}}`,
				s.level, s.exp))
		}
	}
	rejected := func(code int, msg string, data string) {
		n := next()
		body := fmt.Sprintf(`{"cmd":1006,"seq":%d,"error_code":%d,"error_msg":%q`, n, code, msg)
		if data != "" {
			body += fmt.Sprintf(`,"data":%s`, data)
		}
		if lines := emitBody(t, d, stream, flow, body+"}"); len(lines) != 0 {
			t.Fatalf("error_code=%d 的回包不应改状态: %v", code, lines)
		}
	}

	n := next()
	emitBody(t, d, stream, flow, fmt.Sprintf(`{"cmd":1001,"seq":%d,"data":{"account":"acct-1"}}`, n))
	emitBody(t, d, stream, flow, fmt.Sprintf(
		`{"cmd":1002,"seq":%d,"data":{"player_id":"P-1","nickname":"player_acct-1","level":1}}`, n))
	emitBody(t, d, stream, flow, `{"cmd":2001,"seq":0,"data":{"player_id":"P-1","nickname":"player_acct-1","level":1,"exp":0,"online":true}}`)

	n = next()
	emitBody(t, d, stream, flow, fmt.Sprintf(`{"cmd":1003,"seq":%d}`, n))
	emitBody(t, d, stream, flow, fmt.Sprintf(`{"cmd":1004,"seq":%d,"data":{"items":[`+
		`{"item_id":5001,"count":12},{"item_id":5002,"count":6},{"item_id":5003,"count":3}]}}`, n))

	for _, s := range []useStep{
		{item: 5001, count: 1, remain: 11, gold: 190, diamond: 20},
		{item: 5001, count: 1, remain: 10, gold: 180, diamond: 20},
		{item: 5001, count: 2, remain: 8, gold: 160, diamond: 20},
		{item: 5002, count: 1, remain: 5, gold: 140, diamond: 20, exp: 115, level: 2, levelUp: true},
		{item: 5002, count: 2, remain: 3, gold: 100, diamond: 20},
		{item: 5003, count: 1, remain: 2, gold: 60, diamond: 20, exp: 245, level: 3, levelUp: true},
	} {
		use(s)
	}

	// 四类非法使用道具请求：金币不足 / 持有不足 / 道具不存在 / 参数非法。
	rejected(8, `金币不足：需要 80，当前 60`, `{"item_id":5003,"used":2}`)
	rejected(7, `道具 5003 只剩 2 个，不足 5 个`, `{"item_id":5003,"used":5}`)
	rejected(6, `背包中没有道具 9999`, ``)
	rejected(5, `item_id 与 count 必须为正数`, `{"item_id":5001,"used":0}`)

	n = next()
	emitBody(t, d, stream, flow, fmt.Sprintf(`{"cmd":1007,"seq":%d,"data":{"resource":"gold","amount":50}}`, n))
	emitBody(t, d, stream, flow, `{"cmd":2003,"seq":0,"data":{"gold":110,"diamond":20}}`)
	n = next()
	emitBody(t, d, stream, flow, fmt.Sprintf(`{"cmd":1007,"seq":%d,"data":{"resource":"diamond","amount":5}}`, n))
	emitBody(t, d, stream, flow, `{"cmd":2003,"seq":0,"data":{"gold":110,"diamond":25}}`)

	use(useStep{item: 5003, count: 2, remain: 0, gold: 30, diamond: 25, exp: 365, level: 4, levelUp: true})

	// 再次查询背包：同一状态再快照一次，不应产出任何变更。
	n = next()
	if lines := emitBody(t, d, stream, flow, fmt.Sprintf(
		`{"cmd":1004,"seq":%d,"data":{"items":[{"item_id":5001,"count":8},{"item_id":5002,"count":3},{"item_id":5003,"count":0}]}}`, n)); len(lines) != 0 {
		t.Errorf("背包快照与已观测状态一致，不应产出变更: %v", lines)
	}

	got := make(map[string]any)
	for k, v := range d.entities.last {
		if k.flow == flow {
			got[fmt.Sprintf("%s:%s.%s", k.subject, k.id, k.path)] = v
		}
	}
	want := map[string]any{
		"lp_player:P-1.level":   int64(4),
		"lp_player:P-1.exp":     int64(365),
		"lp_player:P-1.online":  true,
		"lp_player:P-1.gold":    int64(30),
		"lp_player:P-1.diamond": int64(25),
		"lp_item:5001.count":    int64(8),
		"lp_item:5002.count":    int64(3),
		"lp_item:5003.count":    int64(0),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("状态 %s = %v, want %v", k, got[k], v)
		}
		delete(got, k)
	}
	if len(got) != 0 {
		t.Errorf("多出的状态条目: %v", got)
	}
}
