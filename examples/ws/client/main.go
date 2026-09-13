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
	"time"

	"gametrace/examples/ws/internal/ws"
)

// message 是 WS 文本帧携带的 JSON 信封，与服务器保持一致。
type message struct {
	Type      string `json:"type"`
	Seq       int64  `json:"seq"`                  // seq correlation（请求/响应配对键）
	Text      string `json:"text,omitempty"`       // 业务内容
	ErrorCode int64  `json:"error_code,omitempty"` // error 语义：0 成功，非 0 失败
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8990", "server address")
	interval := flag.Duration("interval", 5*time.Second, "read window between sends")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		slog.Error("dial failed", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	// 手写 HTTP Upgrade 握手，并校验服务端 Sec-WebSocket-Accept。
	key := ws.NewClientKey()
	if err := ws.WriteHandshakeRequest(conn, *addr, "/ws", key); err != nil {
		slog.Error("write handshake failed", "error", err)
		return
	}
	br := bufio.NewReader(conn)
	line, headers, err := ws.ReadHandshake(br)
	if err != nil {
		slog.Error("read handshake failed", "error", err)
		return
	}
	if !strings.HasPrefix(line, "HTTP/1.1 101") {
		slog.Error("unexpected handshake response", "line", line)
		return
	}
	if want := ws.ComputeAcceptKey(key); headers["sec-websocket-accept"] != want {
		slog.Error("bad Sec-WebSocket-Accept", "got", headers["sec-websocket-accept"], "want", want)
		return
	}
	slog.Info("handshake ok")

	// 连接建立后先发一个 ping，演示 ws.ping / ws.pong 控制帧。
	if err := write(conn, ws.EncodeFrame(ws.OpPing, []byte("ping"), true)); err != nil {
		return
	}

	for i := 1; ; i++ {
		msg := message{Type: "echo", Seq: randSeq(), Text: fmt.Sprintf("hello %d", i)}
		if rand.IntN(4) == 0 {
			msg.ErrorCode = 1 // 错误请求：服务端回显并报错
		}
		payload, _ := json.Marshal(msg)
		slog.Info("send", "n", i, "body", string(payload))
		if err := write(conn, ws.EncodeFrame(ws.OpText, payload, true)); err != nil {
			return
		}

		// 读取直至下一次发送时刻，及时收到推送 / 二进制 / 响应帧。
		_ = conn.SetReadDeadline(time.Now().Add(*interval))
		for {
			opcode, payload, err := ws.ReadFrame(br)
			if err != nil {
				break // 读窗口到期或连接关闭
			}
			switch opcode {
			case ws.OpPong:
				slog.Info("receive pong", "payload", string(payload))
			case ws.OpBinary:
				slog.Info("receive binary", "bytes", len(payload))
			case ws.OpText:
				slog.Info("receive text", "body", string(payload))
			case ws.OpClose:
				return
			}
		}
	}
}

// randSeq 生成 1000~9999 的随机 seq。
func randSeq() int64 { return rand.Int64N(9000) + 1000 }

func write(conn net.Conn, b []byte) error {
	_, err := conn.Write(b)
	return err
}
