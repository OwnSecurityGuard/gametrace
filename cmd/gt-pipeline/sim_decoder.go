package main

// sim_decoder.go —— 模拟器配套的「真实解码插件」与合成场景生成器。
//
// 设计意图（高工 2026-09-10）：写一个测试程序，包含「探针 + 解码」，
// 探针上报虚拟数据、解码器相应地把它们解成模拟事件，用于喂给 webui 做端到端验证。
// 这里的解码器实现的是 gRPC DecodeV2 契约（与任何正式插件完全一致），
// 由 in-process 的 plugin.RegistryServer 托管；模拟器把合成帧当作探针流量
// 经 hub 投递进抓包会话，解码器真实地把它们变成带 _state_changes 的事件，
// 落库后 webui 的「状态变更」三视图就能看到真实可信的数据。
//
// 合成协议（sim_game）：每条帧是一个 JSON 对象，描述一次操作里某个实体的若干字段变更。
// 探针（模拟器）负责按时间线产出这些帧；本文件只负责把帧解成事件。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	gevent "gametrace/pkg/event"
	"gametrace/pkg/plugin"

	sdkEvent "github.com/OwnSecurityGuard/gt-plugin-sdk/event"
	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
	"google.golang.org/grpc"
)

// simPluginName 是注册到注册表的插件名；模拟器启动会话时 Plugin 必须填它。
const simPluginName = "sim-game"

// simFrame 是探针上报的合成协议帧（JSON 线格式）。
type simFrame struct {
	CID     string      `json:"cid"`     // 关联键：把一次操作的 请求/响应/推送 归为一组
	Dir     string      `json:"dir"`     // c2s（请求）| s2c（响应）| push（推送）
	Msg     string      `json:"msg"`     // 操作 / 消息名，如 CastSkill
	Entity  string      `json:"entity"`  // "Type:id"，如 Player:1001
	Changes []simChange `json:"changes"` // 本次帧造成的字段变更
}

// simChange 是单个字段变更描述。
type simChange struct {
	Path   string `json:"path"`
	Op     string `json:"op"` // set | merge | delete
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// simDecoderServer 实现 pb.DecoderServer：把合成帧解成事件。
type simDecoderServer struct {
	pb.UnimplementedDecoderServer
}

// DecodeV2 实现解码双向流：每个请求解成一条带 _state_changes 的事件。
//
// 关键契约（与所有正式插件一致，见 pkg/decode/dispatcher.go 的 convertResultsToEvents）：
// 一条输入必须「先发一条 Done=false 的载荷响应，再发一条 Done=true 的终止响应」。
// dispatcher 只会把 Done=false 的响应累积进结果集，收到 Done=true 才转换为事件；
// 把 payload 直接塞进 Done=true 那条响应会导致结果为空（曾踩过这个坑）。
func (simDecoderServer) DecodeV2(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2]) error {
	// lastRequestInput 记录本流最近一个 c2s 请求的 InputId，供其后紧跟的 s2c
	// 响应设置 CausationInputID（形成请求↔响应配对，webui 才能并排展示两端）。
	// 单流内按发送顺序处理，无需加锁。
	lastRequestInput := ""
	for {
		req, err := stream.Recv()
		if err != nil {
			return err // 流关闭或出错，结束
		}
		if err := decodeSimFrame(stream, req, &lastRequestInput); err != nil {
			_ = stream.Send(&pb.DecodeResponseV2{InputId: req.InputId, Done: true, Error: err.Error()})
		}
	}
}

// decodeSimFrame 把单个 DecodeRequest 的 Payload（原始合成帧字节）解成事件并发回。
// 按 dispatcher 契约发两条响应：载荷（Done=false）+ 终止（Done=true）。
func decodeSimFrame(stream grpc.BidiStreamingServer[pb.DecodeRequest, pb.DecodeResponseV2], req *pb.DecodeRequest, lastRequestInput *string) error {
	var fr simFrame
	if err := json.Unmarshal(req.Payload, &fr); err != nil {
		return fmt.Errorf("解析合成帧失败: %w", err)
	}
	if fr.Entity == "" || fr.Msg == "" {
		return fmt.Errorf("合成帧缺少 entity/msg")
	}
	// 实体键 "Type:id" → subject_type / subject_id
	subjectType, subjectID := "Entity", fr.Entity
	if i := strings.IndexByte(fr.Entity, ':'); i >= 0 {
		subjectType, subjectID = fr.Entity[:i], fr.Entity[i+1:]
	}

	role, isPush, direction := classifyDir(fr.Dir)
	meta := map[string]any{
		"direction": direction,
		"msg_name":  fr.Msg,
		"role":      role,
		"is_push":   isPush,
	}

	// 配对因果：c2s 帧是请求（记录其 InputId）；其后的 s2c 帧是响应，把
	// CausationInputID 指向那个请求，宿主 dispatcher 解析成响应侧 causation_id。
	// 这样 webui 的事件表才能把请求/响应识别为 pair 并左右并排展示。
	causeInput := ""
	switch fr.Dir {
	case "c2s":
		*lastRequestInput = req.InputId
	case "s2c":
		causeInput = *lastRequestInput
	}

	sc := make([]any, 0, len(fr.Changes))
	for _, c := range fr.Changes {
		sc = append(sc, map[string]any{
			"subject_type": subjectType,
			"subject_id":   subjectID,
			"op":           c.Op,
			"path":         c.Path,
			"before":       c.Before,
			"after":        c.After,
			"version":      1,
		})
	}

	payload := map[string]any{
		"flow_id":        req.FlowId,
		"correlation_id": fr.CID,
		"msg_name":       fr.Msg,
		"role":           role,
		"is_push":        isPush,
		"entity":         fr.Entity,
		"entity_type":    subjectType,
		"entity_id":      subjectID,
		"change_count":   len(fr.Changes),
		"_meta":          meta,
		"_state_changes": sc,
	}

	draft := sdkEvent.Draft{
		Type:             "sim.event",
		SchemaRef:        "sim_game.event.v1",
		Value:            sdkEvent.ValueFromMap(payload),
		CorrelationKey:   fr.CID,
		CausationInputID: causeInput,
	}
	// 载荷响应：Done 保持 false，让 dispatcher 把它累积进结果集。
	resp, err := draft.ToResponse(req.InputId)
	if err != nil {
		return err
	}
	if err := stream.Send(resp); err != nil {
		return err
	}
	// 终止响应：input-id-echo + Done=true，dispatcher 据此把前面的载荷响应转成事件。
	return stream.Send(&pb.DecodeResponseV2{InputId: req.InputId, Done: true})
}

// classifyDir 把合成协议的 dir 字段映射成 角色 / 是否推送 / 方向。
func classifyDir(dir string) (role string, isPush bool, direction string) {
	switch dir {
	case "c2s":
		return "request", false, "client_to_server"
	case "s2c":
		return "response", false, "server_to_client"
	default: // push
		return "push", true, "server_to_client"
	}
}

// startSimDecoder 在 unix socket 上起一个真实的解码 gRPC 服务，并注册到 in-process 注册表。
// 返回 socket 路径与停止函数。注册表会做一次可达性拨号，所以必须先 Listen 再 Register。
func startSimDecoder(mgr *plugin.RegistryServer) (string, func(), error) {
	dir, err := os.MkdirTemp("", "sim-decoder")
	if err != nil {
		return "", nil, err
	}
	sock := filepath.Join(dir, "decoder.sock")
	_ = os.Remove(sock)
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return "", nil, err
	}
	srv := grpc.NewServer()
	pb.RegisterDecoderServer(srv, simDecoderServer{})
	go func() { _ = srv.Serve(lis) }()

	// 声明 schema（id sim_game.event → 线上串 sim_game.event.v1），使 dispatcher 不再
	// 把事件降级成 unknown.v1；同时让 webui 的状态变更视图能按 schema 正常归类。
	// 字段类型严格照搬真实插件（int64/bool/string），避免 manifest 校验失败。
	manifest := "api_version: gt.decoder/v2\n" +
		"name: " + simPluginName + "\n" +
		"protocol: tcp\n" +
		"type: decoder\n" +
		"capabilities:\n" +
		"  decode: true\n" +
		"  schema: true\n" +
		"contract:\n" +
		"  name: gta.plugin\n" +
		"  version: 1\n" +
		"schemas:\n" +
		"  - id: sim_game.event\n" +
		"    version: 1\n" +
		"    name: Simulated game event\n" +
		"    description: Synthetic event emitted by the test probe+decoder harness for webui validation.\n" +
		"    strict: false\n" +
		"    fields:\n" +
		"      entity_type:    { type: string, queryable: true }\n" +
		"      entity_id:      { type: string, queryable: true }\n" +
		"      msg_name:       { type: string, queryable: true }\n" +
		"      role:           { type: string, queryable: true }\n" +
		"      is_push:        { type: bool }\n" +
		"      change_count:   { type: int64, queryable: true }\n" +
		"      correlation_id: { type: string, optional: true }\n"
	// Register 会拨号校验可达性，因此解码服务必须先 Listen。
	// SocketPath 必须带 unix: 前缀：dialTarget 对裸 Windows 路径（无 '/'）会误判成 tcp。
	socketTarget := "unix:" + sock
	if _, err := mgr.Register(context.Background(), &pb.RegisterRequest{
		SocketPath: socketTarget,
		Manifest:   []byte(manifest),
	}); err != nil {
		srv.Stop()
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("注册 sim-game 解码插件失败: %w", err)
	}
	stop := func() {
		srv.Stop()
		_ = os.Remove(sock)
		_ = os.RemoveAll(dir)
	}
	return sock, stop, nil
}

// generateScenario 产出一段「多人在线游戏对战」的合成抓包时间线：
// 3 名玩家登录、移动、对 2 只怪物轮番释放技能（怪物掉血/阵亡）、拾取掉落、升级、最后登出。
// 每条帧经 hub 投递后即被真实解码，变成带 before/after 的实体状态变更。
// 时间戳按帧递增 ~400ms 铺开，使 webui 的「按时间」视图能看到真实的密度与顺序。
func generateScenario(base time.Time) []gevent.Packet {
	var pkts []gevent.Packet
	offset := 0
	next := func() time.Time {
		offset += 400
		return base.Add(time.Duration(offset) * time.Millisecond)
	}

	state := map[string]map[string]any{
		"Player:1001": {"hp": 100, "level": 1, "x": 0, "y": 0, "mana": 50, "gold": 0, "online": false},
		"Player:1002": {"hp": 120, "level": 1, "x": 100, "y": 0, "mana": 60, "gold": 10, "online": false},
		"Player:1003": {"hp": 90, "level": 2, "x": 50, "y": 50, "mana": 40, "gold": 5, "online": false},
		"Mob:2001":    {"hp": 200, "level": 3, "x": 20, "y": 20, "alive": true},
		"Mob:2002":    {"hp": 150, "level": 2, "x": 30, "y": 30, "alive": true},
	}

	var cid int
	newCID := func() string { cid++; return fmt.Sprintf("op-%d", cid) }

	mut := func(entity, path, op string, after any) simChange {
		before := state[entity][path]
		state[entity][path] = after
		return simChange{Path: path, Op: op, Before: before, After: after}
	}

	emit := func(dir, msg, entity string, changes []simChange) {
		fr := simFrame{CID: newCID(), Dir: dir, Msg: msg, Entity: entity, Changes: changes}
		b, err := json.Marshal(fr)
		if err != nil {
			slog.Warn("marshal sim frame", "error", err)
			return
		}
		pkts = append(pkts, gevent.Packet{
			ID:        fmt.Sprintf("pkt-%d", len(pkts)),
			Raw:       b,
			LinkType:  gevent.LinkTypeEthernet,
			Protocol:  "tcp",
			Timestamp: next(),
			Src:       netip.MustParseAddrPort("10.0.0.1:1111"),
			Dst:       netip.MustParseAddrPort("10.0.0.2:9250"),
		})
	}

	login := func(p string) {
		emit("c2s", "Login", p, []simChange{mut(p, "online", "set", true)})
		emit("s2c", "LoginAck", p, []simChange{
			{Path: "hp", Op: "set", Before: nil, After: state[p]["hp"]},
			{Path: "level", Op: "set", Before: nil, After: state[p]["level"]},
			{Path: "gold", Op: "set", Before: nil, After: state[p]["gold"]},
		})
		emit("push", "PlayerJoined", p, []simChange{mut(p, "online", "set", true)})
	}

	move := func(p string, x, y int) {
		emit("c2s", "Move", p, []simChange{mut(p, "x", "set", x), mut(p, "y", "set", y)})
		emit("s2c", "MoveAck", p, []simChange{mut(p, "x", "set", x), mut(p, "y", "set", y)})
	}

	cast := func(p, m string, manaCost int) {
		emit("c2s", "CastSkill", p, []simChange{mut(p, "mana", "set", state[p]["mana"].(int)-manaCost)})
		emit("s2c", "CastAck", p, nil)
		newHP := state[m]["hp"].(int) - 50
		if newHP <= 0 {
			newHP = 0
			emit("push", "Damage", m, []simChange{mut(m, "hp", "set", newHP), mut(m, "alive", "set", false)})
			emit("push", "MobDied", m, []simChange{mut(m, "alive", "set", false)})
		} else {
			emit("push", "Damage", m, []simChange{mut(m, "hp", "set", newHP)})
		}
	}

	loot := func(p string, gold int) {
		emit("push", "Loot", p, []simChange{mut(p, "gold", "set", state[p]["gold"].(int)+gold)})
	}

	levelup := func(p string) {
		emit("push", "LevelUp", p, []simChange{
			mut(p, "level", "set", state[p]["level"].(int)+1),
			mut(p, "hp", "set", state[p]["hp"].(int)+50),
			mut(p, "mana", "set", 100),
		})
	}

	logout := func(p string) {
		emit("c2s", "Logout", p, []simChange{mut(p, "online", "set", false)})
		emit("s2c", "LogoutAck", p, nil)
		emit("push", "PlayerLeft", p, []simChange{mut(p, "online", "set", false)})
	}

	// 三个玩家依次登录
	for _, p := range []string{"Player:1001", "Player:1002", "Player:1003"} {
		login(p)
	}

	// 玩家 1001 走位 + 把 Mob:2001 打到死，期间拾取掉落、升级
	move("Player:1001", 10, 5)
	move("Player:1001", 15, 8)
	for i := 0; i < 5; i++ {
		cast("Player:1001", "Mob:2001", 20)
	}
	loot("Player:1001", 25)
	levelup("Player:1001")

	// 玩家 1002 走位 + 把 Mob:2002 打到死
	move("Player:1002", 90, 10)
	move("Player:1002", 80, 12)
	for i := 0; i < 4; i++ {
		cast("Player:1002", "Mob:2002", 20)
	}

	// 玩家 1003 走位 + 补刀残留
	move("Player:1003", 60, 40)
	move("Player:1003", 55, 38)
	cast("Player:1003", "Mob:2002", 15)

	// 玩家 1001 再升一级、1002 拾取
	levelup("Player:1001")
	loot("Player:1002", 15)

	// 全部登出
	for _, p := range []string{"Player:1001", "Player:1002", "Player:1003"} {
		logout(p)
	}

	return pkts
}
