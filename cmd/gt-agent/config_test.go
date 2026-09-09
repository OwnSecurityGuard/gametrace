package main

// config_test.go 覆盖 suppliedConfig.adopt 的判定：它决定「新下载的探针归谁」。
// 判错的代价是探针连到上一个 owner / 上一个服务端，且必然先失败一次才自愈，
// 所以这条规则必须有测试钉住。

import (
	"encoding/json"
	"testing"
)

func TestSuppliedConfigAdopt(t *testing.T) {
	tests := []struct {
		name    string
		cfg     agentConfig
		sup     suppliedConfig
		wantWrt bool // 配置被改写（需要落盘）
		wantTok string
		wantSrv string
		// wantCreds=true 表示原有 probe 凭证应当保留。
		wantCreds bool
	}{
		{
			name:      "首次下发：填身份但不动作废（本来就没凭证）",
			cfg:       agentConfig{},
			sup:       suppliedConfig{token: "gt_a", server: "h:19091"},
			wantWrt:   true,
			wantTok:   "gt_a",
			wantSrv:   "h:19091",
			wantCreds: true,
		},
		{
			name:      "同身份重下：什么都不变",
			cfg:       agentConfig{UserToken: "gt_a", Server: "h:19091", ProbeID: "prb_1", ProbeToken: "gt_prb_1"},
			sup:       suppliedConfig{token: "gt_a", server: "h:19091"},
			wantWrt:   false,
			wantTok:   "gt_a",
			wantSrv:   "h:19091",
			wantCreds: true,
		},
		{
			name:      "换人重下：覆盖身份并作废旧凭证",
			cfg:       agentConfig{UserToken: "gt_a", ProbeID: "prb_1", ProbeToken: "gt_prb_1"},
			sup:       suppliedConfig{token: "gt_b"},
			wantWrt:   true,
			wantTok:   "gt_b",
			wantCreds: false,
		},
		{
			name:      "匿名凭证补身份：旧凭证归属错了，作废",
			cfg:       agentConfig{ProbeID: "prb_1", ProbeToken: "gt_prb_1"},
			sup:       suppliedConfig{token: "gt_b"},
			wantWrt:   true,
			wantTok:   "gt_b",
			wantCreds: false,
		},
		{
			name:      "换服务端：旧凭证新服务端不认，作废",
			cfg:       agentConfig{UserToken: "gt_a", Server: "old:19091", ProbeID: "prb_1", ProbeToken: "gt_prb_1"},
			sup:       suppliedConfig{server: "new:19091"},
			wantWrt:   true,
			wantTok:   "gt_a",
			wantSrv:   "new:19091",
			wantCreds: false,
		},
		{
			name:      "下发为空：不动现有配置",
			cfg:       agentConfig{UserToken: "gt_a", Server: "old:19091", ProbeID: "prb_1", ProbeToken: "gt_prb_1"},
			sup:       suppliedConfig{},
			wantWrt:   false,
			wantTok:   "gt_a",
			wantSrv:   "old:19091",
			wantCreds: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			got := tt.sup.adopt(&cfg, "test")
			if got != tt.wantWrt {
				t.Errorf("adopt() written = %v, want %v", got, tt.wantWrt)
			}
			if cfg.UserToken != tt.wantTok {
				t.Errorf("UserToken = %q, want %q", cfg.UserToken, tt.wantTok)
			}
			if tt.wantSrv != "" && cfg.Server != tt.wantSrv {
				t.Errorf("Server = %q, want %q", cfg.Server, tt.wantSrv)
			}
			// 只有原本就有凭证时才断言保留/作废（本来就没凭证的用例无从判断）。
			if tt.cfg.ProbeID != "" || tt.cfg.ProbeToken != "" {
				hasCreds := cfg.ProbeID != "" && cfg.ProbeToken != ""
				if tt.wantCreds && !hasCreds {
					t.Errorf("credentials were dropped but should be kept: %+v", cfg)
				}
				if !tt.wantCreds && hasCreds {
					t.Errorf("credentials kept but should be dropped: %+v", cfg)
				}
			}
		})
	}
}

// TestArchiveDefaultEnable 钉住"数据落盘留存"的默认开关：archive.enabled 未显式
// 给出时默认开启（让抓包数据持久化到磁盘），只有显式 false 才算关闭。
func TestArchiveDefaultEnable(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "无 archive 键（升级前老配置）", raw: `{"probe_id":"p1"}`, want: true},
		{name: "空 archive 对象", raw: `{"archive":{}}`, want: true},
		{name: "archive 未给 enabled", raw: `{"archive":{"max_age_hours":12}}`, want: true},
		{name: "显式 enabled:true", raw: `{"archive":{"enabled":true}}`, want: true},
		{name: "显式 enabled:false", raw: `{"archive":{"enabled":false}}`, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cfg agentConfig
			if err := json.Unmarshal([]byte(c.raw), &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			cfg.Archive.defaultEnableOn()
			if cfg.Archive.Enabled != c.want {
				t.Errorf("Archive.Enabled = %v, want %v (configured=%v)",
					cfg.Archive.Enabled, c.want, cfg.Archive.configured)
			}
		})
	}
}

func TestTokenFingerprint(t *testing.T) {
	if got := tokenFingerprint(""); got != "" {
		t.Errorf("tokenFingerprint(\"\") = %q, want empty", got)
	}
	a, b := tokenFingerprint("gt_a"), tokenFingerprint("gt_b")
	if a == b {
		t.Fatalf("different tokens share fingerprint %q", a)
	}
	if len(a) != 8 {
		t.Errorf("fingerprint len = %d, want 8", len(a))
	}
	// 凭证明文不能进日志。
	if a == "gt_a" {
		t.Error("fingerprint must not be the token itself")
	}
}
