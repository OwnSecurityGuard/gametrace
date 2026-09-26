// Package ws implements the RFC 6455 subset needed by the examples/ws
// simulator: frame encode/decode and the HTTP Upgrade handshake. It mirrors
// what examples/ws-decoder parses from captured traffic.
package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
)

// RFC 6455 §5.2 frame opcodes.
const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

// wsGUID is the RFC 6455 §1.3 magic GUID used to derive Sec-WebSocket-Accept.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// EncodeFrame builds one WebSocket frame with FIN set. Client→server frames
// must be masked, server→client frames must not (RFC 6455 §5.3).
func EncodeFrame(opcode byte, payload []byte, masked bool) []byte {
	n := len(payload)
	var hdr []byte
	switch {
	case n < 126:
		hdr = make([]byte, 2)
		hdr[1] = byte(n)
	case n <= 0xFFFF:
		hdr = make([]byte, 4)
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
	}
	hdr[0] = 0x80 | opcode // FIN + opcode

	if !masked {
		return append(hdr, payload...)
	}
	hdr[1] |= 0x80
	mask := [4]byte{
		byte(rand.IntN(256)), byte(rand.IntN(256)),
		byte(rand.IntN(256)), byte(rand.IntN(256)),
	}
	out := make([]byte, 0, len(hdr)+4+n)
	out = append(out, hdr...)
	out = append(out, mask[:]...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out
}

// ReadFrame reads one complete frame from r and returns its opcode and
// unmasked payload.
func ReadFrame(r io.Reader) (opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	if length < 0 || length > 1<<30 {
		return 0, nil, fmt.Errorf("ws: implausible payload length %d", length)
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

// NewClientKey returns a fresh Sec-WebSocket-Key value (16 random bytes, base64).
func NewClientKey() string {
	raw := make([]byte, 16)
	for i := range raw {
		raw[i] = byte(rand.IntN(256))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// ComputeAcceptKey derives the Sec-WebSocket-Accept value for a client key
// per RFC 6455 §4.2.2: SHA-1(key + GUID) then base64.
func ComputeAcceptKey(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// WriteHandshakeRequest writes the client HTTP Upgrade request to w.
func WriteHandshakeRequest(w io.Writer, addr, path, key string) error {
	_, err := fmt.Fprintf(w,
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"\r\n", path, addr, key)
	return err
}

// ReadHandshake reads one HTTP handshake message (request or response) from br,
// consuming through the blank line. It returns the status line and the header
// map (keys lower-cased).
func ReadHandshake(br *bufio.Reader) (statusLine string, headers map[string]string, err error) {
	headers = make(map[string]string)
	for {
		var line string
		line, err = br.ReadString('\n')
		if err != nil {
			return "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if statusLine == "" {
			statusLine = line
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return statusLine, headers, nil
}
