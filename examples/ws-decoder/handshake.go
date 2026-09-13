package main

import (
	"bytes"
	"strconv"
	"strings"
)

// wsHandshake is one parsed HTTP handshake message (request or response).
type wsHandshake struct {
	isRequest bool
	method    string
	path      string
	host      string
	version   string // Sec-WebSocket-Version (request only)
	status    int64  // response only
	upgrade   bool   // Upgrade: websocket
}

// parseHandshake tries to read exactly one HTTP handshake message from the
// front of buf (through the blank line). Non-upgrade messages parse fine but
// carry upgrade=false so the caller can skip them. When ok is false the caller
// must NOT consume anything: the bytes so far are an incomplete message.
func parseHandshake(buf []byte) (hs *wsHandshake, consumed int, ok bool) {
	hi := bytes.Index(buf, []byte("\r\n\r\n"))
	if hi < 0 {
		return nil, 0, false
	}
	lines := bytes.Split(buf[:hi], []byte("\r\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		return nil, 0, false
	}

	hs = &wsHandshake{}
	first := strings.Fields(string(lines[0]))
	switch {
	case len(first) >= 3 && strings.HasPrefix(first[0], "HTTP/"):
		if c, e := strconv.Atoi(first[1]); e == nil {
			hs.status = int64(c)
		}
	case len(first) >= 2:
		hs.isRequest = true
		hs.method = first[0]
		hs.path = first[1]
	default:
		return nil, 0, false
	}

	for _, ln := range lines[1:] {
		kv := bytes.SplitN(ln, []byte(":"), 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(string(kv[0])))
		val := strings.TrimSpace(string(kv[1]))
		switch key {
		case "host":
			hs.host = val
		case "upgrade":
			hs.upgrade = strings.EqualFold(val, "websocket")
		case "sec-websocket-version":
			hs.version = val
		}
	}
	return hs, hi + 4, true
}
