package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"

	"gametrace/examples/lp/internal/lp"
)

// 消息类型常量：与 examples/lp-decoder/plugin.yaml 的语义规则保持一致。
// 响应覆盖 response role / push rule / error 语义，并通过回显 seq 完成 request/response 配对。
const (
	cmdLoginRequest  = 1001 // 客户端请求（request）
	cmdLoginResponse = 1002 // 正常响应（response，回显 seq 配对）
	cmdPlayerNotify  = 2001 // 服务端推送（push，seq==0，命中 push rule）
)

// envelope 是 lp 帧 payload 携带的 JSON 信封，解码器按 cmd / seq / error_code 提取语义。
type envelope struct {
	Cmd       int `json:"cmd"`                  // message id
	Seq       int `json:"seq"`                  // seq correlation（回显请求的 seq 以配对）
	ErrorCode int `json:"error_code,omitempty"` // error 语义：0 成功，非 0 失败
}

// randomFrameOptions 每次回复随机选择长度字段宽度与端序，
// 迫使解码器处理 1/2/4 字节、大/小端的所有组合（这是模板要演示的核心差异点）。
func randomFrameOptions() lp.FrameOptions {
	return lp.FrameOptions{
		Width:        []int{1, 2, 4}[rand.IntN(3)],
		LittleEndian: rand.IntN(2) == 0,
	}
}

// replyEnvelope 生成对一条客户端请求的回复，覆盖各类语义格式：
//   - 1002 + 回显 seq：response role + seq correlation（与请求配对）
//   - 2001 + seq==0：服务端推送（命中 push rule）
//   - 1002 + error_code=1：error 语义
func replyEnvelope(req envelope) []envelope {
	reply := envelope{Cmd: cmdLoginResponse, Seq: req.Seq}
	switch rand.IntN(4) {
	case 0, 1: // 正常响应：回显 seq 完成配对
	case 2: // 推送 + 响应
		return []envelope{
			{Cmd: cmdPlayerNotify, Seq: 0},
			reply,
		}
	default: // 错误响应
		reply.ErrorCode = 1
	}
	return []envelope{reply}
}

// handle 处理一条已建立的 lp 连接：往复读取长度前缀帧，按信封语义回包。
// 长度字段宽度/端序逐帧随机选择，模拟真实游戏协议常见的"变长长度字段"。
func handle(conn net.Conn) {
	defer conn.Close()
	slog.Info("conn open", "remote", conn.RemoteAddr())

	for i := 1; ; i++ {
		f, err := lp.ReadFrame(conn)
		if err != nil {
			return // 客户端关闭或对端异常
		}
		var req envelope
		if json.Unmarshal(f.Payload, &req) != nil {
			slog.Warn("invalid envelope", "n", i, "payload", string(f.Payload))
			continue
		}
		slog.Info("receive", "n", i, "cmd", req.Cmd, "seq", req.Seq,
			"width", f.Width, "little_endian", f.LittleEndian)

		for _, ev := range replyEnvelope(req) {
			body, _ := json.Marshal(ev)
			opts := randomFrameOptions()
			if _, err := conn.Write(lp.EncodeFrame(body, opts)); err != nil {
				return
			}
			slog.Info("send", "n", i, "cmd", ev.Cmd, "seq", ev.Seq,
				"width", opts.Width, "little_endian", opts.LittleEndian)
		}
	}
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
		go handle(conn)
	}
}
