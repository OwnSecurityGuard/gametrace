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
	"strings"
	"time"

	"gametrace/pkg/capture/agent/proto"
	"gametrace/pkg/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	req := &proto.RegisterProbeRequest{
		Hostname:     hostname,
		Os:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Version:      version.String(),
		Capabilities: []string{"pcap", "plugin_host"},
		Name:         cfg.Name,
		PrevProbeId:  prevID,
	}
	ack, err := client.RegisterProbe(regCtx, req)
	if err != nil && prevID != "" && isForeignProbeError(err) {
		// 旧记录还在但归别人所有（换人重装 / 服务端恢复过旧库）：带 prev_probe_id
		// 会被服务端拒绝，去掉它重来一次，按当前身份领一个新 id——否则这台机器
		// 会永远卡在"注册被拒 → 连不上"。
		slog.Warn("previous probe record belongs to another user; registering as a new probe",
			"prev_probe_id", prevID)
		req.PrevProbeId = ""
		ack, err = client.RegisterProbe(regCtx, req)
	}
	if err != nil {
		return false, fmt.Errorf("register probe: %w", err)
	}
	newID := ack.GetProbeId()
	if prevID != "" && newID != prevID {
		slog.Warn("probe re-registered with a new id (old record not reusable)",
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

// isForeignProbeError 判断注册被拒是否因为「旧记录归别的 owner」。
// 服务端原文见 pkg/probe/server.go RegisterProbe：
// "probe %s belongs to another user; revoke it first"。
func isForeignProbeError(err error) bool {
	if status.Code(err) != codes.PermissionDenied {
		return false
	}
	return strings.Contains(status.Convert(err).Message(), "belongs to another user")
}
