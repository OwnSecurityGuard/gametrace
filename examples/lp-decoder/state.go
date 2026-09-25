package main

import (
	"encoding/json"
	"strconv"
)

// 实体状态投影：从服务端消息体里抽出可变字段（道具数量、金币/钻石、等级/经验/在线），
// 投影成 _state_changes，让"推送更新数量"在平台的状态视图里能看到前后值。
//
// 三条约定：
//   - 只有服务端→客户端、且 error_code==0 的消息才改状态：被拒绝的请求不产生变更；
//   - 解码器按流记住最近解到的值来补 before；首次观测只有 after（基线未知，
//     通常是抓包起点晚于该实体第一次变化）；
//   - 与最近值相同则不产出变更：服务端重复推送、以及响应与紧随其后的推送
//     携带同一数值（如 UseItemResponse.remain 与 ItemCountNotify.count）时不重复计。

// 状态主体：玩家与背包道具各一类，subject_id 用协议里的玩家号 / 道具号。
const (
	subjectPlayer = "lp_player"
	subjectItem   = "lp_item"
)

// stateKey 定位一份可变状态。带 flow 是因为模板协议的会话状态是每条连接一份：
// 客户端每轮换连接，新流要从第一条推送/回包重新建立基线。
type stateKey struct {
	flow    string
	subject string
	id      string
	path    string
}

// entityTracker 跨 Decode 调用存活（挂在 decoder 上），保存每流的最近已知值。
type entityTracker struct {
	last    map[stateKey]any
	players map[string]string // flow → 玩家号，用于资源/道具状态的归属
}

func newEntityTracker() *entityTracker {
	return &entityTracker{
		last:    make(map[stateKey]any),
		players: make(map[string]string),
	}
}

// stateBody 是消息体里会被投影为状态的字段；用指针区分"没带这个字段"与"值为 0"。
type stateBody struct {
	PlayerID string `json:"player_id"`
	Level    *int64 `json:"level"`
	Exp      *int64 `json:"exp"`
	Online   *bool  `json:"online"`
	Gold     *int64 `json:"gold"`
	Diamond  *int64 `json:"diamond"`
	ItemID   *int64 `json:"item_id"`
	Count    *int64 `json:"count"`
	Items    []struct {
		ItemID *int64 `json:"item_id"`
		Count  *int64 `json:"count"`
	} `json:"items"`
}

// project 解析一条消息并按 cmd 投影状态，返回变更条目（可为空）。
// 业务字段在信封的 data 子对象里，所以取 sem.Data 而不是整帧 payload。
func (t *entityTracker) project(flowID string, sem envelopeSemantics, version int64) []any {
	if sem.direction() != "server_to_client" || sem.IsError || len(sem.Data) == 0 {
		return nil
	}
	var b stateBody
	if err := json.Unmarshal(sem.Data, &b); err != nil {
		return nil
	}
	if b.PlayerID != "" {
		t.players[flowID] = b.PlayerID
	}
	playerID := t.players[flowID]
	if playerID == "" {
		playerID = flowID // 玩家号还没解到：退回按流归属，状态本身仍然可比对
	}

	var changes []any
	add := func(subject, id, path string, value any) {
		if c := t.observe(stateKey{flow: flowID, subject: subject, id: id, path: path}, value, version); c != nil {
			changes = append(changes, c)
		}
	}
	setNum := func(subject, id, path string, v *int64) {
		if v != nil {
			add(subject, id, path, *v)
		}
	}

	switch sem.Cmd {
	case cmdLoginResponse, cmdPlayerInfoNotify:
		setNum(subjectPlayer, playerID, "level", b.Level)
		setNum(subjectPlayer, playerID, "exp", b.Exp)
		if b.Online != nil {
			add(subjectPlayer, playerID, "online", *b.Online)
		}
	case cmdGetBagResponse:
		// 整包快照：给后续单条道具推送提供 before 基线。
		for _, item := range b.Items {
			if item.ItemID != nil && item.Count != nil {
				setNum(subjectItem, idOf(*item.ItemID), "count", item.Count)
			}
		}
	case cmdItemCountNotify:
		if b.ItemID != nil {
			setNum(subjectItem, idOf(*b.ItemID), "count", b.Count)
		}
	case cmdResourceNotify:
		setNum(subjectPlayer, playerID, "gold", b.Gold)
		setNum(subjectPlayer, playerID, "diamond", b.Diamond)
	}
	return changes
}

// observe 记录一份状态取值并返回变更条目；与最近值相同则返回 nil（重复推送不计）。
func (t *entityTracker) observe(k stateKey, after any, version int64) map[string]any {
	prev, seen := t.last[k]
	if seen && prev == after {
		return nil
	}
	t.last[k] = after

	change := map[string]any{
		"subject_type": k.subject,
		"subject_id":   k.id,
		"op":           "set",
		"path":         k.path,
		"after":        after,
		"version":      version,
	}
	if seen {
		change["before"] = prev // 首次观测没有 before：基线在抓包起点之前
	}
	return change
}

// idOf 把数值实体号渲染成 subject_id（平台按字符串寻址状态主体）。
func idOf(id int64) string { return strconv.FormatInt(id, 10) }
