package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"time"

	"gametrace/examples/lp/internal/lp"
)

// 消息类型常量：与协议语义配置保持一致，
// 覆盖 message id / request-response role / seq correlation / push rule / error 五类格式。
const (
	cmdLoginRequest = 1001 // 请求消息（role=request）
)

// envelope 是 lp 帧 payload 携带的 JSON 信封，解码器按 cmd / seq / error_code 提取语义。
type envelope struct {
	Cmd       int `json:"cmd"`                  // message id
	Seq       int `json:"seq"`                  // seq correlation（请求/响应配对键）
	ErrorCode int `json:"error_code,omitempty"` // error 语义：0 成功，非 0 失败
}

// randomEnvelope 随机生成一条请求消息，覆盖各类语义格式：
//   - cmd=1001 + 非零 seq：request role + message id + seq correlation
//   - seq==0：命中 push rule（服务端只推送，客户端仅用于演示语义）
//   - error_code=1：error 语义
func randomEnvelope() envelope {
	switch rand.IntN(4) {
	case 0, 1, 2: // 正常请求
		return envelope{Cmd: cmdLoginRequest, Seq: randSeq()}
	case 3: // 错误请求（error 语义）
		return envelope{Cmd: cmdLoginRequest, Seq: randSeq(), ErrorCode: 1}
	default:
		return envelope{Cmd: cmdLoginRequest, Seq: 0} // seq==0 命中 push rule
	}
}

// randSeq 生成 1000~9999 的随机 seq。
func randSeq() int { return rand.IntN(9000) + 1000 }

func main() {
	addr := flag.String("addr", "127.0.0.1:8998", "server address")
	interval := flag.Duration("interval", 2*time.Second, "send interval")
	count := flag.Int("count", 10, "messages to send before exit (0 = forever)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		slog.Error("dial failed", "error", err)
		os.Exit(1)
	}
	defer conn.Close()
	slog.Info("connected", "addr", *addr)

	// 客户端同样逐帧随机选择长度字段宽度与端序，验证解码器对任意组合均能解析。
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for sent := 1; ; sent++ {
		ev := randomEnvelope()
		opts := lp.FrameOptions{
			Width:        []int{1, 2, 4}[rand.IntN(3)],
			LittleEndian: rand.IntN(2) == 0,
		}
		body, _ := json.Marshal(ev)
		slog.Info("send", "n", sent, "cmd", ev.Cmd, "seq", ev.Seq,
			"width", opts.Width, "little_endian", opts.LittleEndian)
		if _, err := conn.Write(lp.EncodeFrame(body, opts)); err != nil {
			slog.Error("write failed", "error", err)
			return
		}

		// 接收服务端的推送/响应并打印帧头信息。
		if f, err := lp.ReadFrame(conn); err != nil {
			slog.Warn("read reply failed", "error", err)
		} else {
			slog.Info("receive", "n", sent, "body", string(f.Payload),
				"width", f.Width, "little_endian", f.LittleEndian)
		}

		if *count > 0 && sent >= *count {
			return
		}
		<-tick.C
	}
}
