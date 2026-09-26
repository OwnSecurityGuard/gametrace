package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
)

// httpMessage is one parsed HTTP message, request or response. Body bytes are
// reduced to their length — a decoder for traffic analysis rarely needs the
// raw body, and keeping it would bloat every event payload.
type httpMessage struct {
	isRequest     bool
	method        string
	path          string
	host          string
	status        int64
	contentLength int64
}

// parseMessage tries to read exactly one HTTP message from the front of buf.
//
// It returns the message, the number of bytes consumed, and whether a complete
// message was available. When ok is false the caller must NOT consume anything:
// the bytes so far are either an incomplete message (more segments will
// complete it) or not HTTP at all.
//
// The byte-accounting trick: wrap buf in a bufio.Reader; after a successful
// read plus a full body drain, everything the reader did not hand out is still
// Buffered(), so consumed = len(buf) - Buffered().
func parseMessage(buf []byte) (msg *httpMessage, consumed int, ok bool) {
	if m, n, ok := parseRequest(buf); ok {
		return m, n, true
	}
	return parseResponse(buf)
}

func parseRequest(buf []byte) (msg *httpMessage, consumed int, ok bool) {
	br := bufio.NewReader(bytes.NewReader(buf))
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, 0, false
	}
	// Drain the body so consumed covers it. A nil Body (rare) means no body.
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	m := &httpMessage{
		isRequest:     true,
		method:        req.Method,
		path:          req.URL.Path,
		host:          req.Host,
		contentLength: contentLengthOrZero(req.ContentLength),
	}
	return m, len(buf) - br.Buffered(), true
}

func parseResponse(buf []byte) (msg *httpMessage, consumed int, ok bool) {
	br := bufio.NewReader(bytes.NewReader(buf))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, 0, false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	m := &httpMessage{
		isRequest:     false,
		status:        int64(resp.StatusCode),
		contentLength: contentLengthOrZero(resp.ContentLength),
	}
	return m, len(buf) - br.Buffered(), true
}

func contentLengthOrZero(n int64) int64 {
	if n < 0 {
		return 0 // chunked / unknown
	}
	return n
}
