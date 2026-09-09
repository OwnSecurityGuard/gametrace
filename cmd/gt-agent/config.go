package main

// agentConfig 是探针的持久化配置（probe.json）+ 首启引导（命令行 flag / 启动码）。
//
// 优先级（v2 探针优化，docs/plans/2026-09-05 §4.1）：
//   1. 命令行 flag 非空 → 覆盖并写回 probe.json（首启引导一次性生效）；
//   2. probe.json 已有值 → 直接用（此后一切改参走本地控制面 / 远端指令）；
//   3. embedded / sidecar / 启动码 → 首启引导的默认值来源。
//
// probe.json 只存"身份与回连"，抓包参数（iface/ports/bpf）是会话级配置，
// 由平台指派或本地控制面临时给定，不落 probe.json。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// archiveConfig 是本地留存的保留策略（可在运行中经控制面 / 远端指令调整）。
//
// Enabled 默认开启（数据落盘留存），除非配置显式写了 "enabled": false。
// configured 标记「本次解析里是否显式出现了 enabled 键」，用于区分
// 「用户明确关闭」（保留 false）与「老配置没写过 archive」（默认开启）——
// 否则升级前的老 probe.json 会让抓包回到发后即焚、数据不持久化。
type archiveConfig struct {
	Enabled   bool  `json:"enabled"`
	MaxAgeHrs int   `json:"max_age_hours"` // 0 = 默认 24
	MaxBytes  int64 `json:"max_bytes"`     // 0 = 默认 4GB

	configured bool `json:"-"`
}

// UnmarshalJSON 在解析 archive 对象时记录 enabled 键是否出现。
func (c *archiveConfig) UnmarshalJSON(b []byte) error {
	type plain archiveConfig
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = archiveConfig(p)
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) == nil {
		if _, ok := raw["enabled"]; ok {
			c.configured = true
		}
	}
	return nil
}

// defaultEnableOn 在配置从未显式给出 enabled 时置为开启：保证老配置 / 升级前的
// probe.json（没有 archive 字段）也默认把抓包数据落盘留存，而不是发后即焚。
func (c *archiveConfig) defaultEnableOn() {
	if !c.configured {
		c.Enabled = true
		c.configured = true
	}
}

// agentConfig 是 probe.json 的内存形态。
type agentConfig struct {
	ProbeID      string `json:"probe_id,omitempty"`
	ProbeToken   string `json:"probe_token,omitempty"` // 长期凭证；明文落盘（0600），丢失可重接
	UserToken    string `json:"user_token,omitempty"`  // 用户 token（注册/重接用）
	Server       string `json:"server,omitempty"`      // host[:registryPort]
	IngestAddr   string `json:"ingest_addr,omitempty"` // 显式覆盖（默认由 Server 推导）
	RegistryAddr string `json:"registry_addr,omitempty"`
	Name         string `json:"name,omitempty"` // 机器业务名；空 = 注册时默认 hostname

	Archive archiveConfig `json:"archive"`
}

// configDir 返回配置目录（UserConfigDir/gt-agent；失败退回可执行文件同目录）。
func configDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		exe, e := os.Executable()
		if e == nil {
			return filepath.Dir(exe)
		}
		return "."
	}
	return filepath.Join(base, "gt-agent")
}

func configPath() string { return filepath.Join(configDir(), "probe.json") }

// loadAgentConfig 读 probe.json；不存在返回零值配置与 false。
// 无论文件是否存在，archive.enabled 未显式给出时一律默认开启（数据落盘留存）。
func loadAgentConfig() (*agentConfig, bool) {
	missing := &agentConfig{Archive: archiveConfig{Enabled: true, configured: true}}
	b, err := os.ReadFile(configPath())
	if err != nil {
		return missing, false
	}
	cfg := &agentConfig{}
	if err := json.Unmarshal(b, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "probe.json 解析失败（忽略该文件）: %v\n", err)
		return missing, false
	}
	// 升级前的老 probe.json 没有 archive 字段 → 默认为归档开启，避免数据发后即焚。
	cfg.Archive.defaultEnableOn()
	return cfg, true
}

// saveAgentConfig 原子写 probe.json（0600：里面存着探针长期凭证）。
func saveAgentConfig(cfg *agentConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := configDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

// suppliedConfig 是「本次下发」的身份与回连目标：命令行 flag / 下载产物里的
// config.embedded.json / 启动码领取结果。它们是同一样东西的三种来源。
type suppliedConfig struct {
	token    string
	server   string
	registry string
	ingest   string
}

// adopt 把下发的身份与回连目标写进 cfg。
//
// 为什么下发的身份要**盖掉** probe.json：probe.json 在 UserConfigDir（见 configDir），
// 重下一次探针、解压到新目录都不会删掉它。沿用上一次的 user_token 有两个后果：
//  1. 用 B 的身份下载的探针仍然归 A（平台里看到的归属是错的）；
//  2. 旧 probe_token 只对新身份/新服务端有效，首次回连必然被
//     PermissionDenied 打回，靠自愈重注册才连上——表现就是"总要失败一次"。
//
// 下发值为空表示这次没给该项，不动；与现有值相同也不动（避免无谓重注册）。
//
// 返回 written=true 表示 cfg 被改写（调用方需落盘 probe.json）。凭证是否作废是
// 另一件事：只有**顶掉了一个已有值**（换人 / 换服务端）才作废，首次把空字段
// 填上不该动凭证——那时还没有凭证，或凭证就是本次身份发的。
func (s suppliedConfig) adopt(cfg *agentConfig, reason string) (written bool) {
	invalid := false
	switch {
	case s.token == "" || s.token == cfg.UserToken:
		// 没给 / 和现有身份一样：不动。
	case cfg.UserToken == "":
		// 此前是匿名凭证（owner=local）：现在有了用户身份，旧凭证归属是错的。
		cfg.UserToken = s.token
		written = true
		invalid = cfg.ProbeID != "" || cfg.ProbeToken != ""
		if invalid {
			slog.Warn("probe identity supplied; dropping anonymous credentials", "reason", reason)
		}
	default:
		slog.Warn("probe identity replaced by supplied config; dropping stale credentials",
			"reason", reason,
			"old_token", tokenFingerprint(cfg.UserToken), "new_token", tokenFingerprint(s.token))
		cfg.UserToken = s.token
		written, invalid = true, true
	}
	// 回连轴：换了服务端，旧凭证那边不认。
	for _, a := range []struct {
		dst   *string
		val   string
		field string
	}{
		{&cfg.Server, s.server, "server"},
		{&cfg.RegistryAddr, s.registry, "registry_addr"},
		{&cfg.IngestAddr, s.ingest, "ingest_addr"},
	} {
		w, repl := adoptAddr(a.dst, a.val, a.field, reason)
		written, invalid = written || w, invalid || repl
	}
	if invalid {
		cfg.ProbeID, cfg.ProbeToken = "", ""
	}
	return written
}

// adoptAddr 写入单个回连字段。返回 (written, replaced)：
// written 表示值被写进了 cfg（含首次填空），replaced 表示顶掉了原有值。
func adoptAddr(dst *string, val, field, reason string) (written, replaced bool) {
	if val == "" || val == *dst {
		return false, false
	}
	replaced = *dst != ""
	if replaced {
		slog.Warn("probe server replaced by supplied config; dropping stale credentials",
			"reason", reason, "field", field, "old", *dst, "new", val)
	}
	*dst = val
	return true, replaced
}

// tokenFingerprint 只取凭证哈希前 8 位：日志里能分辨"换了哪个 token"，又不泄露明文。
func tokenFingerprint(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:8]
}
