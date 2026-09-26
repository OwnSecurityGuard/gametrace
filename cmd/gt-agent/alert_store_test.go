package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gametrace/pkg/checkrule"
)

func sampleBundle(id string) checkrule.AlertBundle {
	return checkrule.AlertBundle{
		AlertID: id, RuleID: "r1", RuleName: "login", SessionID: "s1",
		Title: "命中", Message: "登录", GeneratedAt: "2026-09-26T00:00:00Z",
		Trigger: checkrule.AlertRecord{ID: id + "-t", Type: "game.login", Direction: "request"},
		Context: map[string][]checkrule.AlertRecord{"response": {{ID: id + "-c1", Direction: "response"}}},
	}
}

func TestAlertStorePutGetList(t *testing.T) {
	s := newAlertStore(filepath.Join(t.TempDir(), "alerts"))
	s.Put(sampleBundle("a1"))
	s.Put(sampleBundle("a2"))

	b, ok := s.Get("a1")
	if !ok || b.AlertID != "a1" || len(b.Context["response"]) != 1 {
		t.Fatalf("Get(a1) wrong: ok=%v b=%+v", ok, b)
	}
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// 新→旧：最后 Put 的 a2 在前。
	if list[0].AlertID != "a2" {
		t.Fatalf("List not newest-first: %s, %s", list[0].AlertID, list[1].AlertID)
	}
}

func TestAlertStoreDiskPersistenceAndHydrate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "alerts")
	s := newAlertStore(dir)
	s.Put(sampleBundle("x1"))
	if _, err := os.Stat(filepath.Join(dir, "x1.json")); err != nil {
		t.Fatalf("bundle not persisted to disk: %v", err)
	}

	// 新实例从磁盘回填内存（模拟探针重启后详情页仍可读）。
	s2 := newAlertStore(dir)
	if b, ok := s2.Get("x1"); !ok || b.RuleName != "login" {
		t.Fatalf("hydrate failed: ok=%v b=%+v", ok, b)
	}
	if len(s2.List()) != 1 {
		t.Fatalf("hydrated list len = %d, want 1", len(s2.List()))
	}
}

func TestAlertStoreRingEviction(t *testing.T) {
	s := newAlertStore(filepath.Join(t.TempDir(), "alerts"))
	for i := 0; i < alertStoreCapacity+10; i++ {
		s.Put(sampleBundle("id" + itoaStore(i)))
	}
	if len(s.List()) != alertStoreCapacity {
		t.Fatalf("ring not capped at %d, got %d", alertStoreCapacity, len(s.List()))
	}
	// 最旧的 id0 应被淘汰出内存缓存。
	if _, ok := s.mem["id0"]; ok {
		t.Fatal("oldest alert should be evicted from memory ring")
	}
}

func TestAlertStoreRejectsUnsafeID(t *testing.T) {
	s := newAlertStore(filepath.Join(t.TempDir(), "alerts"))
	s.Put(checkrule.AlertBundle{AlertID: "../evil"})
	if _, ok := s.Get("../evil"); ok {
		t.Fatal("unsafe id must not be stored")
	}
	// 空 id 忽略。
	s.Put(checkrule.AlertBundle{AlertID: "  "})
	if len(s.List()) != 0 {
		t.Fatal("empty id should be ignored")
	}
}

func TestAlertStoreDetailURL(t *testing.T) {
	s := newAlertStore(filepath.Join(t.TempDir(), "alerts"))
	if got := s.DetailURL("a1"); got != "" {
		t.Fatalf("no addr set → URL should be empty, got %q", got)
	}
	s.SetAddr("127.0.0.1:19501")
	if got := s.DetailURL("a1"); got != "http://127.0.0.1:19501/v1/alerts/a1" {
		t.Fatalf("DetailURL = %q", got)
	}
	if got := s.DetailURL("../x"); got != "" {
		t.Fatalf("unsafe id should yield empty URL, got %q", got)
	}
}

func TestSafeAlertID(t *testing.T) {
	good := []string{"abc", "ABC123", "a-b_c", "019a-dead"}
	bad := []string{"", "../etc/passwd", "a/b", "a\\b", "a b", "a.b", `a"b`, "x;y"}
	for _, g := range good {
		if !safeAlertID(g) {
			t.Errorf("safeAlertID(%q) = false, want true", g)
		}
	}
	for _, b := range bad {
		if safeAlertID(b) {
			t.Errorf("safeAlertID(%q) = true, want false", b)
		}
	}
}

func TestAlertLogPath(t *testing.T) {
	if got := alertLogPath(filepath.Join("spool", "alerts")); got != filepath.Join("spool", "alerts.log") {
		t.Fatalf("alertLogPath = %q, want spool/alerts.log", got)
	}
	if got := alertLogPath(""); got != "" {
		t.Fatalf("empty dir should yield empty log path, got %q", got)
	}
}

func TestAlertStoreLogf(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "alerts")
	s := newAlertStore(dir)
	s.Logf("ALERT id=%s stored=true", "abc")
	s.Logf("NOTIFY id=%s delivered=true", "abc")

	raw, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "alerts.log"))
	if err != nil {
		t.Fatalf("log not written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d:\n%s", len(lines), raw)
	}
	if !strings.Contains(lines[0], "ALERT id=abc") || !strings.Contains(lines[1], "NOTIFY id=abc") {
		t.Fatalf("log content mismatch:\n%s", raw)
	}
	// 每行带 RFC3339 时间戳前缀。
	if _, err := time.Parse(time.RFC3339, strings.Fields(lines[0])[0]); err != nil {
		t.Fatalf("log line lacks RFC3339 timestamp prefix: %q (%v)", lines[0], err)
	}
}

func itoaStore(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}
