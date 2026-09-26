package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"sort"
	"sync/atomic"

	"gametrace/examples/lp/internal/lp"
)

// 会话初始状态：每条新连接重置，便于反复跑同一套客户端场景。
// 数值刻意取小，让"资源不足"这类非法请求在演示中真的会触发。
var (
	startItems   = map[int]int{5001: 12, 5002: 6, 5003: 3}
	itemCost     = map[int]int{5001: 10, 5002: 20, 5003: 40}
	itemExp      = map[int]int{5001: 20, 5002: 35, 5003: 60}
	startGold    = 200
	startDiamond = 20

	// expNeedPerLevel 是升到下一级所需的累计经验（level × 该值），因此道具给的
	// 经验会让玩家连升几级，服务端每次升级各推一条玩家档案。
	expNeedPerLevel = 100

	// gatherLimit 单次采集上限；gold / diamond 之外的资源名一律拒绝。
	gatherLimit = 1000

	playerSeq atomic.Uint64
)

// session 是一条客户端连接上的游戏会话：登录态 + 背包 + 资源 + 等级经验。
// 服务端在回业务响应之外，还会就背包/资源/玩家档案变化追加推送。
type session struct {
	conn     net.Conn
	loggedIn bool
	playerID string
	nickname string
	level    int
	exp      int
	items    map[int]int
	gold     int
	diamond  int
}

func newSession(conn net.Conn) *session {
	items := make(map[int]int, len(startItems))
	for id, count := range startItems {
		items[id] = count
	}
	return &session{conn: conn, items: items, gold: startGold, diamond: startDiamond}
}

// serve 逐帧读取长度前缀帧并回包。帧头里的长度字段宽度/端序由发送端逐帧随机决定，
// 这里必须按帧头读取而不是假设固定格式。
func (s *session) serve() {
	defer func() { _ = s.conn.Close() }()
	slog.Info("conn open", "remote", s.conn.RemoteAddr())

	for i := 1; ; i++ {
		f, err := lp.ReadFrame(s.conn)
		if err != nil {
			slog.Info("conn closed", "remote", s.conn.RemoteAddr(), "frames", i-1, "error", err)
			return
		}
		for _, env := range s.dispatch(f.Payload) {
			if err := s.write(env); err != nil {
				return
			}
		}
	}
}

// dispatch 把一帧 payload 分派给业务处理，返回"回包 + 推送"序列（先回包后推送）。
// 所有校验失败都以 error_code != 0 的回包告知客户端，而不是静默丢帧。
func (s *session) dispatch(payload []byte) []lp.Envelope {
	var req lp.Envelope
	if err := json.Unmarshal(payload, &req); err != nil {
		return []lp.Envelope{errReply(lp.CmdBadRequest, 0, lp.ErrBadEnvelope, "payload 不是合法 JSON 信封")}
	}
	slog.Info("receive", "cmd", req.Cmd, "name", lp.CmdName(req.Cmd), "seq", req.Seq,
		"error_code", req.ErrorCode)

	if !lp.IsRequestCmd(req.Cmd) {
		return []lp.Envelope{errReply(lp.CmdBadRequest, req.Seq, lp.ErrUnknownCmd,
			fmt.Sprintf("cmd=%d 不是已定义的请求消息", req.Cmd))}
	}
	if req.Cmd == lp.CmdLoginRequest {
		return s.handleLogin(req)
	}
	if !s.loggedIn {
		return []lp.Envelope{errReply(lp.ResponseCmd(req.Cmd), req.Seq, lp.ErrNotLoggedIn, "尚未登录")}
	}

	switch req.Cmd {
	case lp.CmdGetBagRequest:
		return s.handleGetBag(req)
	case lp.CmdUseItemRequest:
		return s.handleUseItem(req)
	case lp.CmdGatherRequest:
		return s.handleGather(req)
	default:
		// 号段内但服务端未实现（如 1999）：请求号合法、业务未知。
		return []lp.Envelope{errReply(lp.CmdBadRequest, req.Seq, lp.ErrUnknownCmd,
			fmt.Sprintf("cmd=%d 服务端未实现", req.Cmd))}
	}
}

func (s *session) handleLogin(req lp.Envelope) []lp.Envelope {
	if s.loggedIn {
		return []lp.Envelope{errReply(lp.CmdLoginResponse, req.Seq, lp.ErrAlreadyLoggedIn, "该连接已登录")}
	}
	var body lp.LoginRequestData
	if err := lp.UnmarshalData(req.Data, &body); err != nil || body.Account == "" {
		return []lp.Envelope{errReply(lp.CmdLoginResponse, req.Seq, lp.ErrInvalidParam, "account 不能为空")}
	}

	s.loggedIn = true
	s.playerID = fmt.Sprintf("P-%d", playerSeq.Add(1))
	s.nickname = "player_" + body.Account
	s.level = 1
	return []lp.Envelope{
		okReply(lp.CmdLoginResponse, req.Seq, lp.LoginResponseData{
			PlayerID: s.playerID, Nickname: s.nickname, Level: s.level,
		}),
		notify(lp.CmdPlayerInfoNotify, s.playerInfo()),
	}
}

func (s *session) handleGetBag(req lp.Envelope) []lp.Envelope {
	return []lp.Envelope{okReply(lp.CmdGetBagResponse, req.Seq, lp.GetBagResponseData{Items: s.bag()})}
}

// handleUseItem 演示"回包 + 状态推送"：响应告知本次使用结果，
// 随后各推一条道具数量与资源变化，若这次使用让玩家升级则再推一条玩家档案，
// 客户端不需要再查询。
func (s *session) handleUseItem(req lp.Envelope) []lp.Envelope {
	var body lp.UseItemRequestData
	if err := lp.UnmarshalData(req.Data, &body); err != nil {
		return []lp.Envelope{errReply(lp.CmdUseItemResponse, req.Seq, lp.ErrBadEnvelope, "使用道具请求体字段类型不符")}
	}
	if body.ItemID <= 0 || body.Count <= 0 {
		return []lp.Envelope{errReply(lp.CmdUseItemResponse, req.Seq, lp.ErrInvalidParam, "item_id 与 count 必须为正数")}
	}
	remain, owned := s.items[body.ItemID]
	if !owned {
		return []lp.Envelope{errReply(lp.CmdUseItemResponse, req.Seq, lp.ErrItemNotFound,
			fmt.Sprintf("背包中没有道具 %d", body.ItemID))}
	}
	if remain < body.Count {
		return []lp.Envelope{errReply(lp.CmdUseItemResponse, req.Seq, lp.ErrItemNotEnough,
			fmt.Sprintf("道具 %d 只剩 %d 个，不足 %d 个", body.ItemID, remain, body.Count))}
	}
	cost := itemCost[body.ItemID] * body.Count
	if s.gold < cost {
		return []lp.Envelope{errReply(lp.CmdUseItemResponse, req.Seq, lp.ErrResourceLow,
			fmt.Sprintf("金币不足：需要 %d，当前 %d", cost, s.gold))}
	}

	s.items[body.ItemID] = remain - body.Count
	s.gold -= cost
	replies := []lp.Envelope{
		okReply(lp.CmdUseItemResponse, req.Seq, lp.UseItemResponseData{
			ItemID: body.ItemID, Used: body.Count, Remain: s.items[body.ItemID],
		}),
		notify(lp.CmdItemCountNotify, lp.ItemCountNotifyData{
			ItemID: body.ItemID, Count: s.items[body.ItemID], Delta: -body.Count,
		}),
		notify(lp.CmdResourceNotify, s.resources()),
	}
	if s.gainExp(itemExp[body.ItemID] * body.Count) {
		replies = append(replies, notify(lp.CmdPlayerInfoNotify, s.playerInfo()))
	}
	return replies
}

// gainExp 累加经验并按累计门槛升级，返回是否发生升级。
func (s *session) gainExp(amount int) bool {
	before := s.level
	s.exp += amount
	for s.exp >= s.level*expNeedPerLevel {
		s.level++
	}
	return s.level != before
}

func (s *session) handleGather(req lp.Envelope) []lp.Envelope {
	var body lp.GatherRequestData
	if err := lp.UnmarshalData(req.Data, &body); err != nil {
		return []lp.Envelope{errReply(lp.CmdGatherResponse, req.Seq, lp.ErrBadEnvelope, "采集请求体字段类型不符")}
	}
	if body.Resource != "gold" && body.Resource != "diamond" {
		return []lp.Envelope{errReply(lp.CmdGatherResponse, req.Seq, lp.ErrInvalidParam,
			fmt.Sprintf("未知资源类型 %q", body.Resource))}
	}
	if body.Amount <= 0 || body.Amount > gatherLimit {
		return []lp.Envelope{errReply(lp.CmdGatherResponse, req.Seq, lp.ErrInvalidParam,
			fmt.Sprintf("采集数量必须在 1~%d 之间", gatherLimit))}
	}

	if body.Resource == "gold" {
		s.gold += body.Amount
	} else {
		s.diamond += body.Amount
	}
	return []lp.Envelope{
		okReply(lp.CmdGatherResponse, req.Seq, lp.GatherResponseData{
			Resource: body.Resource, Gained: body.Amount,
		}),
		notify(lp.CmdResourceNotify, s.resources()),
	}
}

// write 发送一帧，长度字段宽度与端序逐帧随机选择，
// 迫使解码器处理 1/2/4 字节、大/小端的所有组合（这是模板要演示的核心差异点）。
func (s *session) write(env lp.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	opts := lp.FrameOptions{
		Width:        []int{1, 2, 4}[rand.IntN(3)],
		LittleEndian: rand.IntN(2) == 0,
	}
	slog.Info("send", "cmd", env.Cmd, "name", lp.CmdName(env.Cmd), "seq", env.Seq,
		"error_code", env.ErrorCode, "error_msg", env.ErrorMsg,
		"width", opts.Width, "little_endian", opts.LittleEndian)
	_, err = s.conn.Write(lp.EncodeFrame(body, opts))
	return err
}

func (s *session) playerInfo() lp.PlayerInfoNotifyData {
	return lp.PlayerInfoNotifyData{
		PlayerID: s.playerID, Nickname: s.nickname, Level: s.level, Exp: s.exp, Online: s.loggedIn,
	}
}

func (s *session) resources() lp.ResourceNotifyData {
	return lp.ResourceNotifyData{Gold: s.gold, Diamond: s.diamond}
}

// bag 按道具号排序返回背包快照，保证同一状态的回包字节稳定。
func (s *session) bag() []lp.ItemEntry {
	ids := make([]int, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	entries := make([]lp.ItemEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, lp.ItemEntry{ItemID: id, Count: s.items[id]})
	}
	return entries
}

func okReply(cmd, seq int, data any) lp.Envelope {
	return lp.Envelope{Cmd: cmd, Seq: seq, Data: lp.MarshalData(data)}
}

func errReply(cmd, seq, code int, msg string) lp.Envelope {
	return lp.Envelope{Cmd: cmd, Seq: seq, ErrorCode: code, ErrorMsg: msg}
}

// notify 构造服务端推送：seq 恒为 0，因此不参与请求/响应配对。
func notify(cmd int, data any) lp.Envelope {
	return lp.Envelope{Cmd: cmd, Seq: 0, Data: lp.MarshalData(data)}
}

func main() {
	addr := flag.String("addr", ":8998", "listen address")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		slog.Error("listen failed", "error", err)
		os.Exit(1)
	}
	slog.Info("lp server listening", "addr", *addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			slog.Error("accept failed", "error", err)
			continue
		}
		go newSession(conn).serve()
	}
}
