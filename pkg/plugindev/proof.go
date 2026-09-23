package plugindev

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 本文件实现 validated proof 的方案 B 持久化（见接入复盘文档 §14）：
// Runtime Plane（gt-pipeline verify）把 proof 写盘，Developer Plane
//（gt-mcp 内嵌 server / 独立 gt-plugin-dev）从磁盘读回。两平面共享
// <workdir>/plugins 目录（docker compose 下同为 /data/plugins），因此
// 进程内 Tracker 丢失（跨进程、重启）不再导致 validated 状态消失。

// validationProofFile 是 validation proof 的磁盘持久化格式
//（plugins/<name>/.gametrace/validation.json）。
type validationProofFile struct {
	VerifyRunID string `json:"verify_run_id"`
	SessionID   string `json:"session_id"`
	Verdict     string `json:"verdict"`
	At          string `json:"at"`
}

// proofDir 返回 <root>/<name>/.gametrace 目录。
func proofDir(root, name string) string {
	return filepath.Join(root, name, ".gametrace")
}

// proofPath 返回验证证明文件路径。
func proofPath(root, name string) string {
	return filepath.Join(proofDir(root, name), "validation.json")
}

// PersistValidation 把 validated proof 持久化到
// plugins/<name>/.gametrace/validation.json。仅 verify 判 pass 时调用；
// 与进程内 Tracker 各自独立，读取方（Developer Plane status）查 Tracker
// 未命中时落盘兜底。
func PersistValidation(root, name string, p *ValidatedProof) error {
	if root == "" || name == "" || p == nil {
		return fmt.Errorf("persist validation: root/name/proof required")
	}
	if err := os.MkdirAll(proofDir(root, name), 0o755); err != nil {
		return fmt.Errorf("persist validation: mkdir: %w", err)
	}
	b, err := json.MarshalIndent(validationProofFile{
		VerifyRunID: p.VerifyRunID,
		SessionID:   p.SessionID,
		Verdict:     p.Verdict,
		At:          p.At.Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("persist validation: marshal: %w", err)
	}
	tmp := proofPath(root, name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("persist validation: write: %w", err)
	}
	return os.Rename(tmp, proofPath(root, name))
}

// LoadValidation 从磁盘读取 validated proof；文件不存在或损坏返回 nil。
func LoadValidation(root, name string) *ValidatedProof {
	if root == "" || name == "" {
		return nil
	}
	b, err := os.ReadFile(proofPath(root, name))
	if err != nil {
		return nil
	}
	var f validationProofFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil
	}
	p := &ValidatedProof{
		VerifyRunID: f.VerifyRunID,
		SessionID:   f.SessionID,
		Verdict:     f.Verdict,
	}
	if t, err := time.Parse(time.RFC3339, f.At); err == nil {
		p.At = t
	}
	return p
}

// ClearValidation 删除磁盘上的 validated proof（build 成功、非 pass verify 时）。
// 删除整个 .gametrace 目录（该目录仅承载本 proof；退化失败时什么都不做）。
func ClearValidation(root, name string) {
	if root == "" || name == "" {
		return
	}
	_ = os.RemoveAll(proofDir(root, name))
}