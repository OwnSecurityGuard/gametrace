package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"

	"gametrace/examples/lp/internal/lp"
)

// replyTimeout 单条请求等待响应的上限；pushTimeout 一轮结束后收束推送的读窗口。
const (
	replyTimeout = 3 * time.Second
	pushTimeout  = 500 * time.Millisecond
)

// garbagePayload 是故意发出的非 JSON 载荷：服务端无法解析信封，只能回 error_code 提示。
const garbagePayload = "this is not a lp envelope"

// client 是一条 lp 连接上的模拟客户端：主线程按场景脚本发请求，
// 读协程负责解帧——响应按 seq 交给等待者，推送只打印，以此演示"回包 + 主动推送"。
type client struct {
	conn    net.Conn
	closed  chan struct{}
	pending map[int]chan lp.Envelope

	mu  sync.Mutex
	seq int
}

func newClient(conn net.Conn) *client {
	c := &client{conn: conn, closed: make(chan struct{}), pending: make(map[int]chan lp.Envelope)}
	go c.readLoop()
	return c
}

// nextSeq 递增并返回请求序号；seq 同时是请求/响应的配对键。
func (c *client) nextSeq() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	return c.seq
}

// step 发送一条请求并打印服务端的处理结果（正常回包或错误提示）。
func (c *client) step(name string, cmd int, data any) error {
	return c.stepEnvelope(name, lp.Envelope{Cmd: cmd, Seq: c.nextSeq(), Data: lp.MarshalData(data)})
}

// stepEnvelope 允许构造"残缺信封"（如 cmd 缺失/未知），用于非法请求演示。
func (c *client) stepEnvelope(name string, env lp.Envelope) error {
	ch := make(chan lp.Envelope, 1)
	c.mu.Lock()
	c.pending[env.Seq] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, env.Seq)
		c.mu.Unlock()
	}()

	if err := c.send(env); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	select {
	case resp := <-ch:
		logStep(name, resp)
		return nil
	case <-time.After(replyTimeout):
		return fmt.Errorf("%s: 等待 cmd=%d seq=%d 的响应超时", name, env.Cmd, env.Seq)
	case <-c.closed:
		return fmt.Errorf("%s: 连接已关闭", name)
	}
}

// garbage 发出一帧非 JSON 载荷。服务端回的错误包无法回显 seq（信封都没解出来），
// 因此不等待配对，交给读协程打印、由 drain 窗口收尾。
func (c *client) garbage() error {
	return c.sendRaw(0, 0, []byte(garbagePayload))
}

// drain 用一次读超时收住本轮：让最后的推送落地后再断开连接。
func (c *client) drain() {
	if err := c.conn.SetReadDeadline(time.Now().Add(pushTimeout)); err != nil {
		slog.Warn("set read deadline failed", "error", err)
		return
	}
	<-c.closed
}

func (c *client) send(env lp.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return c.sendRaw(env.Cmd, env.Seq, body)
}

// sendRaw 逐帧随机选择长度字段宽度与端序，验证解码器对 1/2/4 字节、大/小端的
// 任意组合都能解析。
func (c *client) sendRaw(cmd, seq int, body []byte) error {
	opts := lp.FrameOptions{
		Width:        []int{1, 2, 4}[rand.IntN(3)],
		LittleEndian: rand.IntN(2) == 0,
	}
	slog.Info("send", "cmd", cmd, "name", lp.CmdName(cmd), "seq", seq,
		"data", string(body), "width", opts.Width, "little_endian", opts.LittleEndian)
	_, err := c.conn.Write(lp.EncodeFrame(body, opts))
	return err
}

// readLoop 解出每一帧并打印；seq 命中等待者的作为响应交付，其余（推送）只记录。
func (c *client) readLoop() {
	defer close(c.closed)
	for {
		f, err := lp.ReadFrame(c.conn)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.Debug("read loop stopped", "error", err)
			}
			return
		}
		var env lp.Envelope
		if json.Unmarshal(f.Payload, &env) != nil {
			slog.Warn("receive", "body", string(f.Payload), "error", "信封无法解析")
			continue
		}
		kind := "reply"
		if lp.IsPushCmd(env.Cmd) {
			kind = "push"
		}
		slog.Info("receive", "kind", kind, "cmd", env.Cmd, "name", lp.CmdName(env.Cmd),
			"seq", env.Seq, "error_code", env.ErrorCode, "error_msg", env.ErrorMsg,
			"data", string(env.Data), "width", f.Width, "little_endian", f.LittleEndian)

		c.mu.Lock()
		ch := c.pending[env.Seq]
		c.mu.Unlock()
		if ch != nil {
			ch <- env
		}
	}
}

// logStep 把一步请求的服务端结论压成一行：接受（含消息体）或拒绝（含错误提示）。
func logStep(name string, env lp.Envelope) {
	if env.ErrorCode != 0 {
		slog.Info("step rejected", "step", name, "cmd", env.Cmd, "name", lp.CmdName(env.Cmd),
			"error_code", env.ErrorCode, "error_msg", env.ErrorMsg)
		return
	}
	slog.Info("step accepted", "step", name, "cmd", env.Cmd, "name", lp.CmdName(env.Cmd),
		"data", string(env.Data))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8998", "server address")
	rounds := flag.Int("rounds", 0, "scenario rounds to run (0 = forever)")
	interval := flag.Duration("interval", 5*time.Second, "pause between rounds")
	pace := flag.Duration("pace", 500*time.Millisecond, "pause between scenario steps")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	for round := 1; *rounds == 0 || round <= *rounds; round++ {
		// 每轮换一条连接：服务端会话状态随之重置，同一套场景可反复复现。
		if err := runRound(*addr, round, *pace); err != nil {
			slog.Error("round failed", "round", round, "error", err)
		}
		if *rounds > 0 && round >= *rounds {
			return
		}
		time.Sleep(*interval)
	}
}

// runRound 跑一轮完整场景：登录 → 查询 → 连续使用道具（伴随数量推送）→ 采集资源，
// 中间穿插各类非法请求，覆盖"服务端回错误提示"与"回包之外还有推送"两种形态。
func runRound(addr string, round int, pace time.Duration) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	c := newClient(conn)
	slog.Info("round start", "round", round, "addr", addr)

	account := fmt.Sprintf("acct-%d", round)
	steps := []func() error{
		// 非法请求：未登录就访问业务消息。
		func() error { return c.step("未登录查背包", lp.CmdGetBagRequest, nil) },

		// 登录：回包之外服务端还会推一条玩家档案。
		func() error {
			return c.step("登录", lp.CmdLoginRequest,
				lp.LoginRequestData{Account: account, DeviceID: "lp-sim"})
		},
		// 非法请求：同一连接重复登录。
		func() error {
			return c.step("重复登录", lp.CmdLoginRequest, lp.LoginRequestData{Account: account})
		},
		func() error { return c.step("查询背包", lp.CmdGetBagRequest, nil) },

		// 连续使用道具：每次都是"响应 + 道具数量推送 + 资源推送"。
		// 金币起始 120、道具单价 30/60/120，因此这三次正好把金币花光。
		func() error {
			return c.step("使用道具 5001", lp.CmdUseItemRequest, lp.UseItemRequestData{ItemID: 5001, Count: 1})
		},
		func() error {
			return c.step("使用道具 5001", lp.CmdUseItemRequest, lp.UseItemRequestData{ItemID: 5001, Count: 1})
		},
		func() error {
			return c.step("使用道具 5002", lp.CmdUseItemRequest, lp.UseItemRequestData{ItemID: 5002, Count: 1})
		},
		// 非法请求：金币已耗尽 → 资源不足。
		func() error {
			return c.step("金币不足时使用道具 5003", lp.CmdUseItemRequest, lp.UseItemRequestData{ItemID: 5003, Count: 1})
		},
		// 非法请求：持有数量不足、道具不存在、参数非正数。
		func() error {
			return c.step("超量使用道具 5001", lp.CmdUseItemRequest, lp.UseItemRequestData{ItemID: 5001, Count: 5})
		},
		func() error {
			return c.step("使用未持有道具", lp.CmdUseItemRequest, lp.UseItemRequestData{ItemID: 9999, Count: 1})
		},
		func() error {
			return c.step("使用数量为 0", lp.CmdUseItemRequest, lp.UseItemRequestData{ItemID: 5001, Count: 0})
		},

		// 采集资源：回包 + 资源数量推送，随后金币够再吃一次道具。
		func() error {
			return c.step("采集金币", lp.CmdGatherRequest, lp.GatherRequestData{Resource: "gold", Amount: 200})
		},
		// 非法请求：采集超上限、资源名不存在。
		func() error {
			return c.step("采集超上限", lp.CmdGatherRequest, lp.GatherRequestData{Resource: "gold", Amount: 5000})
		},
		func() error {
			return c.step("采集未知资源", lp.CmdGatherRequest, lp.GatherRequestData{Resource: "wood", Amount: 10})
		},
		func() error {
			return c.step("再使用道具 5001", lp.CmdUseItemRequest, lp.UseItemRequestData{ItemID: 5001, Count: 1})
		},

		// 非法请求：合法号段内但未实现的 cmd、信封缺少 cmd、payload 根本不是 JSON。
		func() error { return c.step("未实现的请求号", 1999, nil) },
		func() error {
			return c.stepEnvelope("信封缺少 cmd", lp.Envelope{Seq: c.nextSeq()})
		},
		c.garbage,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
		time.Sleep(pace)
	}

	c.drain()
	slog.Info("round done", "round", round)
	return nil
}
