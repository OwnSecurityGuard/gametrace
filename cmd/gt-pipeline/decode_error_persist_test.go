package main

// decode_error_persist_test.go —— 「解码失败原因」端到端护栏。
//
// 背景：前端过去只能看到「解码失败 N 次」这一个数字，原因全在日志里；更糟的是
// 插件主动报的错（r.Error）在 dispatcher 里被静默 continue，连计数都没有。
//
// 本测试跑真实抓包链路（agent 会话 → captureTask → dispatcher → 插件），用一个人为
// 让每个包都失败的解码器，验证三件事：
//  1. 失败被采集并落库（会话停止后还能查）；
//  2. 失败文本只差数字时被归并成**一组** —— 这正是「错误太多」场景下不淹掉界面的依据；
//  3. 计数口径与失败总数一致（decode_errors 不再只是链路错误数）。

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

// failPluginName 是测试用「必失败解码器」的插件名。
const failPluginName = "fail-decoder-test"

// failingDecoderServer 对每个输入包都返回一个错误，错误文本里只带包序号 ——
// 因此归一化后所有错误模板相同，应当归并成一组。
type failingDecoderServer struct {
	pb.UnimplementedDecoderServer
	mu sync.Mutex
	n  int
}

func (d *failingDecoderServer) DecodeV2(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return nil // 客户端关闭流
		}
		d.mu.Lock()
		d.n++
		seq := d.n
		d.mu.Unlock()

		_ = stream.Send(&pb.DecodeResponseV2{
			InputId: req.InputId,
			Error:   fmt.Sprintf("unsupported frame magic at byte %d", seq*13),
		})
		_ = stream.Send(&pb.DecodeResponseV2{InputId: req.InputId, Done: true})
	}
}

// startFailingDecoder 注册一个必失败的解码器，返回停止函数。
// 注册流程与 sim_decoder.go 完全一致（同样的 manifest 校验与可达性校验）。
func startFailingDecoder(t *testing.T, mgr *plugin.RegistryServer) func() {
	t.Helper()
	dir, err := os.MkdirTemp("", "fail-decoder")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "decoder.sock")
	_ = os.Remove(sock)
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterDecoderServer(srv, &failingDecoderServer{})
	go func() { _ = srv.Serve(lis) }()

	manifest := "api_version: gt.decoder/v2\n" +
		"name: " + failPluginName + "\n" +
		"protocol: tcp\n" +
		"type: decoder\n" +
		"capabilities:\n" +
		"  decode: true\n" +
		"contract:\n" +
		"  name: gta.plugin\n" +
		"  version: 1\n"
	if _, err := mgr.Register(context.Background(), &pb.RegisterRequest{
		SocketPath: "unix:" + sock,
		Manifest:   []byte(manifest),
	}); err != nil {
		srv.Stop()
		_ = os.RemoveAll(dir)
		t.Fatalf("注册必失败解码器失败: %v", err)
	}
	return func() {
		srv.Stop()
		_ = os.Remove(sock)
		_ = os.RemoveAll(dir)
	}
}

func TestCapturePersistsDecodeErrorGroups(t *testing.T) {
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
	defer startFailingDecoder(t, mgr)()

	ctx := context.Background()
	res, err := s.StartSession(ctx, capturecontrol.StartSessionRequest{
		Agent:  true,
		Plugin: failPluginName,
		Port:   9250,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sessionID := res.SessionID

	// 等 agent source 完成 hub 订阅，否则投递进来的包会被整批丢弃。
	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(sessionID) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hub.Subscribers(sessionID) == 0 {
		t.Fatalf("agent source 未订阅 hub（session=%s）", sessionID)
	}

	pkts := generateScenario(time.Now())
	for _, p := range pkts {
		hub.Deliver(sessionID, []gevent.Packet{p})
	}
	// 等解码流水线跑完并落库（管线每秒 flush，解码另有一跳延迟）。
	time.Sleep(4 * time.Second)
	if _, err := s.StopSession(ctx, sessionID); err != nil {
		t.Fatalf("StopSession: %v", err)
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
	if len(groups) == 0 {
		t.Fatalf("没有落库任何解码失败分组（投递了 %d 个包，插件对每个都报错）", len(pkts))
	}

	// 核心诉求：错误再多，只要「形状」相同就只占一组。
	if len(groups) != 1 {
		t.Fatalf("分组数 = %d, want 1：错误文本只差数字，应被归一化成同一模板（%+v）",
			len(groups), groups)
	}
	g := groups[0]
	if g.Kind != "plugin" {
		t.Errorf("Kind = %q, want plugin（这些是插件主动报的错）", g.Kind)
	}
	if g.Template != "unsupported frame magic at byte <n>" {
		t.Errorf("Template = %q, want 归一化模板", g.Template)
	}
	if g.Count == 0 {
		t.Error("Count = 0：失败次数没有被累加")
	}
	if g.Sample == "" || g.SampleRawID == "" {
		t.Error("分组缺少样本或代表包：没有这两样就没法排查")
	}
	t.Logf("落库 %d 组；同类失败 %d 次；样本=%q 代表包=%s",
		len(groups), g.Count, g.Sample, g.SampleRawID)
}
