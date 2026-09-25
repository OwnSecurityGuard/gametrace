package main

// capture_binding_error_test.go —— 「绑了插件却接不上」必须端到端可见。
//
// 背景：会话绑定了一个没启动（或 owner 不符）的解码插件时，包照样落库、事件恒为 0、
// 解码错误也是 0 —— 平台唯一的痕迹是一行 Debug 日志，用户只能理解成"GameTrace 坏了"。
//
// 这里跑真实抓包链路（agent 会话 → captureTask），验证三件事：
//  1. 解码器长时间接不上会被记成 binding 类解码失败，停止后仍能查出原因，且点名到插件；
//  2. 按状态跳变计数（==1），不是每包一次 —— resolveDecoder 每包都会被调；
//  3. 宽限期内的正常情况不冤枉人：没配插件、以及"先开抓包、插件几秒后才起来"都不该有失败。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gametrace/pkg/capture/agent"
	gevent "gametrace/pkg/event"
	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/plugin"
	"gametrace/pkg/store"

	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"google.golang.org/grpc"
)

// unboundPluginName 是一个从不被注册的插件名：会话绑到它就等于"解析不到可用实例"。
const unboundPluginName = "never-registered-decoder"

// latePluginName 是宽限期后才注册的插件名（正常用法：先开抓包再启动插件）。
const latePluginName = "joins-late-decoder"

// silentDecoderServer 对每个包回 0 个事件且不算失败（只验证"接上了"这件事）。
type silentDecoderServer struct {
	pb.UnimplementedDecoderServer
	mu sync.Mutex
	n  int
}

func (d *silentDecoderServer) DecodeV2(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return nil
		}
		d.mu.Lock()
		d.n++
		d.mu.Unlock()
		_ = stream.Send(&pb.DecodeResponseV2{InputId: req.InputId, Done: true})
	}
}

func registerTunnelDecoder(t *testing.T, mgr *plugin.RegistryServer, name string, srv pb.DecoderServer) func() {
	t.Helper()
	manifest := "api_version: gt.decoder/v2\n" +
		"name: " + name + "\n" +
		"protocol: tcp\n" +
		"type: decoder\n" +
		"capabilities:\n" +
		"  decode: true\n" +
		"contract:\n" +
		"  name: gta.plugin\n" +
		"  version: 1\n"
	_, stop, err := mgr.RegisterInProcessTunnel(context.Background(), []byte(manifest), srv)
	if err != nil {
		t.Fatalf("注册解码器 %s 失败: %v", name, err)
	}
	return stop
}

// runSession 起一个 agent 会话、灌入真实包流，等 holdFor 后停止，
// 返回会话元数据与落库的解码失败分组。
func runSession(t *testing.T, pluginName string, port int, holdFor time.Duration) (*store.SessionMeta, []store.DecodeErrorRow) {
	t.Helper()

	workDir := t.TempDir()
	controlStore, err := store.NewControlStore(workDir + "/control.sqlite")
	if err != nil {
		t.Fatalf("NewControlStore: %v", err)
	}
	defer controlStore.Close()

	mgr := plugin.NewRegistryServer(10)
	hub := agent.NewHub()
	s := newPipelineService(workDir, controlStore, mgr, ":9091", "sqlite", "")
	s.SetAgentHub(hub)

	ctx := context.Background()
	res, err := s.StartSession(ctx, capturecontrol.StartSessionRequest{
		Agent:  true,
		Plugin: pluginName,
		Port:   port,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sessionID := res.SessionID

	// 等 agent source 订阅 hub，否则投进来的包会被整批丢弃。
	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(sessionID) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hub.Subscribers(sessionID) == 0 {
		t.Fatalf("agent source 未订阅 hub（session=%s）", sessionID)
	}

	for _, p := range generateScenario(time.Now()) {
		hub.Deliver(sessionID, []gevent.Packet{p})
	}
	time.Sleep(holdFor)
	if _, err := s.StopSession(ctx, sessionID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	meta, err := controlStore.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	st, err := store.NewSQLiteStoreReadOnly(res.DBPath)
	if err != nil {
		t.Fatalf("打开会话库失败: %v", err)
	}
	defer st.Close()
	groups, err := st.QueryDecodeErrorGroups(ctx, sessionID)
	if err != nil {
		t.Fatalf("QueryDecodeErrorGroups: %v", err)
	}
	return meta, groups
}

func findBinding(groups []store.DecodeErrorRow) *store.DecodeErrorRow {
	for i := range groups {
		if groups[i].Kind == "binding" {
			return &groups[i]
		}
	}
	return nil
}

func TestCaptureReportsUnboundPlugin(t *testing.T) {
	// 超过 bindingReportGrace，否则测的是宽限期而不是失败上报。
	meta, groups := runSession(t, unboundPluginName, 9250, 7*time.Second)

	if meta.RawPackets == 0 {
		t.Fatalf("RawPackets = 0：包没落库，后面的断言就没有意义了")
	}
	if meta.Events != 0 {
		t.Fatalf("Events = %d, want 0（没有任何解码实例时不该产出事件）", meta.Events)
	}
	// 前端 DecodeErrorPanel 只在 decode_errors > 0 时渲染：这一条决定"有没有提示"。
	if meta.DecodeErrors == 0 {
		t.Fatalf("DecodeErrors = 0：解码器没接上这件事仍然只在日志里，界面看不到任何痕迹")
	}

	binding := findBinding(groups)
	if binding == nil {
		t.Fatalf("没有 binding 分组（%+v）", groups)
	}
	if !strings.Contains(binding.Template, unboundPluginName) {
		t.Errorf("Template = %q, want 点名插件 %s", binding.Template, unboundPluginName)
	}
	if binding.Count != 1 {
		t.Errorf("Count = %d, want 1：应按状态跳变记一次，不是每包一次", binding.Count)
	}
	if binding.SampleRawID != "" {
		t.Errorf("SampleRawID = %q, want 空：这是会话级状态问题，不属于某个包", binding.SampleRawID)
	}
}

// TestCaptureSilentWhenPluginUnset 保证"只抓包不解码"这个合法模式不被记成失败。
func TestCaptureSilentWhenPluginUnset(t *testing.T) {
	meta, groups := runSession(t, "", 9251, 7*time.Second)

	if meta.RawPackets == 0 {
		t.Fatalf("RawPackets = 0：包没落库，后面的断言就没有意义了")
	}
	if meta.DecodeErrors != 0 {
		t.Fatalf("DecodeErrors = %d, want 0：未指定插件是「只抓包」的合法模式，不该算解码失败", meta.DecodeErrors)
	}
	if g := findBinding(groups); g != nil {
		t.Fatalf("出现了 binding 分组 %q：未指定插件时不该报", g.Template)
	}
}

// TestNoBindingErrorWhenPluginJoinsLate 保证宽限期真的在起作用：
// 「先开始抓包、过一会儿才启动插件」是正常用法，不能每个会话都背一条失败。
func TestNoBindingErrorWhenPluginJoinsLate(t *testing.T) {
	workDir := t.TempDir()
	controlStore, err := store.NewControlStore(workDir + "/control.sqlite")
	if err != nil {
		t.Fatalf("NewControlStore: %v", err)
	}
	defer controlStore.Close()

	mgr := plugin.NewRegistryServer(10)
	hub := agent.NewHub()
	s := newPipelineService(workDir, controlStore, mgr, ":9091", "sqlite", "")
	s.SetAgentHub(hub)

	ctx := context.Background()
	res, err := s.StartSession(ctx, capturecontrol.StartSessionRequest{
		Agent:  true,
		Plugin: latePluginName,
		Port:   9252,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sessionID := res.SessionID

	// 会话先跑起来（此时插件还没有），抓到包；宽限期内插件才注册。
	waitDeadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(sessionID) == 0 && time.Now().Before(waitDeadline) {
		time.Sleep(20 * time.Millisecond)
	}
	for _, p := range generateScenario(time.Now()) {
		hub.Deliver(sessionID, []gevent.Packet{p})
	}
	time.Sleep(time.Second)
	stop := registerTunnelDecoder(t, mgr, latePluginName, &silentDecoderServer{})
	defer stop()

	time.Sleep(3 * time.Second)
	if _, err := s.StopSession(ctx, sessionID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	meta, err := controlStore.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if meta.DecodeErrors != 0 {
		t.Fatalf("DecodeErrors = %d, want 0：插件在宽限期内接上了，不该留下解码失败", meta.DecodeErrors)
	}
}
