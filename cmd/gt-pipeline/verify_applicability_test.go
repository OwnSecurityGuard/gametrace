package main

import "testing"

// TestRawPortMatches 覆盖双向往返命中与端口信息缺失（<=0）时的宽松语义。
func TestRawPortMatches(t *testing.T) {
	cases := []struct {
		src, dst string
		port     int32
		want     bool
	}{
		{"10.0.0.1:52340", "10.0.0.2:8080", 8080, true},  // client→server
		{"10.0.0.2:8080", "10.0.0.1:52340", 8080, true},  // server→client
		{"10.0.0.1:52340", "10.0.0.3:3306", 8080, false}, // 完全无关的端口
		{"10.0.0.1:52340", "10.0.0.2:8080", 0, true},     // 无端口信息 → 不误伤
	}
	for _, c := range cases {
		if got := rawPortMatches(c.src, c.dst, c.port); got != c.want {
			t.Errorf("rawPortMatches(%q, %q, %d) = %v, want %v", c.src, c.dst, c.port, got, c.want)
		}
	}
}

// TestSessionApplicability 锁定适用性判定：无包 / 零命中 → not_applicable，
// 有命中 → ok。verdict 覆盖在 pipeline Verify 中单测（见 pipeline_service_test）。
func TestSessionApplicability(t *testing.T) {
	app := sessionApplicability(8080, 0, 0)
	if app.Applicable || app.Reason != "no_raw_packets" {
		t.Errorf("empty window: got applicable=%v reason=%q, want false/no_raw_packets", app.Applicable, app.Reason)
	}

	app = sessionApplicability(8080, 1200, 0)
	if app.Applicable || app.Reason != "no_matching_packets" {
		t.Errorf("zero match: got applicable=%v reason=%q, want false/no_matching_packets", app.Applicable, app.Reason)
	}
	if app.TotalPackets != 1200 || app.MatchedPackets != 0 || app.TargetPort != 8080 {
		t.Errorf("zero match counters wrong: %+v", app)
	}

	app = sessionApplicability(8080, 1200, 836)
	if !app.Applicable || app.Reason != "ok" {
		t.Errorf("partial match: got applicable=%v reason=%q, want true/ok", app.Applicable, app.Reason)
	}

	// 无端口信息的会话退化为全命中（保旧行为：不因端口判定误判不适用）。
	app = sessionApplicability(0, 1200, 0)
	if !app.Applicable || app.Reason != "ok" {
		t.Errorf("port<=0: got applicable=%v reason=%q, want true/ok", app.Applicable, app.Reason)
	}
}