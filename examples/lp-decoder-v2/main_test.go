package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func encodeTestFrame(t *testing.T, payload []byte, width int, little bool) []byte {
	t.Helper()
	param := byte(0)
	if little {
		param |= paramEndian
	}
	switch width {
	case 2:
		param |= 1 << paramWidthShift
	case 4:
		param |= 2 << paramWidthShift
	}
	out := []byte{magic, version, param, 0}
	switch width {
	case 1:
		out = append(out, byte(len(payload)))
	case 2:
		var b [2]byte
		if little {
			binary.LittleEndian.PutUint16(b[:], uint16(len(payload)))
		} else {
			binary.BigEndian.PutUint16(b[:], uint16(len(payload)))
		}
		out = append(out, b[:]...)
	case 4:
		var b [4]byte
		if little {
			binary.LittleEndian.PutUint32(b[:], uint32(len(payload)))
		} else {
			binary.BigEndian.PutUint32(b[:], uint32(len(payload)))
		}
		out = append(out, b[:]...)
	}
	return append(out, payload...)
}

func TestParseFrameAllVariants(t *testing.T) {
	body := []byte(`{"cmd":1001,"seq":7,"data":{"account":"a1"}}`)
	for _, width := range []int{1, 2, 4} {
		for _, little := range []bool{false, true} {
			raw := encodeTestFrame(t, body, width, little)
			f, err := parseFrame(raw)
			if err != nil {
				t.Fatalf("width=%d little=%v: %v", width, little, err)
			}
			if f == nil {
				t.Fatalf("width=%d little=%v: incomplete", width, little)
			}
			if f.consumed != len(raw) {
				t.Fatalf("width=%d little=%v: consumed=%d want=%d", width, little, f.consumed, len(raw))
			}
			if f.env.Cmd != 1001 || f.env.Seq != 7 {
				t.Fatalf("width=%d little=%v: env=%+v", width, little, f.env)
			}
		}
	}
}

func TestParseFrameIncompleteAndErrors(t *testing.T) {
	body := []byte(`{"cmd":1003,"seq":1}`)
	raw := encodeTestFrame(t, body, 2, false)
	for i := 0; i < len(raw); i++ {
		f, err := parseFrame(raw[:i])
		if err != nil || f != nil {
			t.Fatalf("prefix len=%d should be incomplete, got f=%v err=%v", i, f, err)
		}
	}
	// 两帧连发：只解第一帧
	second := encodeTestFrame(t, body, 1, true)
	both := append(append([]byte{}, raw...), second...)
	f, err := parseFrame(both)
	if err != nil || f == nil || f.consumed != len(raw) {
		t.Fatalf("multi-frame: f=%v err=%v", f, err)
	}
	// 坏 magic
	bad := bytes.Clone(raw)
	bad[0] = 'X'
	if _, err := parseFrame(bad); err == nil {
		t.Fatal("bad magic should error")
	}
	// 非零保留位
	bad2 := bytes.Clone(raw)
	bad2[3] = 1
	if _, err := parseFrame(bad2); err == nil {
		t.Fatal("reserved byte should error")
	}
	// 信封非法 JSON：帧仍完整，cmd=0
	garbage := encodeTestFrame(t, []byte("not-json"), 1, false)
	f, err = parseFrame(garbage)
	if err != nil || f == nil || f.env.Cmd != 0 || f.consumed != len(garbage) {
		t.Fatalf("garbage envelope: f=%v err=%v", f, err)
	}
}
