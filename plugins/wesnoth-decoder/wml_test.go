package main

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

// gzipText 把文本 gzip 压缩（客户端 io::write_gz 路径）。
func gzipText(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// bzip2Text 用预生成的 bzip2 流解出文本（Go 标准库无 bzip2 writer，
// 固件由 python bz2 生成，首字节 'B' 命中 decompressWML 的 bzip2 分支）。
func bzip2Text(t *testing.T, hex string) string {
	t.Helper()
	raw := mustHex(t, hex)
	if raw[0] != 'B' {
		t.Fatalf("fixture must start with 'B', got %#x", raw[0])
	}
	out, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("bzip2 read: %v", err)
	}
	return string(out)
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	out, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return out
}

// ---- 用例 ----

func TestParseWMLBasic(t *testing.T) {
	text := `[login]
username="player1"
password="s3cret"
[/login]`
	root, err := parseWML(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(root.children) != 1 || root.children[0].name != "login" {
		t.Fatalf("root children = %d, want 1x login", len(root.children))
	}
	login := root.children[0]
	if len(login.attrs) != 2 {
		t.Fatalf("attrs = %d, want 2", len(login.attrs))
	}
	if login.attrs[0] != (wmlAttr{"username", "player1"}) || login.attrs[1] != (wmlAttr{"password", "s3cret"}) {
		t.Fatalf("attrs = %+v", login.attrs)
	}
}

func TestParseWMLUnquotedAndNested(t *testing.T) {
	text := `[gamelist]
[game]
name=Test Game
port=15000
[era]
id=default
[/era]
[/game]
[user]
name=alice
location=0
[/user]
[/gamelist]`
	root, err := parseWML(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(root.children) != 1 || root.children[0].name != "gamelist" {
		t.Fatalf("want single gamelist child, got %d", len(root.children))
	}
	gl := root.children[0]
	if len(gl.children) != 2 {
		t.Fatalf("gamelist children = %d, want 2", len(gl.children))
	}
	if gl.children[0].name != "game" || gl.children[1].name != "user" {
		t.Fatalf("children names = %s/%s", gl.children[0].name, gl.children[1].name)
	}
	game := gl.children[0]
	if game.attrs[0] != (wmlAttr{"name", "Test Game"}) || game.attrs[1] != (wmlAttr{"port", "15000"}) {
		t.Fatalf("game attrs = %+v", game.attrs)
	}
	if len(game.children) != 1 || game.children[0].name != "era" || game.children[0].attrs[0].value != "default" {
		t.Fatalf("era = %+v", game.children)
	}
}

func TestParseWMLQuotedEscapesAndContinuation(t *testing.T) {
	text := "[message]\n" +
		`sender="alice"` + "\n" +
		`message="line1" +` + "\n" +
		"# translation note\n" +
		`	_"line2 with ""quoted"" part"` + "\n" +
		"[/message]"
	root, err := parseWML(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	msg := root.children[0]
	want := `line1` + "\n" + `line2 with "quoted" part`
	if msg.attrs[0].key != "sender" || msg.attrs[0].value != "alice" {
		t.Fatalf("sender attr = %+v", msg.attrs[0])
	}
	if msg.attrs[1].key != "message" || msg.attrs[1].value != want {
		t.Fatalf("message attr = %+v, want %q", msg.attrs[1], want)
	}
}

func TestParseWMLLenientOnMalformed(t *testing.T) {
	// 缺闭 tag、垃圾行、未闭合引号都不应返回 error（宽容解析）。
	text := "[turn]\n" +
		"garbage line without equals\n" +
		`side=1` + "\n" +
		`unclosed="value` + "\n" +
		"[command]\n" +
		`[move]x=3[/move]` + "\n" // 缺 [/command] 与 [/turn]
	root, err := parseWML(text)
	if err != nil {
		t.Fatalf("parse must be lenient: %v", err)
	}
	turn := root.children[0]
	if turn.name != "turn" {
		t.Fatalf("root child = %s, want turn", turn.name)
	}
	foundSide := false
	for _, a := range turn.attrs {
		if a.key == "side" && a.value == "1" {
			foundSide = true
		}
	}
	if !foundSide {
		t.Fatalf("side attr not parsed: %+v", turn.attrs)
	}
}

func TestDecompressWMLGzip(t *testing.T) {
	body := gzipText(t, `[login]
username="player1"
[/login]`)
	out, ok := decompressWML(body)
	if !ok || !strings.Contains(string(out), "player1") {
		t.Fatalf("decompress gzip: ok=%v out=%q", ok, out)
	}
}

func TestDecompressWMLBzip2(t *testing.T) {
	// 固件：bzip2('[version]\nversion="1.18.0"\nclient_source="Wesnoth"\n[/version]')
	const fixture = "425a68393141592653590d77057a0000095b8000101001e042008a8a659f002000545068d1a0c80d08d29b4d4da9ea3264c6a197555626676a1caf647742d652f88185cec2ec71e503ca10760e262cbe97e2ee48a70a1201aee0af40"
	text := bzip2Text(t, fixture)
	out, ok := decompressWML(mustHex(t, fixture))
	if !ok {
		t.Fatalf("decompress bzip2: ok=false")
	}
	if string(out) != text {
		t.Fatalf("decompress mismatch: %q vs %q", out, text)
	}
}

func TestDecompressWMLGarbage(t *testing.T) {
	if _, ok := decompressWML([]byte("not a gzip stream at all")); ok {
		t.Fatalf("garbage payload must not decompress")
	}
	if _, ok := decompressWML([]byte("BZh garbage bzip2")); ok {
		t.Fatalf("garbage bzip2 payload must not decompress")
	}
	if _, ok := decompressWML(nil); ok {
		t.Fatalf("empty payload must not decompress")
	}
}
