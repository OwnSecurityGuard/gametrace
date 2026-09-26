package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestFrameRoundTripAllObservedFlags 用抓包实测出现过的全部 flags 组合做编解码闭环。
// flags 出处：{0x00,0x80,0x08,0x88,0x10,0x90}（20 个数据帧的字段实测）。
func TestFrameRoundTripAllObservedFlags(t *testing.T) {
	bodies := map[byte]string{
		0x00: `{"cmd":1003,"seq":1}`,                                                    // 1B BE(单字节无端序)
		0x80: `{"cmd":1001,"seq":3,"data":{"account":"acc"}}`,                           // 1B + LE 标志位
		0x08: `{"cmd":1005,"seq":7,"data":{"item_id":5001,"count":2}}`,                  // 2B BE
		0x88: `{"cmd":1001,"seq":2,"data":{"account":"acc-1062","device_id":"lp-sim"}}`, // 2B LE
		0x10: `{"cmd":1007,"seq":15,"data":{"resource":"gold","amount":50}}`,            // 4B BE
		0x90: `{"cmd":2001,"seq":0,"data":{"player_id":"P-1065","level":1}}`,            // 4B LE
	}
	for flags, body := range bodies {
		raw := buildFrame([]byte(body), flags)
		got, n, err := parseFrame(raw)
		if err != nil {
			t.Fatalf("flags=0x%02x: %v", flags, err)
		}
		if n != len(raw) {
			t.Errorf("flags=0x%02x: consumed=%d want %d", flags, n, len(raw))
		}
		if !bytes.Equal(got, []byte(body)) {
			t.Errorf("flags=0x%02x: body mismatch", flags)
		}
	}
}

// TestParseFrameTwoInOneSegment 一个 TCP 段承载两帧：连续两次 parseFrame+Consume 都切对。
func TestParseFrameTwoInOneSegment(t *testing.T) {
	a := buildFrame([]byte(`{"cmd":1003,"seq":9}`), 0x90)
	b := buildFrame([]byte(`{"cmd":1004,"seq":9,"data":{"items":[]}}`), 0x00)
	buf := append(append([]byte{}, a...), b...)

	body, n, err := parseFrame(buf)
	if err != nil || n != len(a) || !bytes.HasPrefix(body, []byte(`{"cmd":1003`)) {
		t.Fatalf("first: n=%d body=%s err=%v", n, body, err)
	}
	body, n, err = parseFrame(buf[n:])
	if err != nil || n != len(b) || !bytes.HasPrefix(body, []byte(`{"cmd":1004`)) {
		t.Fatalf("second: n=%d body=%s err=%v", n, body, err)
	}
}

// TestParseFrameIncompleteAtEveryPrefix 对真实帧的任意截断都必须报"不完整"而不是错误/panic。
func TestParseFrameIncompleteAtEveryPrefix(t *testing.T) {
	raw := buildFrame([]byte(`{"cmd":2002,"seq":0,"data":{"item_id":5001,"count":11,"delta":-1}}`), 0x88)
	for i := 0; i < len(raw)-1; i++ {
		if _, _, err := parseFrame(raw[:i]); !errors.Is(err, errFrameIncomplete) {
			t.Fatalf("prefix len=%d: err=%v want incomplete", i, err)
		}
	}
}

// TestParseFrameDesync 头部非法与长度越界（抓包失步实测到过 13824 / 1509949440 这类误读值）判失步。
func TestParseFrameDesync(t *testing.T) {
	if _, _, err := parseFrame([]byte("XXXXXX")); !errors.Is(err, errFrameDesync) {
		t.Fatalf("bad magic: err=%v", err)
	}
	// flags=0x10（4B BE）却填 LE 误读出的天文数字——正是分析期真实踩过的坑。
	junk := []byte{0x4c, 0x01, 0x10, 0x00, 0x5a, 0x00, 0x00, 0x00}
	if _, _, err := parseFrame(junk); !errors.Is(err, errFrameDesync) {
		t.Fatalf("huge length: err=%v", err)
	}
}

// TestManifestConsistency plugin.yaml 声明的 cmd 集合必须与解码器常量口径一致。
func TestManifestConsistency(t *testing.T) {
	b, err := os.ReadFile("plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	y := string(b)
	for _, want := range []string{
		"name: lp-decoder-v3",
		"protocol: lp",
		"api_version: gt.decoder/v2",
		"value: [1001, 1003, 1005, 1007]", // pair side0：请求 cmd 集
		"value: [1002, 1004, 1006, 1008]", // pair side1：响应 cmd 集
		"value: [2001, 2002, 2003]",       // push 集
		"effect: { type: name, key: cmd }",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("plugin.yaml missing %q", want)
		}
	}
	for _, id := range []string{"lp.name_msg", "lp.mark_push", "lp.mark_error", "lp.pair_seq"} {
		if !strings.Contains(y, "id: "+id) {
			t.Errorf("plugin.yaml missing rule %q", id)
		}
	}
}
