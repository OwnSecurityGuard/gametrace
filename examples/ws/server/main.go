package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"strings"

	"gametrace/examples/ws/internal/ws"
)

// 消息类型常量：与 examples/ws-decoder/plugin.yaml 的语义规则保持一致。
const (
	TypeEcho      = "echo"       // 客户端请求（role=request）
	TypeEchoReply = "echo_reply" // 服务端响应（role=response，回显 seq 配对）
	TypePush      = "push"       // 服务端推送（role=push，命中 push rule: type==push）
)

// message 是 WS 文本帧携带的 JSON 信封，解码器按 type / seq / error_code 提取语义。
type message struct {
	Type      string `json:"type"`
	Seq       int64  `json:"seq"`                  // seq correlation（请求/响应配对键）
	Text      string `json:"text,omitempty"`       // 业务内容
	ErrorCode int64  `json:"error_code,omitempty"` // error 语义：0 成功，非 0 失败
}

func main() {
	addr := flag.String("addr", ":8990", "listen address")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		slog.Error("listen failed", "error", err)
		os.Exit(1)
	}
	slog.Info("ws server listening", "addr", *addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			slog.Error("accept failed", "error", err)
			continue
		}
		go handle(conn)
	}
}

// handle 完成一次 WebSocket 握手（手写 HTTP Upgrade），随后进入帧循环：
// 文本帧按信封回显 seq 完成 request/response 配对，随机推送/报错/附带二进制帧。
func handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	line, headers, err := ws.ReadHandshake(br)
	if err != nil {
		slog.Warn("handshake read failed", "error", err)
		return
	}
	fields := strings.Fields(line)
	key := headers["sec-websocket-key"]
	if len(fields) < 2 || fields[0] != "GET" || !strings.EqualFold(headers["upgrade"], "websocket") || key == "" {
		slog.Warn("not a websocket handshake", "line", line)
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}

	if _, err := fmt.Fprintf(conn,
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n", ws.ComputeAcceptKey(key)); err != nil {
		return
	}
	slog.Info("handshake ok", "remote", conn.RemoteAddr(), "path", fields[1])

	for i := 1; ; i++ {
		opcode, payload, err := ws.ReadFrame(br)
		if err != nil {
			return // 客户端关闭或对端异常
		}
		switch opcode {
		case ws.OpPing:
			_ = write(conn, ws.EncodeFrame(ws.OpPong, payload, false))
		case ws.OpClose:
			_ = write(conn, ws.EncodeFrame(ws.OpClose, payload, false))
			return
		case ws.OpText:
			var req message
			if json.Unmarshal(payload, &req) != nil {
				continue
			}
			slog.Info("receive", "n", i, "type", req.Type, "seq", req.Seq, "text", req.Text)
			for _, frame := range replyFrames(req) {
				if err := write(conn, frame); err != nil {
					return
				}
			}
		case ws.OpBinary:
			slog.Info("receive binary", "n", i, "bytes", len(payload))
		}
	}
}

// replyFrames 生成对一条客户端消息的回复帧，覆盖各类语义格式：
//   - echo_reply + 回显 seq：response role + seq correlation（与请求配对）
//   - push：服务端主动推送（type==push，seq==0，命中 push rule）
//   - echo_reply + error_code=1：error 语义
//   - 偶尔附带一条二进制帧，演示 ws.binary 事件
func replyFrames(req message) [][]byte {
	var frames [][]byte
	reply := func(m message) {
		b, _ := json.Marshal(m)
		frames = append(frames, ws.EncodeFrame(ws.OpText, b, false))
	}

	switch rand.IntN(4) {
	case 0, 1: // 正常响应：回显 seq 完成配对
		reply(message{Type: TypeEchoReply, Seq: req.Seq, Text: req.Text})
	case 2: // 推送 + 响应
		reply(message{Type: TypePush, Seq: 0, Text: "hello from server"})
		reply(message{Type: TypeEchoReply, Seq: req.Seq, Text: req.Text})
	default: // 错误响应
		reply(message{Type: TypeEchoReply, Seq: req.Seq, Text: req.Text, ErrorCode: 1})
	}
	if rand.IntN(3) == 0 {
		frames = append(frames, ws.EncodeFrame(ws.OpBinary, []byte{0x01, 0x02, 0x03}, false))
	}
	return frames
}

func write(conn net.Conn, b []byte) error {
	_, err := conn.Write(b)
	return err
}
