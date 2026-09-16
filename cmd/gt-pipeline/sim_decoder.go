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

	sdkEvent "github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
	"google.golang.org/grpc"
)

// simPluginName 是注册到注册表的插件名；模拟器启动会话时 Plugin 必须填它。
const simPluginName = "sim-game"

// simFrame 是探针上报的合成协议帧（JSON 线格式）。
//
// Entity 是帧级「主实体」：绝大多数帧只改一个实体，此时帧级 Entity 足够。
// 但真实协议里一次操作常常同时冲击多个实体（升级时等级、称号、战力、技能一起变），
// 因此每条 change 都可以用自己的 Entity 覆盖帧级值——**同一帧内的多条 change 共享
// 同一个 cid**，投影后就归在同一个「操作」组下，这才是「一次操作改变多个实体」的正确表达。
type simFrame struct {
	CID     string      `json:"cid"`     // 关联键：把一次操作的 请求/响应/推送 归为一组
	Dir     string      `json:"dir"`     // c2s（请求）| s2c（响应）| push（推送）
	Msg     string      `json:"msg"`     // 操作 / 消息名，如 CastSkill
	Entity  string      `json:"entity"`  // "Type:id"，如 Player:1001，帧内默认实体
	Changes []simChange `json:"changes"` // 本次帧造成的字段变更
}

// simChange 是单个字段变更描述。
type simChange struct {
	Entity string `json:"entity,omitempty"` // 可选：跨区分帧级多实体变更
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
	// 帧级实体作为默认值（"Type:id" → subject_type/subject_id）。
	frameType, frameID := splitEntity(fr.Entity)

	role, isPush, direction := classifyDir(fr.Dir)

	sc := make([]any, 0, len(fr.Changes))
	for _, c := range fr.Changes {
		subjectType, subjectID := frameType, frameID
		if c.Entity != "" {
			// 一次操作可以同时改动多个实体：每条 change 指定自己的归属实体。
			subjectType, subjectID = splitEntity(c.Entity)
		}
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

	business := simBusinessFields(fr, frameType, frameID)
	analysis := map[string]any{
		"entity":         fr.Entity,
		"entity_type":    frameType,
		"entity_id":      frameID,
		"change_count":   len(fr.Changes),
		"_state_changes": sc,
	}

	draft := sdkEvent.Draft{
		Type:             "sim.event",
		Value:            sdkEvent.ValueFromMap(business), // 纯业务 payload
		Meta:             sdkEvent.ValueFromMap(meta),     // direction/msg_name/role/is_push
		Analysis:         sdkEvent.ValueFromMap(analysis), // entity/_state_changes 等分析
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

// splitEntity 把 "Type:id" 拆成 subject_type / subject_id；无冒号时类型兜底为 "Entity"。
func splitEntity(ent string) (subjectType, subjectID string) {
	if i := strings.IndexByte(ent, ':'); i >= 0 {
		return ent[:i], ent[i+1:]
	}
	return "Entity", ent
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

// simBusinessFields 按消息名生成纯业务 payload 字段（供前端「业务数据」第一眼展示）。
//
// 跨实体的帧（如 LevelUp 同时改等级/称号/战力）只把「帧级主实体」自己的 after 值并进
// payload——把别家实体的值并进来会造出一个不存在的业务对象。
func simBusinessFields(fr simFrame, subjectType, subjectID string) map[string]any {
	pid := subjectID
	b := map[string]any{}
	setAfter := func(keys ...string) {
		for _, c := range fr.Changes {
			if c.Entity != "" && c.Entity != fr.Entity {
				continue
			}
			for _, k := range keys {
				if c.Path == k {
					b[k] = c.After
				}
			}
		}
	}
	switch fr.Msg {
	case "Login":
		b["playerId"] = pid
		b["username"] = "player_" + pid
		b["platform"] = "pc"
	case "SyncProfile":
		b["playerId"] = pid
		setAfter("level", "exp", "gold", "hp", "mana", "power_total", "title_id")
	case "LoginFinish":
		b["playerId"] = pid
		b["scene"] = "world_01"
	case "Move", "MoveAck":
		b["playerId"] = pid
		setAfter("x", "y")
	case "CastSkill":
		b["playerId"] = pid
		b["skillId"] = "skill_fireball"
		b["targetId"] = "Mob:2001"
		b["manaCost"] = 20
		setAfter("mana")
	case "CastAck":
		b["playerId"] = pid
		b["skillId"] = "skill_fireball"
	case "Damage":
		b["targetId"] = subjectType + ":" + pid
		b["damage"] = 50
		setAfter("hp")
	case "Loot":
		b["playerId"] = pid
		setAfter("gold")
	case "LevelUp":
		b["playerId"] = pid
		setAfter("level", "exp", "power_total", "title_id")
	case "Logout":
		b["playerId"] = pid
		b["reason"] = "client_quit"
	default:
		// 各类 Sync 消息（Skills/Equips/Skins/Pets/Heroes/Titles/Buffs/Power）走到这里：
		// payload 只标归属账号与本次下发的实体规模，明细在 _state_changes 里。
		b["playerId"] = pid
		if strings.HasPrefix(fr.Msg, "Sync") {
			b["sync_batch"] = fr.Msg
			b["entity_count"] = entityCountOf(fr)
		}
	}
	return b
}

// entityCountOf 统计一帧里涉及多少个不同实体。
func entityCountOf(fr simFrame) int {
	seen := map[string]bool{}
	for _, c := range fr.Changes {
		ent := c.Entity
		if ent == "" {
			ent = fr.Entity
		}
		seen[ent] = true
	}
	return len(seen)
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

	manifest := "api_version: gt.decoder/v2\n" +
		"name: " + simPluginName + "\n" +
		"protocol: tcp\n" +
		"type: decoder\n" +
		"capabilities:\n" +
		"  decode: true\n" +
		"contract:\n" +
		"  name: gta.plugin\n" +
		"  version: 1\n"
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

// generateScenario 产出一段「MMO 账号登录后同步角色档案 + 角色成长」的合成抓包时间线。
//
// 场景设计（对应高工 2026-09-16 的复现要求）：
//  1. 登录后服务端不是回一条 LoginAck，而是把整份角色档案连续推下来——
//     技能 / 装备 / 皮肤 / 宠物 / 英雄 / 称号 / buff / 战力，每类都是若干独立实体，
//     一次账号登录就有几百条字段变更，这是「批量同步」噪音的主体；
//  2. 一次人物升级会同时冲击多种实体：等级、经验、多项战力、称号更替、
//     新技能解锁、已有技能变强、宠物/英雄跟随升级、buff 叠加——
//     这些变更共享同一个 cid，投影后落在同一个「操作」组下；
//  3. 三个账号刻意做成三种体量（老玩家 / 中手 / 新号），让「按实体」视图有长尾。
//
// 每条帧经 hub 投递后即被真实解码；帧间隔 400ms，便于「按时间」视图看出密度层次。

// powerTypes 是固定 10 种战力类型（每种一个 Power 实体）。
var powerTypes = []string{"atk", "def", "hp", "crit", "crit_res", "hit", "dodge", "penetration", "tenacity", "heal"}

// equipSlots 是固定 6 个装备位（每种一个 Equip 实体）。
var equipSlots = []string{"weapon", "helmet", "armor", "ring", "boots", "amulet"}

// playerPaths 是 Player 本体的字段顺序（显式列出，避免 map 迭代顺序抖动）。
var playerPaths = []string{"online", "level", "exp", "gold", "hp", "mana", "power_total", "title_id", "x", "y"}

// playerProfile 描述一个登录账号的角色档案规模。
type playerProfile struct {
	id     string
	level  int
	skills int
	equips int
	skins  int
	pets   int
	heroes int
	titles int
	buffs  int
}

// catalog 是某账号下某一类实体的全部实例及其字段顺序。
// paths 有序很重要：map 迭代是随机的，字段顺序抖会让 UI 每次刷新都换个样子。
type catalog struct {
	typ   string
	label string // 同步消息名后缀：Skills → SyncSkills
	ids   []string
	paths []string
}

// simWorld 是模拟器自己的服务端状态表：entityKey(Type:id) → path → value。
// 宿主侧的 BaselineManager 另有基线且从不伪造 before；这里负责给出协议层面的
// 真实 before/after，让「全量下发（无 before）」与「真实变化（有 before）」区分开。
type simWorld struct{ state map[string]map[string]any }

func newSimWorld() *simWorld { return &simWorld{state: map[string]map[string]any{}} }

// init 写入实体初始状态，不产生变更（服务端本来就有这份数据）。
func (w *simWorld) init(ent string, fields map[string]any) {
	m := w.state[ent]
	if m == nil {
		m = map[string]any{}
		w.state[ent] = m
	}
	for k, v := range fields {
		m[k] = v
	}
}

// declare 产出一条「服务端全量下发」：只有 after，没有 before。
// 登录时服务端推整份档案就是这个形态——协议不关心旧值，宿主此刻也无基线。
func (w *simWorld) declare(ent, path string) simChange {
	return simChange{Entity: ent, Path: path, Op: "set", After: w.state[ent][path]}
}

// set 改成新值，返回带真实 before 的变更。
func (w *simWorld) set(ent, path string, after any) simChange {
	before := w.state[ent][path]
	w.state[ent][path] = after
	return simChange{Entity: ent, Path: path, Op: "set", Before: before, After: after}
}

// bump 给整型字段加增量。
func (w *simWorld) bump(ent, path string, delta int) simChange {
	cur, _ := w.state[ent][path].(int)
	return w.set(ent, path, cur+delta)
}

// buildProfile 生成一个账号的全部角色档案实体（写入初始状态），返回分类目录。
func buildProfile(p playerProfile, w *simWorld) []catalog {
	cats := make([]catalog, 0, 8)

	skill := catalog{typ: "Skill", label: "Skills", paths: []string{"unlocked", "level", "exp", "damage"}}
	for i := 1; i <= p.skills; i++ {
		ent := fmt.Sprintf("Skill:%s-skill%02d", p.id, i)
		w.init(ent, map[string]any{"unlocked": i <= 3, "level": 1, "exp": 0, "damage": 100 + i*10})
		skill.ids = append(skill.ids, ent)
	}
	cats = append(cats, skill)

	equip := catalog{typ: "Equip", label: "Equips", paths: []string{"owned", "level", "refine", "equipped"}}
	for i, slot := range equipSlots {
		if i >= p.equips {
			break
		}
		ent := fmt.Sprintf("Equip:%s-%s", p.id, slot)
		w.init(ent, map[string]any{"owned": true, "level": 1, "refine": 0, "equipped": i < 2})
		equip.ids = append(equip.ids, ent)
	}
	cats = append(cats, equip)

	skin := catalog{typ: "Skin", label: "Skins", paths: []string{"owned", "active"}}
	for i := 1; i <= p.skins; i++ {
		ent := fmt.Sprintf("Skin:%s-skin%02d", p.id, i)
		w.init(ent, map[string]any{"owned": i <= 3, "active": i == 1})
		skin.ids = append(skin.ids, ent)
	}
	cats = append(cats, skin)

	pet := catalog{typ: "Pet", label: "Pets", paths: []string{"level", "star", "exp", "active"}}
	for i := 1; i <= p.pets; i++ {
		ent := fmt.Sprintf("Pet:%s-pet%02d", p.id, i)
		w.init(ent, map[string]any{"level": 1, "star": 1, "exp": 0, "active": i == 1})
		pet.ids = append(pet.ids, ent)
	}
	cats = append(cats, pet)

	hero := catalog{typ: "Hero", label: "Heroes", paths: []string{"level", "star", "exp", "deployed"}}
	for i := 1; i <= p.heroes; i++ {
		ent := fmt.Sprintf("Hero:%s-hero%02d", p.id, i)
		w.init(ent, map[string]any{"level": 1, "star": 1, "exp": 0, "deployed": i == 1})
		hero.ids = append(hero.ids, ent)
	}
	cats = append(cats, hero)

	title := catalog{typ: "Title", label: "Titles", paths: []string{"owned", "active"}}
	for i := 1; i <= p.titles; i++ {
		ent := fmt.Sprintf("Title:%s-title%02d", p.id, i)
		w.init(ent, map[string]any{"owned": i <= 2, "active": i == 1})
		title.ids = append(title.ids, ent)
	}
	cats = append(cats, title)

	buff := catalog{typ: "Buff", label: "Buffs", paths: []string{"stack", "remaining_ms"}}
	for i := 1; i <= p.buffs; i++ {
		ent := fmt.Sprintf("Buff:%s-buff%02d", p.id, i)
		w.init(ent, map[string]any{"stack": 0, "remaining_ms": 0})
		buff.ids = append(buff.ids, ent)
	}
	cats = append(cats, buff)

	power := catalog{typ: "Power", label: "Power", paths: []string{"value"}}
	for _, t := range powerTypes {
		ent := "Power:" + p.id + "-" + t
		w.init(ent, map[string]any{"value": 1000})
		power.ids = append(power.ids, ent)
	}
	cats = append(cats, power)

	return cats
}

// catByType 从账号目录里取某一类实体。
func catByType(cats []catalog, typ string) catalog {
	for _, c := range cats {
		if c.typ == typ {
			return c
		}
	}
	return catalog{}
}

// actorState 记录某个账号当前的成长进度（称号位、已解锁技能数、已挂 buff 数）。
type actorState struct {
	titleIdx int // 当前生效称号的 1-based 序号
	skillIdx int // 已解锁技能数量
	buffIdx  int // 已挂 buff 数量
}

func generateScenario(base time.Time) []gevent.Packet {
	var pkts []gevent.Packet
	offset := 0
	next := func() time.Time {
		offset += 400
		return base.Add(time.Duration(offset) * time.Millisecond)
	}

	w := newSimWorld()
	var cid int
	// emit 产出一条合成帧：Entity 恒为该账号的 Player（帧级主实体），
	// 跨实体的变更由每条 change 自带的 Entity 表达。
	emit := func(pid, dir, msg string, changes []simChange) {
		cid++
		fr := simFrame{CID: fmt.Sprintf("op-%d", cid), Dir: dir, Msg: msg, Entity: "Player:" + pid, Changes: changes}
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

	profiles := []playerProfile{
		{id: "1001", level: 42, skills: 10, equips: 6, skins: 10, pets: 10, heroes: 10, titles: 10, buffs: 10},
		{id: "1002", level: 21, skills: 5, equips: 3, skins: 4, pets: 3, heroes: 4, titles: 4, buffs: 4},
		{id: "1003", level: 3, skills: 2, equips: 1, skins: 1, pets: 1, heroes: 1, titles: 1, buffs: 1},
	}
	catsByPlayer := map[string][]catalog{}
	actors := map[string]*actorState{}

	w.init("Mob:2001", map[string]any{"hp": 200, "alive": true})
	w.init("Mob:2002", map[string]any{"hp": 150, "alive": true})

	// login：客户端请求登录 → 服务端连推八份数据视图 → 登录完成广播。
	login := func(p playerProfile) {
		pid := p.id
		pl := "Player:" + pid
		w.init(pl, map[string]any{
			"online": false, "level": p.level, "exp": 0, "gold": 1000 + p.level*10,
			"hp": 1000, "mana": 500, "power_total": 10000 + p.level*100,
			"title_id": "01", "x": 0, "y": 0,
		})
		cats := buildProfile(p, w)
		catsByPlayer[pid] = cats
		actors[pid] = &actorState{titleIdx: 1, skillIdx: 3}

		emit(pid, "c2s", "Login", []simChange{w.set(pl, "online", true)})

		// 档案本体 + 10 种战力，同属一份 profile 视图。
		profile := make([]simChange, 0, len(playerPaths)+len(powerTypes))
		for _, path := range playerPaths {
			profile = append(profile, w.declare(pl, path))
		}
		for _, ent := range catByType(cats, "Power").ids {
			profile = append(profile, w.declare(ent, "value"))
		}
		emit(pid, "s2c", "SyncProfile", profile)

		// 其余每类实体各一份数据视图。
		for _, c := range cats {
			if c.typ == "Power" {
				continue
			}
			cs := make([]simChange, 0, len(c.ids)*len(c.paths))
			for _, ent := range c.ids {
				for _, path := range c.paths {
					cs = append(cs, w.declare(ent, path))
				}
			}
			emit(pid, "s2c", "Sync"+c.label, cs)
		}

		// 登录完成广播：又同步一遍 online（真实协议里这种重复回声很常见）。
		emit(pid, "push", "LoginFinish", []simChange{w.set(pl, "online", true)})
	}

	// levelUp：一次升级同时冲击多种实体——等级 / 多项战力 / 称号更替 /
	// 技能解锁与增强 / 宠物英雄跟随 / buff 叠加，全部落在同一个 cid 下。
	levelUp := func(pid string) {
		pl := "Player:" + pid
		a := actors[pid]
		cats := catsByPlayer[pid]

		cs := []simChange{
			w.bump(pl, "level", 1),
			w.set(pl, "exp", 0),
			w.bump(pl, "power_total", 1200),
		}

		// 前 4 项战力维度随等级上涨
		for i, ent := range catByType(cats, "Power").ids {
			if i >= 4 {
				break
			}
			cs = append(cs, w.bump(ent, "value", 250+i*10))
		}

		// 称号更替：旧称号失效 → 新称号解锁并生效 → Player.title_id 跟着变
		titles := catByType(cats, "Title")
		if len(titles.ids) >= 2 && a.titleIdx <= len(titles.ids) {
			cs = append(cs, w.set(titles.ids[a.titleIdx-1], "active", false))
			if a.titleIdx < len(titles.ids) {
				nextTitle := titles.ids[a.titleIdx]
				cs = append(cs, w.set(nextTitle, "owned", true), w.set(nextTitle, "active", true))
				a.titleIdx++
				cs = append(cs, w.set(pl, "title_id", fmt.Sprintf("%02d", a.titleIdx)))
			}
		}

		// 技能：解锁一个新技能，并让全部已解锁技能升级、伤害提升
		skills := catByType(cats, "Skill")
		if a.skillIdx < len(skills.ids) {
			cs = append(cs, w.set(skills.ids[a.skillIdx], "unlocked", true))
			a.skillIdx++
		}
		for i := 0; i < a.skillIdx && i < len(skills.ids); i++ {
			cs = append(cs, w.bump(skills.ids[i], "level", 1), w.bump(skills.ids[i], "damage", 25))
		}

		// 宠物 / 英雄跟随升级
		if pets := catByType(cats, "Pet"); len(pets.ids) > 0 {
			cs = append(cs, w.bump(pets.ids[0], "level", 1), w.bump(pets.ids[0], "exp", 100))
		}
		if heroes := catByType(cats, "Hero"); len(heroes.ids) > 0 {
			cs = append(cs, w.bump(heroes.ids[0], "level", 1))
		}

		// buff 叠加
		buffs := catByType(cats, "Buff")
		if a.buffIdx < len(buffs.ids) {
			ent := buffs.ids[a.buffIdx]
			cs = append(cs, w.set(ent, "stack", 1), w.set(ent, "remaining_ms", 60000))
			a.buffIdx++
		}

		emit(pid, "push", "LevelUp", cs)
	}

	move := func(pid string, x, y int) {
		pl := "Player:" + pid
		emit(pid, "c2s", "Move", []simChange{w.set(pl, "x", x), w.set(pl, "y", y)})
		emit(pid, "s2c", "MoveAck", []simChange{w.set(pl, "x", x), w.set(pl, "y", y)})
	}

	cast := func(pid, mob string) {
		emit(pid, "c2s", "CastSkill", []simChange{w.bump("Player:"+pid, "mana", -20)})
		emit(pid, "s2c", "CastAck", nil)
		hp := w.state[mob]["hp"].(int) - 60
		if hp < 0 {
			hp = 0
		}
		cs := []simChange{w.set(mob, "hp", hp)}
		if hp == 0 {
			cs = append(cs, w.set(mob, "alive", false))
		}
		emit(pid, "push", "Damage", cs)
	}

	loot := func(pid string, gold int) {
		emit(pid, "push", "Loot", []simChange{w.bump("Player:"+pid, "gold", gold)})
	}

	// logout：下线广播把在场实体一次性置为失效——又一条典型的大批量同步。
	logout := func(pid string) {
		pl := "Player:" + pid
		emit(pid, "c2s", "Logout", []simChange{w.set(pl, "online", false)})
		emit(pid, "s2c", "LogoutAck", nil)

		var cs []simChange
		for _, c := range catsByPlayer[pid] {
			switch c.typ {
			case "Pet":
				for _, ent := range c.ids {
					cs = append(cs, w.set(ent, "active", false))
				}
			case "Hero":
				for _, ent := range c.ids {
					cs = append(cs, w.set(ent, "deployed", false))
				}
			case "Buff":
				for _, ent := range c.ids {
					if stack, _ := w.state[ent]["stack"].(int); stack > 0 {
						cs = append(cs, w.set(ent, "remaining_ms", 0), w.set(ent, "stack", 0))
					}
				}
			}
		}
		emit(pid, "push", "PlayerLeft", cs)
	}

	// 三名玩家依次登录：服务端把整份角色档案连续推下来
	for _, p := range profiles {
		login(p)
	}

	// 人物成长：老玩家连升 3 级，另外两个账号各升 1 级
	levelUp("1001")
	levelUp("1001")
	levelUp("1001")
	levelUp("1002")
	levelUp("1003")

	// 一小段真实业务动作，与上面的批量同步形成对照
	move("1001", 10, 5)
	cast("1001", "Mob:2001")
	cast("1001", "Mob:2001")
	cast("1001", "Mob:2001")
	cast("1001", "Mob:2001")
	loot("1001", 25)
	move("1002", 90, 10)
	cast("1002", "Mob:2002")
	cast("1002", "Mob:2002")

	// 全部登出
	for _, p := range profiles {
		logout(p.id)
	}

	return pkts
}
