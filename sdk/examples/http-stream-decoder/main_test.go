package main

import (
	"strings"
	"testing"
)

func TestParseMessageRequest(t *testing.T) {
	raw := "GET /api/player HTTP/1.1\r\nHost: example.com\r\nContent-Length: 3\r\n\r\nabc"
	msg, n, ok := parseMessage([]byte(raw))
	if !ok {
		t.Fatal("expected request to parse")
	}
	if n != len(raw) {
		t.Fatalf("consumed = %d, want %d", n, len(raw))
	}
	if !msg.isRequest || msg.method != "GET" || msg.path != "/api/player" ||
		msg.host != "example.com" || msg.contentLength != 3 {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func TestParseMessageResponse(t *testing.T) {
	raw := "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"
	msg, n, ok := parseMessage([]byte(raw))
	if !ok {
		t.Fatal("expected response to parse")
	}
	if n != len(raw) {
		t.Fatalf("consumed = %d, want %d", n, len(raw))
	}
	if msg.isRequest || msg.status != 404 {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func TestParseMessageIncomplete(t *testing.T) {
	raw := "GET /api/player HTTP/1.1\r\nHost: example.com\r\nContent-Len"
	_, _, ok := parseMessage([]byte(raw))
	if ok {
		t.Fatal("truncated request must not parse")
	}
}

func TestParseMessageTwoInOneBuffer(t *testing.T) {
	first := "GET /a HTTP/1.1\r\nHost: h\r\n\r\n"
	second := "GET /b HTTP/1.1\r\nHost: h\r\n\r\n"
	buf := []byte(first + second)
	_, n, ok := parseMessage(buf)
	if !ok || n != len(first) {
		t.Fatalf("first message: ok=%v consumed=%d want=%d", ok, n, len(first))
	}
	_, n2, ok2 := parseMessage(buf[n:])
	if !ok2 || n2 != len(second) {
		t.Fatalf("second message: ok=%v consumed=%d want=%d", ok2, n2, len(second))
	}
}

func TestParseMessageChunkedResponse(t *testing.T) {
	var b strings.Builder
	b.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
	b.WriteString("5\r\nhello\r\n0\r\n\r\n")
	msg, _, ok := parseMessage([]byte(b.String()))
	if !ok {
		t.Fatal("expected chunked response to parse")
	}
	if msg.contentLength != 0 {
		t.Fatalf("chunked content_length = %d, want 0", msg.contentLength)
	}
}
