package main

// register.go：探针注册（AgentControl.RegisterProbe）。
// claim 启动码 → 换发长期凭证（probe_id + probe_token）→ 落盘 probe.json。
// 已有凭证直接复用；凭证被吊销（服务端拒绝）时清除本地凭证，等下次带用户 token 重接。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"gametrace/pkg/capture/agent/proto"
	"gametrace/pkg/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// ensureRegistered 确保探针已注册：有凭证直接返回；无凭证且有用户 token 时注册。
// 两者皆无（匿名单机）返回 false（远端控制禁用，本地控制面照常可用）。
func ensureRegistered(ctx context.Context, cfg *agentConfig, ingestAddr string) (bool, error) {
	if cfg.ProbeID != "" && cfg.ProbeToken != "" {
		return true, nil
	}
	if cfg.UserToken == "" {
		return false, nil
	}
	return registerProbe(ctx, cfg, ingestAddr, "")
}

// registerProbe 调 RegisterProbe 换发凭证并落盘。prevID 非空时带上 prev_probe_id：
// 服务端若还有该探针记录且 owner 一致则覆盖换发（保持 probe_id），
// 记录已丢（服务端存储重建/换库）则当作新注册发新 probe_id。
// 成功后 cfg.ProbeID/ProbeToken 已更新并写回 probe.json。
func registerProbe(ctx context.Context, cfg *agentConfig, ingestAddr, prevID string) (bool, error) {
	conn, err := grpc.NewClient("passthrough:///"+ingestAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, err
	}
	defer conn.Close()

	regCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	regCtx = metadata.AppendToOutgoingContext(regCtx, "authorization", "Bearer "+cfg.UserToken)

	hostname, _ := os.Hostname()
	client := proto.NewAgentControlClient(conn)
	ack, err := client.RegisterProbe(regCtx, &proto.RegisterProbeRequest{
		Hostname:     hostname,
		Os:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Version:      version.String(),
		Capabilities: []string{"pcap", "plugin_host"},
		Name:         cfg.Name,
		PrevProbeId:  prevID,
	})
	if err != nil {
		return false, fmt.Errorf("register probe: %w", err)
	}
	newID := ack.GetProbeId()
	if prevID != "" && newID != prevID {
		slog.Warn("probe re-registered with new id (old record lost server-side)",
			"old_probe_id", prevID, "new_probe_id", newID)
	}
	cfg.ProbeID = newID
	cfg.ProbeToken = ack.GetProbeToken()
	if err := saveAgentConfig(cfg); err != nil {
		return false, fmt.Errorf("save probe credentials: %w", err)
	}
	slog.Info("probe registered", "probe_id", cfg.ProbeID)
	return true, nil
}
