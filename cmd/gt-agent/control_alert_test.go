package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gametrace/pkg/capture/agent/proto"
	"gametrace/pkg/checkrule"
)

func TestRecordAlertStoresAndLogs(t *testing.T) {
	dir := t.TempDir()
	store := newAlertStore(filepath.Join(dir, "alerts"))
	store.SetAddr("127.0.0.1:19500")

	// 不再有 opener / autoOpen 字段：recordAlert 只落盘并记日志，绝不打开浏览器。
	c := &ControlAgent{alerts: store}

	detail, _ := json.Marshal(sampleBundle("alX"))
	url := c.recordAlert(&proto.Notify{Title: "T", Message: "M", AlertId: "alX", DetailJson: detail})

	if want := "http://127.0.0.1:19500/v1/alerts/alX"; url != want {
		t.Fatalf("recordAlert url = %q, want %q", url, want)
	}
	if b, ok := store.Get("alX"); !ok || b.RuleName != "login" {
		t.Fatalf("bundle not stored: ok=%v b=%+v", ok, b)
	}
	// 告警记录 JSON 落在 spool/alerts 目录（此处 dir=alerts 父级）。
	if _, err := os.Stat(filepath.Join(dir, "alerts", "alX.json")); err != nil {
		t.Fatalf("bundle not persisted under alerts dir: %v", err)
	}
	// 诊断日志落在同级 alerts.log，含 alert id 与详情页 URL。
	logRaw, err := os.ReadFile(filepath.Join(dir, "alerts.log"))
	if err != nil {
		t.Fatalf("alerts.log not written: %v", err)
	}
	if !strings.Contains(string(logRaw), "ALERT id=alX") || !strings.Contains(string(logRaw), url) {
		t.Fatalf("alerts.log missing expected fields:\n%s", logRaw)
	}
}

func TestRecordAlertPlainNotifyIsNoop(t *testing.T) {
	store := newAlertStore(filepath.Join(t.TempDir(), "alerts"))
	c := &ControlAgent{alerts: store}
	if url := c.recordAlert(&proto.Notify{Title: "T", Message: "M"}); url != "" {
		t.Fatalf("plain notify (no alert_id) should yield empty url, got %q", url)
	}
	if len(store.List()) != 0 {
		t.Fatal("plain notify should not store anything")
	}
}

func TestRecordAlertDisabledStore(t *testing.T) {
	c := &ControlAgent{} // alerts nil → 未启用告警展示
	if url := c.recordAlert(&proto.Notify{AlertId: "x", DetailJson: []byte("{}")}); url != "" {
		t.Fatalf("nil store should yield empty url, got %q", url)
	}
}

// ---- 本地控制面只读路由 ----

func newTestLocalControl(t *testing.T) (*localControl, *alertStore) {
	t.Helper()
	store := newAlertStore(filepath.Join(t.TempDir(), "alerts"))
	lc := &localControl{alerts: store}
	return lc, store
}

func loopbackGet(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "127.0.0.1:53123"
	return r
}

func TestHandleAlertsList(t *testing.T) {
	lc, store := newTestLocalControl(t)
	store.Put(sampleBundle("a1"))
	rec := httptest.NewRecorder()
	lc.handleAlertsList(rec, loopbackGet("/v1/alerts"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		OK     bool           `json:"ok"`
		Alerts []alertSummary `json:"alerts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || len(resp.Alerts) != 1 || resp.Alerts[0].AlertID != "a1" {
		t.Fatalf("bad list payload: %+v", resp)
	}
}

func TestHandleAlertDetailHTML(t *testing.T) {
	lc, store := newTestLocalControl(t)
	store.Put(sampleBundle("a1"))
	rec := httptest.NewRecorder()
	lc.handleAlertDetail(rec, loopbackGet("/v1/alerts/a1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"登录", "触发记录", "a1-t", "a1-c1", "game.login"} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail HTML missing %q\n---\n%s", want, body)
		}
	}
}

func TestHandleAlertDetailNotFound(t *testing.T) {
	lc, _ := newTestLocalControl(t)
	rec := httptest.NewRecorder()
	lc.handleAlertDetail(rec, loopbackGet("/v1/alerts/nope"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", rec.Code)
	}
}

func TestHandleAlertDetailRejectsNonLoopback(t *testing.T) {
	lc, store := newTestLocalControl(t)
	store.Put(sampleBundle("a1"))
	r := httptest.NewRequest(http.MethodGet, "/v1/alerts/a1", nil)
	r.RemoteAddr = "203.0.113.9:1234" // 非回环
	rec := httptest.NewRecorder()
	lc.handleAlertDetail(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status = %d, want 403", rec.Code)
	}
}

func TestHandleAlertDetailRejectsMethodAndBadID(t *testing.T) {
	lc, _ := newTestLocalControl(t)
	// POST 不允许
	rec := httptest.NewRecorder()
	lc.handleAlertDetail(rec, httptest.NewRequest(http.MethodPost, "/v1/alerts/a1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
	// 含非法字符的 id → 404（safeAlertID 拦截）
	rec = httptest.NewRecorder()
	lc.handleAlertDetail(rec, loopbackGet("/v1/alerts/bad!id"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad id status = %d, want 404", rec.Code)
	}
}

func TestAlertDetailHTMLEscapes(t *testing.T) {
	b := checkrule.AlertBundle{
		AlertID: "x", Title: `<script>alert(1)</script>`,
		Trigger: checkrule.AlertRecord{ID: "t", Type: "x", Data: json.RawMessage(`{"k":"<img onerror=x>"}`)},
	}
	html := alertDetailHTML(b)
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("title script tag should be escaped")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("expected escaped title")
	}
}
