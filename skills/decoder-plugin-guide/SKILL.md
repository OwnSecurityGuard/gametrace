---
name: "decoder-plugin-guide"
description: "指引用户用 Go 编写解码插件（gt.decoder/v2，基于 gt-plugin-sdk）接入 GameTrace 平台：协议分析、插件骨架、TCP 重组与握手处理、plugin.yaml、测试与验证。semantic_rules 的选取标准、约束、校验机制与验收标准见 §5.2–§5.6。当用户要为新协议编写解码插件、接入自定义游戏协议、解析网络协议为业务事件，或要编写/审查/修正 semantic_rules 时调用。"
---

# GameTrace 解码插件开发指南

## 目的

指导用户把任意 TCP/UDP 网络协议接入 GameTrace 平台。解码插件把抓包帧转成结构化业务事件，宿主（gt-pipeline）负责 TCP 重组以外的平台职责：语义规则执行、事件配对、状态分析、前端展示。

- 语言：Go 1.26+（与宿主 gt-pipeline 的 go.mod 对齐，低于此版本可能出现兼容问题）
- SDK：`github.com/OwnSecurityGuard/gametrace/sdk` v0.9.0
- API 版本：`api_version: gt.decoder/v2`
- 插件形态：独立可执行文件，通过 Register RPC 向 registry 注册

## 何时使用

- 用户要编写新的解码插件接入平台
- 用户要解析某个游戏/应用的网络协议为结构化事件
- 用户询问如何让自定义协议在平台中解码、显示、配对
- 参考插件：`examples/http-decoder`、`examples/ws-decoder`、`examples/lp-decoder`（本仓库 TCP 模板，plugin.yaml 每条语义规则均先陈述协议事实）；wesnoth 解码器的规则注释是"证据注解"范式（§5.2 有摘录）

## 平台契约（先读，避免返工）

- **事件三分离**：Payload（业务字段）/ Meta（方向、msg_name 等平台元信息）/ Analysis（配对、状态变更分析）。解码器只产 Payload；Meta 由宿主写；Analysis 由宿主按 semantic_rules 生成。
- **消息名称**由 `name` 语义规则从 payload 提取（宿主写入 `meta.msg_name`），解码器不硬编码。
- **方向**：解码器可在事件上标注方向，宿主按端口/连接补齐。
- **配对**：宿主按 `correlation_id`（同一连接会话）与 `causation_id`（请求-响应）配对，前端左右并排展示。
- **Payload 必须是 JSON 可表达结构**：平台的语义规则（semantic_rules）、查询、前端展示全部基于 JSON path 提取，因此无论线格式是二进制、文本还是 key=value，解码器都**必须**把业务字段解析为 JSON 对象（string / int / bool / 数组 / 嵌套对象）。
- **硬约束（无论什么情况）**：Payload 只允许包含**原本游戏协议真实存在**的字段——字段名与协议属性名一致（或与用户确认的映射名），值是协议中实际传输的内容。**禁止**添加协议不存在的字段：解码器推导值、平台时间戳、内部 ID、原始文本副本等一律不得进入 Payload；这类辅助信息走 Meta（方向等平台字段）或 `_raw`（仅当用户确认保留原文时，且声明为 optional）。

## 工作流程

### 0. 确认平台连接信息（第一步必做，别猜）

插件通过环境变量连接平台。**先与用户确认要连哪个平台、怎么连**，再写代码。4 个变量**全部自动获取**：调一次平台工具 `get_plugin_env`，把返回的 `env_file` 原样写入 `.env` 即可，无需任何手填。

| 变量 | 含义 | 获取方式 |
|---|---|---|
| `GT_REGISTRY_ADDR` | registry 端点（插件注册） | **自动**：`get_plugin_env` 的 `registry_addr` |
| `GT_AUTH_TOKEN` | 注册鉴权 Bearer token | **自动**：`get_plugin_env` 返回调用者自己的 token；agent 托管（`GT_TUNNEL=1`）下平台自动注入，可留空 |
| `GT_DECODER_ADDR` | 解码器本地监听地址 | **自动**：`get_plugin_env` 的 `decoder_addr`（默认 `0.0.0.0:61887`） |
| `GT_DECODER_PUBLIC_ADDR` | 注册时上报、宿主回拨的地址 | **自动**：`get_plugin_env` 的 `decoder_public_addr`（外部可达 host + 端口） |

确认步骤：

1. 问用户平台部署形态：**本机单机** / **Docker 局域网** / **远端公网**。
2. 调 `get_plugin_env`（跨机部署、插件与调用方不在同一台机器时，参数 `host` 传插件所在机器视角的可达主机；平台已配 `GT_PUBLIC_HOST` 的公网/Docker 部署可省略）→ 把返回的 `env_file` 原样写入插件目录 `.env`。匿名模式（平台未配 token）下 `auth_token` 为空属正常。
3. 写错了也不怕：注册失败时平台会记录诊断（含注册连接来源 IP 建议值），`status_plugin` 返回的 `register_failure` 字段可查，按其提示修正 `.env` 后重新 `activate_plugin`。

#### 0.1 用 `.env` 集中管理连接配置（推荐）

不要在代码里写死地址；插件目录放 `.env`（**直接生成，不再用 `.env.example` 占位模板**），main.go 启动时加载。

**零手填、零复制**：让用户（或引导 agent）调 `get_plugin_env`，把返回的 `env_file` **直接写入插件目录 `.env`**——内容已含正确值与注释，无需任何改动：

```
# .env —— 内容由 get_plugin_env 的 env_file 原样写入；
#        同名环境变量优先于本文件。
GT_REGISTRY_ADDR=<get_plugin_env 的 registry_addr>
GT_AUTH_TOKEN=<get_plugin_env 的 auth_token；agent 托管 GT_TUNNEL 下可留空>
GT_DECODER_ADDR=0.0.0.0:61887
GT_DECODER_PUBLIC_ADDR=<registry_addr 的 host 段>:61887
```

> 提醒：`.env` 含用户 token，**不提交 git**——插件骨架自带 `.gitignore`（内容一行 `.env`），生成时一并创建，不要省略。

main.go 顶部加轻量加载器（不覆盖已存在的环境变量，无文件时静默跳过，不引第三方依赖）：

```go
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
```

### 1. 协议分析（必做，占一半工作量）

写代码前必须确认线格式，优先**读官方客户端/服务端源码**（最权威），其次抓包。产出协议笔记，明确：

1. 传输层与帧定界：TCP/UDP？固定头 + 长度字段 / 分隔符 / 定长？
2. 连接握手：首个包是否有固定字节（如 4 字节握手）？哪个方向？
3. 负载是否压缩/加密：gzip / bzip2 / TLS / 自研算法？
4. 方向判定依据：固定服务器端口 / 标志位 / 无法判定（由宿主补齐）？
5. 语义证据：候选 semantic_rules 的出处（哪个消息、哪个字段、哪一侧；双向都发的标签单独标记）。

协议笔记写进插件注释与 plugin.yaml 的 `hints`；另产出一份**语义规则证据表**（每条候选规则标注依据来源；证据不足的标「待确认」），它是 §5.3 四道准入门的输入——写 plugin.yaml 前与用户对齐，不猜。

### 2. 插件骨架

目录：`plugins/<protocol>-decoder/`，文件清单：

```
plugins/<protocol>-decoder/
├── go.mod          # 依赖 gt-plugin-sdk v0.9.0
├── main.go         # 入口：RunRegisterLoopWithOptions + loadDotEnv(".env")
├── decode.go       # 核心：Decode(req) → []*Event
├── <fmt>.go        # 负载解析器（解压/解帧/解文本）
├── plugin.yaml     # manifest：semantic_rules
├── .env            # 连接配置：内容按「步骤 0」调 get_plugin_env 的 env_file 原样写入，零手填
├── .gitignore      # 一行 .env——token 是用户凭证，不入库
├── <fmt>_test.go   # 解析器单测
└── decode_test.go  # 全链路解码测试 + manifest 一致性
```

go.mod：

```go
module your.org/plugins/foo-decoder

go 1.26

require github.com/OwnSecurityGuard/gametrace/sdk v0.9.0
```

main.go（入口必须是 `sdk.DecodeFuncV2` 函数，不是实例；每个 input 必须以 `done=true` 收尾，即使一条消息都没解出来）：

```go
package main

import (
	"os"
	"strings"

	"github.com/OwnSecurityGuard/gametrace/sdk"
	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

func main() {
	// 连接配置优先 .env（见「步骤 0」）；同名环境变量（如 agent 托管注入的
	// GT_AUTH_TOKEN）优先于文件。
	loadDotEnv(".env")

	d := newDecoder()
	// GT_REGISTRY_ADDR / GT_DECODER_ADDR / GT_DECODER_PUBLIC_ADDR 由 SDK 原生
	// 读取；这里只需显式传入鉴权 token 与隧道开关。
	sdk.RunRegisterLoopWithOptions(d.decodePacket, sdk.RegisterOptions{
		Tunnel:    os.Getenv("GT_TUNNEL") != "",
		AuthToken: os.Getenv("GT_AUTH_TOKEN"),
	})
}

// decodePacket 实现 sdk.DecodeFuncV2（gt.decoder/v2）。
// req.Payload 在 pcap 路径下是完整链路层帧；每个 input 必须以 done=true 收尾。
func (d *decoder) decodePacket(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
	events, err := d.Decode(req)
	if err != nil {
		return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true, Error: err.Error()})
	}
	for _, e := range events {
		// v0.9.0 契约：Payload（纯业务）与 Meta（方向等）分开传输，
		// 前端「元信息」独立展示，不再混入业务 payload。
		mp, mErr := event.ValueFromMap(e.Payload).MarshalMsgpack()
		if mErr != nil {
			return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true, Error: "marshal: " + mErr.Error()})
		}
		var metaData []byte
		if len(e.Meta) > 0 {
			md, me := event.ValueFromMap(e.Meta).MarshalMsgpack()
			if me != nil {
				return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true, Error: "marshal meta: " + me.Error()})
			}
			metaData = md
		}
		if err := stream.Send(&pb.DecodeResponseV2{
			InputId:        req.GetInputId(),
			EventType:      e.EventType,
			PayloadMsgpack: mp,
			MetaMsgpack:    metaData,
			CorrelationKey: e.CorrelationKey,
		}); err != nil {
			return err
		}
	}
	return stream.Send(event.Done(req.GetInputId()))
}
```

### 3. 解码核心与 TCP 重组（高频踩坑区，重点阅读）

解码入口接收 `*pb.DecodeRequest`（payload = **完整链路层帧**），返回插件内部定义的 `[]*Event`。宿主把同一个 TCP 连接上的帧串行喂给插件，但**插件必须自己处理传输层与字节流重组**。使用 SDK 的 `framing` 包，禁止手写链路层剥离。

内部事件结构（`decodePacket` 映射到 `DecodeResponseV2`）：

```go
// Event 是一条解码结果。
type Event struct {
	EventType      string         // 形如 "<protocol>.<msg_type>"，如 "wesnoth.login"
	Payload        map[string]any // 业务字段（payload 根对象），纯业务，不带平台字段
	Meta           map[string]any // 元信息（direction 等），前端「元信息」弹窗展示
	CorrelationKey string         // 业务会话/操作标识（battle_id / txn_id 等）；不是连接标识
}
```

解码核心骨架：

```go
import (
	"sync"

	"github.com/OwnSecurityGuard/gametrace/sdk/framing"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

type decoder struct {
	reasm      *framing.Reassembler // 多连接安全：按 FlowKey 隔离流状态
	mu         sync.Mutex
	handshakes map[string]bool // 方向流 key → 4 字节握手已消费
}

func newDecoder() *decoder {
	return &decoder{reasm: framing.NewReassembler(), handshakes: map[string]bool{}}
}

func (d *decoder) Decode(req *pb.DecodeRequest) (events []*Event, err error) {
	events = []*Event{}
	defer func() { if r := recover(); r != nil { events = []*Event{} } }() // 兜底：坏包不 panic

	seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
	if !ok {
		return events, nil
	}
	flowKey := seg.Flow.String() // 方向性流标识：每连接每方向独立

	// 坑 1【必踩】：SYN/RST 必须重置本方向握手簿记。
	// 否则客户端重连复用同一 5-tuple 时，旧连接 seen=true 残留会把新连接
	// 的头 4 字节握手误当帧长度 → n==0 → 失步。
	if seg.IsTCP && (seg.Flags.SYN || seg.Flags.RST) {
		d.mu.Lock(); delete(d.handshakes, flowKey); d.mu.Unlock()
	}

	d.mu.Lock()
	seen := d.handshakes[flowKey]
	d.mu.Unlock()

	// 坑 2【必踩】：SYN/RST/FIN 空 payload 段也必须 Push 给 Reassembler。
	// 跳过会让 SDK 的 SYN 重置 sequence base、RST/FIN 退役流不生效；
	// 5-tuple 复用时序列跳变被当作数据空洞，后续段卡在 oob 永不释放。
	if len(seg.Payload) == 0 {
		d.reasm.Push(seg)
		return events, nil
	}

	s := d.reasm.Push(seg)
	for {
		raw := s.Bytes()
		if !seen {
			// 每方向流首 4 字节可能是连接握手。消费后标记 seen。
			if handshake, ok := isHandshake(raw); ok {
				s.Consume(4)
				seen = true
				d.mu.Lock(); d.handshakes[flowKey] = true; d.mu.Unlock()
			}
			if len(s.Bytes()) == 0 { break }
		}
		n := frameLen(raw) // 你的帧长度字段
		if n == 0 || n > maxFrame { d.reasm.Forget(seg.Flow); break } // 失步：丢弃流状态
		if len(raw) < 4+int(n) { break } // 不完整，等下一段
		ev := d.decodePayload(raw[4:4+n], seg)
		// 不设 CorrelationKey：连接身份由宿主派生的 ConnID 承担。
		// 该字段只在协议里有真正的业务会话/操作 id 时才填。
		events = append(events, ev)
		s.Consume(4 + int(n))
	}
	return events, nil
}
```

要点：

- `framing.Reassembler` 按 `FlowKey`（每方向流一个缓冲）隔离，**天然支持多连接**，可跨 goroutine 并发调用（内部有锁）；单流的 Bytes/Consume 必须串行。
- 方向判定：读 `seg.Flow.SrcPort/DstPort`，目的端口 == 服务器端口 → C→S，否则 S→C；无法判定返回空，宿主补齐。
- 失步（长度非法）时 `Forget` 丢弃该流状态，让下一个段从中间重新自同步。
- **不要设 `CorrelationKey`**（除非协议里有真正的业务会话/操作 id）。连接身份由宿主按五元组 + TCP 生命周期派生为 `ConnID`，请求/响应配对由 `semantic_rules` 的 pair 规则声明；把 `seg.Flow.Canonical()` 塞进 `CorrelationKey` 是误用，会在 pair 命中时被覆盖（宿主把原值转存到 `Meta.corr_key`）。
- 长度字段注意字节序（wesnoth 是 big-endian）。

**UDP 场景**（与 TCP 差异，SDK 已处理好，解码器只需注意语义）：

- `framing.ExtractL7` 对 UDP 同样返回 Segment（`IsTCP=false`、`Seq/Flags` 为零）；每个 UDP 包是**自包含**的，`Reassembler.Push` 对 UDP 直接透传（返回整包负载、不进入重组缓冲），`Consume` 为 no-op。
- UDP **没有**连接握手与跨段重组：每包独立解码，包内第一个字段即业务数据，不存在「首 4 字节握手」逻辑（`seen` 簿记只对 TCP 有意义）。
- 方向判定与 TCP 相同（按端口）。UDP 同样**不设** `CorrelationKey`（无连接生命周期，更没有"连接会话"可填）。
- 多连接（多对端）天然隔离：不同五元组是不同 `FlowKey`；UDP 无 FIN/RST，无流生命周期，`handshakes` 簿记不适用于 UDP，SYN/RST 重置逻辑对 UDP 段（`IsTCP=false`）自然跳过。

### 4. 负载解析器与 Payload 规范化

#### 4.1 规范化目标（平台规则的根基）

平台所有语义规则、查询、前端展示都基于 **JSON path 提取**，因此解析器输出的 Payload 必须是干净、可理解的 JSON 对象：

1. **线格式 → JSON**：无论协议是二进制、分隔文本还是 `key=value`，都要解析为结构化字段（`string` / `int` / `bool` / `数组` / `嵌套对象`）。例：`is_moderator="no"` → `"is_moderator": "no"`。
2. **字段名**：优先保持协议属性**原名**；确需改名（如转义、歧义）时与用户确认映射名，保证用户一眼能对应回协议。
3. **嵌套字段的进一步解码**：若协议某字段的值本身是结构化内容（如内嵌 WML/CSV/编码串），可与用户确认后递归解析为嵌套 JSON（例：`player` 串 → `{"name":..., "id":...}`）；确认不拆时保留字符串原样。
4. **不破坏语义**：任何转换不得丢值、改值、臆测含义；拿不准的保持原样并标注（走 Meta 或与用户确认）。

#### 4.2 硬约束复查（产 Payload 前逐字段过）

- 每个字段都能在协议中找到出处（属性名 + 值）。
- 协议中不存在的字段（推导值、平台时间戳、内部 ID、原文副本）一律**不进 Payload**。
- 原始文本等辅助信息：默认进 **Meta**；仅当用户明确要求保留原文时，才以 `_raw` 字段进入 Payload 并截断上限（如 8KB）。

#### 4.3 解析实现要点（以 wesnoth simple_wml 为例，通用）

- 解压：gzip/bzip2 等（wesnoth 首字节 `'B'` 表示 bzip2，否则 gzip；用 `io.LimitReader` 防解压炸弹）。
- 文本解析器必须**宽容**：坏输入不 panic（外层 recover 兜底），跳过错乱字符继续。
- 属性解析：key 搜索 `=` 限制在本行内，防止跨行吞掉垃圾文本。
- 引号值转义：只处理协议定义的转义（simple_wml 是 `""` 转义引号、`+"` 续行——**续行拼接要补 `\n`**；`#` 注释跳到行尾含换行）。**不要臆造转义**：如 simple_wml 中反引号无特殊语义（读源码确认过），直接按字面保留。
- 截断超大字段，防止事件过大。

### 5. plugin.yaml（manifest）

```yaml
api_version: gt.decoder/v2
name: foo-decoder
protocol: foo
protocol_version: "1.0"
type: decoder
hints:
  - tcp
  - length-prefixed
  - gzip
  - port:12345
capabilities:
  decode: true
semantic_rules:
  - id: foo.name_msg
    when:
      - path: msg_type
        op: exists
    effect:
      type: name
      key: msg_type
```

- `semantic_rules` 是接入平台语义能力的入口，effect 闭集 3 类：`name`（消息名→`meta.msg_name`）、`annotate`（角色标签→`meta.semantic`）、`pair`（请求/响应配对→`correlation_id`+`causation_id`）。接入要点见下节。
- `hints` 帮助平台匹配：传输层、压缩、定界方式、端口。
- 注册前宿主会校验 manifest；规则校验失败会在宿主日志输出（`semantic rules:` 前缀），注意查看。

#### 5.1 语义规则接入（semantic_rules）——复用平台的配对/命名/标注能力

规则 = Predicate(`when`) + Effect，**决定事件怎么被解释/关联，权重高于解码器硬编码**。effect 是一个闭集，插件只需声明，执行由平台完成。

> **补充原则：能上规则不硬编码；但每条规则必须先过 §5.3 的四道准入门，不虚构、不猜。**
> 先读 §5.2 的运行期执行事实——平台只校验声明形状，运行期失效是静默的。

| effect | 作用 | 产出(host) | 必填字段 |
|---|---|---|---|
| `name` | 从 payload 提取消息名 | `meta.msg_name` | `key` |
| `annotate` | 打角色标签：request/response/notification/error | `meta.semantic` | `semantic` |
| `pair` | 请求-响应配对（同一个“来回”） | `correlation_id`（双方相同）+ `causation_id`（响应方→请求方） | `sides`（恰好 2 个，每侧自带 `key`） |

**① `name` —— 消息名**（替代解码器写死）：

```yaml
- id: foo.name_msg
  when: [ { path: msg_type, op: exists } ]
  effect: { type: name, key: msg_type }
```

规则求值**先执行 name 并把结果注入 `_meta.msg_name`**，因此后续规则可用 `_meta.msg_name` 判定（见 `pair`）。

**② `annotate` —— 角色**：

```yaml
- id: foo.annotate_req
  when: [ { path: _meta.msg_name, op: eq, value: login } ]
  effect: { type: annotate, semantic: request }   # response/notification/error 同理
```

**③ `pair` —— 请求/响应配对（跨消息横向关联）**：
pair 恰好 2 个 `sides`，每侧自带 `key`（该侧配对键 GJSON path，两侧可指向不同字段）与角色判定（`path`/`op`/`value`）；**每侧 `key` 必填，无规则级 `key`**。同一个来回里两侧 `key` 的取值相等即配对；**side 下标 0 = 请求方、1 = 响应方**（声明顺序即角色，决定 `causation_id` 方向）。配对成功后双方写同一 `correlation_id`（取请求方事件 id），**响应方 `causation_id` 指向请求方**；前端据此把请求/响应左右并排。典型：请求带 `ts`，响应回显同一 `ts`。

```yaml
- id: foo.pair_ts
  when: [ { path: _meta.msg_name, op: in, value: [ping, pong] } ]
  effect:
    type: pair
    sides:
      - { path: _meta.msg_name, op: eq, value: ping, key: ts }
      - { path: _meta.msg_name, op: eq, value: pong, key: ts }
```

**附：「一个包产出多条事件」不走规则**

DecodeV2 的解码响应本身就是事件切片（`r.Events []*Event`），"一个网络消息承载多个逻辑成分"
是解码器的原生能力，不需要任何 semantic rule。在解码器里拆开直接返回即可：

```go
// 世界快照 → 每条实体状态一个事件（各自带完整 Meta 与 Analysis）
events := make([]*event.Event, 0, len(snapshot.Ents))
for _, ent := range snapshot.Ents {
    ev := event.NewEvent(req.SessionID, "entity_snapshot", srcID, ent.Value(), ctx)
    ev.Meta = event.ValueObject(map[string]event.Value{
        "msg_name":  event.ValueString("EntityState"),
        "direction": event.ValueString("server_to_client"),
    })
    // 需要状态投影就挂 _state_changes（走 Analysis 通道）
    ev.Analysis = event.ValueObject(map[string]event.Value{
        "_state_changes": ent.StateChanges(),
    })
    events = append(events, ev)
}
// DecodeV2 的响应 Events 是切片，把这些事件一并返回即可。
```

**为什么不要用规则声明来做这件事**：平台曾有过一个 `extract` 效果（声明 `source` 拆子事件、
子事件挂 `parent_id`），已于 2026-09-18 整体删除。它拆出来的子事件不过语义规则、不参与状态
投影、`schema_id` 也没有落点——解码器路径同时具备这三件能力，规则路径三样全缺。

> 方向从哪来：规则里判定请求/响应侧用的是 `_meta.direction`，它由**解码器**写进 Meta 通道
> （`Meta["direction"] = "client_to_server" | "server_to_client"`），平台把 Meta 并入求值视图的
> `_meta` 键。解码器判不了方向就留空，不要猜——留空顶多是规则不命中，猜错会误导配对。

**规则通用约束**（不满足会在注册期校验报错，留意宿主 `semantic rules:` 日志）：
- `when` 谓词 = GJSON `path` + `op`（闭集：`eq/neq/exists/not_exists/gt/gte/lt/lte/in/not_in/contains/prefix/suffix`）+ `value`。
- 规则 `id`：点分小写段 `[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*`，全局唯一，如 `foo.pair_ts`。
- 语义标签闭集：`request/response/notification/error`，勿臆造。

**何时用哪个**：跨消息的“一问一答”→ `pair`；消息名 → `name`；角色标签 → `annotate`。**优先声明规则而非解码器硬编码**去表达这类语义，便于复用平台的配对/前端联动能力。同消息内“一拆多”（快照→多实体、批量→逐条）**不属于规则层**——见上「附」。

**验证**：注册后 `get_session_status`/宿主日志看规则加载；`list_decoded_data` 抽查 `meta.msg_name`、`meta.semantic`、`trace.correlation_id/causation_id`。

#### 5.2 运行期执行事实（写规则前必读）

**平台只在注册期校验声明形状**（`rule.RulesReport` → `gt.semantic.*`：id 格式、effect 闭集、必填字段、op 闭集）。下面 10 条是**运行期语义，注册期一条都不查**——写错就是静默失效，或更糟：结构合法但运行期错配、错标。

源码依据：`sdk/rule/evaluate.go`、`sdk/rule/predicate.go`、`cmd/gt-pipeline/semantic_hook.go`。

| # | 事实 | 对写规则的约束 |
|---|---|---|
| F1 | 求值视图 = `payload` 合并 Meta 进 `_meta`；**payload 自带 `_meta` 时平台不再合并 Meta** | 解码器不得在 payload 里放 `_meta` 键 |
| F2 | 方向的唯一权威路径是 `_meta.direction`，取值闭集 `client_to_server` / `server_to_client` | 用别的字段名或取值一律不命中 |
| F3 | 除 `exists` / `not_exists`，**path 不存在一律判 false** | 不能用 `neq` 表达"字段不存在"或"没有错误" |
| F4 | `eq/neq/in/gt/gte/lt/lte` **kind 感知，不隐式转型**：`0` ≠ `"0"` | `value` 字面量类型必须与 payload 一致 |
| F5 | `name` 规则先跑，**仅首个命中**写入 `_meta.msg_name`（按声明顺序） | 多条 name 规则只有第一条有效，其余空转 |
| F6 | pair 待配对池是 `semanticEngine.pending`（每个抓包任务一个），键 = `ConnID + \x00 + RuleID + \x00 + 配对键`；`MatchPair` 只比「同规则 + 键相等 + Side 不同」 | 配对键只需**连接内唯一**；无 `ConnID` 的事件（无五元组）退化为全局池 |
| F7 | 待配对 TTL **30s**，且事件 flush 落库后配对不回写（宿主注释已标为已知限制） | 异步/长轮询/匹配队列这类响应不能声明 pair |
| F8 | `evalPair` 短路：命中 side i 就取 side i 的 key，key 缺失**直接 no-hit，不再试另一侧** | pair 两侧谓词必须互斥，两侧 key 都必须存在 |
| F9 | annotate 同一标签去重后写入 `meta.semantic` 数组 | 不要为同一 semantic 声明多条规则 |
| F10 | `CorrelationKey` 是**业务会话/操作**标识；pair 命中时 `Trace.CorrelationID` 被更紧的「一问一答」分组键（请求方事件 ID）覆盖，原值转存 `Meta.corr_key` | 别把连接身份（五元组 / `flow_id` / `FlowKey.Canonical()`）填进 `CorrelationKey`——它会被 pair 结果吃掉，且连接身份宿主已经自己派生 |

求值顺序：**全部 `name` 规则（按声明顺序，首个命中注入 `_meta.msg_name`）→ 其余规则**。因此凡用 `_meta.msg_name` 分支的 annotate/pair，前提是 name 规则确实命中；name 未命中时后续分支全部落空。

**F6 详解：连接维度是怎么来的**（`cmd/gt-pipeline/semantic_hook.go` `applyPairs`）。早期版本配对池是全局单表（键只有 `RuleID + 配对键`），两条 TCP 连接 A、B 用 per-connection 自增 `seq`（各从 1 开始）时：

1. A 发 `seq=1` 请求 → `pending["rule\x001"] = A的请求`（Side 0）
2. B 发 `seq=1` 请求 → `MatchPair(Side 0, Side 0)` 同侧返回 false → 落到 `e.pending[mapKey] = B的请求`，**A 的请求被静默覆盖，永久丢失**
3. A 回 `seq=1` 响应（Side 1）→ 与 B 的请求 Side 不同 → **判定配对成功**

结果：A 的响应配到 B 的请求，`causation_id` 指向 B。单连接时 `seq` 唯一不会暴露——这也是示例插件从未触发该问题的原因。

> **平台已修复（2026-09-15）**：pair 池按 `ConnID` 分片，配对键只需**连接内唯一**。
> per-connection 自增 seq 这类键现在可以正常声明。
>
> `ConnID` 是**业务连接实例标识**，与五元组解耦：
> **识别**连接边界靠网络事实（五元组 + TCP SYN/FIN/RST，绕不开），
> **标识**连接实例用 `connTracker` 派生的 `ConnectionID`（`cmd/gt-pipeline/conn_tracker.go`）——
> 格式 `tcp:10.0.0.2:50000<->10.0.0.1:9250#3`，`#N` 是代次。
> 纯 SYN（不带 ACK）开新一代；FIN 半关闭到两侧才退役；RST 立即退役。
> 因此**重连复用同一五元组会拿到新 ID**，两代连接的 seq 不会混。
>
> 注意：只有 pcap / 网卡抓包带 TCP 控制位；探针（agent）上报不带，
> 此时退化为"每五元组一个实例、不退役"——比修复前不差，但代次无法区分。
> 移动代理自带真实 `conn_id`，原样保留不参与派生。

#### 5.3 规则选取：四道准入门

目标是"能用规则表达的语义尽量用规则，且每条都站得住"，**不是写得越多越好**。每条候选规则必须依次过四道门；**任一不过就不写**，进「待确认」清单问用户。

| 门 | 判据 | 不过的典型表现 |
|---|---|---|
| **G1 证据门** | 说得出出处（官方客户端/服务端源码 > 真实抓包报文 > 协议文档），注释能写清"哪个消息、哪个字段、哪一侧" | 注释里写不出依据来源 |
| **G2 路径门** | `when.path` / `effect.key` / `effect.source` 在**真实解码产物**上存在，且类型确定（数字/字符串/数组/对象） | path 拼错、字段只在部分消息存在、`source` 是标量 |
| **G3 取值门** | `when.value` 与配对键取值在真实报文里**出现过**，且字面量 kind 与 payload 一致（F4） | 规则永不命中（死规则） |
| **G4 效果门** | 该 effect 在本协议真能成立：pair 键连接内唯一 + 30s 内往返；annotate 方向可判定；name 的 key 在真实报文里存在 | 结构合法但运行期落空或错配 |

排序约束：先 `name` → 再 `annotate` → 最后 `pair`。pair 风险最高（错配会直接污染前端左右并排展示），证据要求最严。

#### 5.4 各 effect 的判据与反例

**`name`** —— 判据：提取字段在**全部目标消息**里都存在且能区分消息。
反例：只在部分消息存在的字段当全局 name；声明多条 name 规则指望"互补"（F5：只有第一条生效）。

**`annotate`** —— 判据：角色由**可判定的事实**推出：`_meta.direction`、消息身份、或协议里明确的推送标志位（如 `seq == 0`）。**双向都发的消息留空不标**。
反例：凭"我觉得这是请求"标注；照搬别的协议的标签。

**`pair`** —— 判据（五条全满足）：
1. 两侧 key 在真实"一问一答"里取值相等（回显字段，抓包核对过）；
2. 两侧谓词互斥（F8），都指向 `_meta.direction` 或其他互斥事实；
3. 配对键**连接内唯一**（F6：pair 池已按 ConnID 分片，per-connection 自增 seq 可用）；
4. 往返在 30s 内且未经 flush（F7）；
5. `when` 已拦截推送/广播。

反例：两侧谓词都用 `exists` 导致永远命中 side 0（F8）。
（per-connection 自增 seq 曾经是反例，F6 修复后已可用——但前提是事件带 `ConnID`，
探针上报等无连接标识的来源仍会退化成全局池，此时仍按"全局唯一"要求自己。）

```yaml
# 依 据：客户端发 [request_choice] 带 request_id；服务端在 [random_seed] /
#        [change_controller_wml] 里回显同一 request_id（见服务端源码）。
# 配对键：request_id 由服务端全局发号，跨连接唯一（已抓包核对无重复）。
# 拦 截：when 限定 request_id 存在，服务端主动推送不带该字段，不参与配对。
- id: wesnoth.pair_choice
  when: [ { path: request_id, op: exists } ]
  effect:
    type: pair
    sides:
      - { path: _meta.direction, op: eq, value: client_to_server, key: request_id }
      - { path: _meta.direction, op: eq, value: server_to_client, key: request_id }
```

**`extract`**：该效果已删除，不再有这条门。同消息"一拆多"直接在解码器里返回多条事件
（见 §5.1「附」），不走规则层。

#### 5.5 覆盖率回放（诊断产出，人工判定）

声明合法 ≠ 运行期生效。规则写完后**必须**用真实抓包固件（或固件解码出的 payload + meta）回放并产出覆盖率表，**把表交给用户逐条判定**。0 命中的规则不得静默保留：删除 / 修正 / 在注释写明"已知未覆盖 + 原因 + 判定人"，三选一。

两个指标，判据不同（示例代码可直接放进 `decode_test.go`）：

```go
// 指标一：逐条孤立回放 —— 该规则在固件上是否具备命中能力（G3/G4 的机器验证）。
// 必须孤立跑：全量回放的 annotate 结果不带 RuleID，无法归因到具体规则。
func ruleHits(t *testing.T, m *sdk.Manifest, views []sdkevent.Value) map[string]int {
	t.Helper()
	hits := make(map[string]int, len(m.SemanticRules))
	for _, r := range m.SemanticRules {
		for _, v := range views {
			res, err := rule.Evaluate([]rule.Rule{r}, v)
			if err != nil {
				t.Fatalf("rule %s evaluate: %v", r.ID, err)
			}
			if len(res.Names)+len(res.Pairs)+len(res.Children)+len(res.Semantics) > 0 {
				hits[r.ID]++
			}
		}
	}
	return hits
}

// 指标二：全量回放 —— name 规则只有首个命中会写入 _meta.msg_name（F5），
// 孤立回放会高估，必须单独确认"实际生效"的是哪一条。
func effectiveNameRules(t *testing.T, m *sdk.Manifest, views []sdkevent.Value) map[string]int {
	t.Helper()
	eff := map[string]int{}
	for _, v := range views {
		res, err := rule.Evaluate(m.SemanticRules, v)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if len(res.Names) > 0 {
			eff[res.Names[0].RuleID]++
		}
	}
	return eff
}
```

> 片段依赖：`sdk`（`ParseManifest`）、`sdkrule "…/sdk/rule"`、`sdkevent "…/sdk/event"`。
> `views` 是固件解码出的求值视图，构造方式与示例插件一致——`payload` 与 `meta` 合并成
> `payload + {"_meta": meta}`（F1），保证与宿主 `withMetaObject` 的行为一致。

产出表格交用户判定：

| 规则 id | effect | 固件命中数 | name 实际生效 | 判定 |
|---|---|---|---|---|
| `foo.pair_ts` | pair | 12 | — | 保留 |
| `foo.mark_push` | annotate | 0 | — | **删**：固件无推送样本，G3 不过 |

判定规则：
- 命中数 0 → 死规则，默认删除；确需保留（固件覆盖不到的稀有分支）必须在注释写明原因并经用户确认。
- 多条 `name` 规则里"实际生效"为 0 → 空转规则，删除或调整声明顺序。
- `pair` 两侧命中数应成对（各 N 次）；只命中一侧说明对侧谓词或 key 有问题（F8）。

#### 5.6 虚构/无依据规则的常见说辞（都要拒绝）

**虚构/无依据规则的常见说辞（都要拒绝）**：

| 说辞 | 事实 |
|---|---|
| "先加上，反正 annotate 无害" | 规则权重高于硬编码，标错角色/方向直接误导前端展示 |
| "别的项目都这么标，照搬" | 每个协议语义不同，必须以本协议证据为准 |
| "我按包结构猜的，八成对" | 猜的规则要么在真实报文上不命中（死规则），要么错配请求/响应；注册校验发现不了 |
| "skill 说要优先规则，凑几条" | 优先规则 ≠ 凑规则；没有证据就保持最低限度或留空 |

**红旗（出现即停，先找证据或问用户）**：

- "我觉得这个应该是请求/响应"
- "这个字段看起来像配对键"
- "先写上去，后面再验证"
- 规则注释里写不出依据来源

### 6. 测试（质量门槛）

必须覆盖以下场景，缺一不可（标注 TCP 的条目仅对 TCP 协议适用，UDP 插件替换为分包独立/无握手用例）：

1. 解析器单测：正常输入、转义、注释、续行、坏输入不 panic。
2. 解压单测：gzip 与 bzip2 固件（固件生成用临时脚本，跑完删除）。
3. 全链路：单帧解码、**一个消息跨多个 TCP 段**（reassembly，TCP）、同包多帧。
4. 握手（TCP）：握手被消费、mid-stream attach（无握手段）。
5. 畸形输入：非法长度、截断、压缩损坏 → 不 panic、不无限循环。
6. **多连接**：两条独立连接互不干扰（连接隔离由宿主 `ConnID` 保证，pair 池已按连接分片，见 F6）。
7. **5-tuple 复用（重连，TCP）**：SYN 后新连接握手被再次正确消费（回归坑 1/坑 2）。
8. manifest 一致性：`semantic_rules` 引用的 payload 路径（`when.path`/`effect.key`/`effect.source`）能实际解析；`contract.NewPluginChecker().Check(m)` 零 violation。
9. **规则覆盖率回放（§5.5，必做）**：用真实抓包固件跑 `rule.Evaluate`，产出「逐条命中数 + name 实际生效」覆盖率表并**交用户判定**；0 命中规则不得静默保留。声明期校验（`gt.semantic.*`）不查语义事实，只过它不能证明规则有效。
10. **Payload 纯度**：断言 payload 中不含协议外字段（对照 4.2 硬约束）。

固件构造用 `gopacket` 拼以太网 + IPv4 + TCP 帧，使 `framing.ExtractL7` 能读出端口（用于方向判定）；测试内对压缩负载直接预压缩后写入。

### 7. 验证命令（全部通过才算完成）

```bash
cd plugins/foo-decoder
go mod tidy
go vet ./...
go build -o foo-decoder.exe .
go test -count=1 ./...   # 必须 -count=1：go test 缓存会掩盖问题
```

- 宿主侧验证：把插件 `--registry=` 指向宿主，观察宿主日志 `semantic rules:` 无 error 级问题；用 `list_decoded_data` 核对事件的 `payload`（业务字段）与 `meta.msg_name`（消息名）。
- 前端验证：协议数据页应显示业务 payload 为主、消息名正确、请求/响应按 `causation_id` 配对并排。

## 踩坑清单（完成前逐条自查）

- [ ] go.mod 为 `go 1.26`（与宿主对齐）
- [ ] SYN/RST 空 payload 段 Push 给了 Reassembler（坑 2）
- [ ] SYN/RST 重置了握手簿记（坑 1；仅 TCP 需要，UDP 跳过）
- [ ] 长度字段字节序正确（wesnoth 为 big-endian）
- [ ] 解析器不 panic：recover 兜底 + 宽容解析
- [ ] 续行拼接补了 `\n`，注释跳过了整行
- [ ] 没有臆造转义（反引号等以源码为准）
- [ ] Payload 全部字段可在协议中找到出处（硬约束：无协议外字段）
- [ ] Payload 为 JSON 可表达结构（key=value/二进制已转结构化字段）
- [ ] 嵌套/编码字段是否拆分已与用户确认
- [ ] 原始文本默认进 Meta；进 payload 需用户确认并声明 optional + 截断
- [ ] **`CorrelationKey` 没被当成连接标识**：没有填 `seg.Flow.Canonical()` / `flow_id` / 五元组；只填了协议里的业务会话/操作 id，没有就不填（连接身份归宿主 `ConnID`）
- [ ] payload 里没有 `_meta` 键（F1：有则平台不再合并 Meta）
- [ ] 解码器写了 `Meta["direction"]`，取值只用 `client_to_server` / `server_to_client`（F2）
- [ ] **每条 semantic_rules 都过了四道门**（§5.3）：G1 证据 / G2 路径 / G3 取值 / G4 效果；任缺一条的不写，进「待确认」清单问用户
- [ ] 规则注释写了依据来源（哪个消息/字段/哪一侧、源码还是抓包）
- [ ] `when` 没有用 `neq` 表达"字段不存在"（F3）；`value` 字面量类型与 payload 一致（F4）
- [ ] `name` 规则至多一条生效，多条时确认过声明顺序（F5）
- [ ] `pair` 配对键**连接内唯一**（F6，per-connection seq 可用）且已抓包核对；两侧谓词互斥（F8）；往返 30s 内（F7）；推送/广播已被 `when` 拦截
- [ ] 双向都发的消息 `annotate` 已留空（不标错方向/角色）；标错比不标更糟
- [ ] 已跑覆盖率回放并产出覆盖率表交用户判定，0 命中规则已删除或写明原因（§5.5）
- [ ] 测试覆盖：跨段重组、多连接、5-tuple 复用、manifest 一致性（UDP 插件加：分包独立、无握手）
- [ ] `go vet` + `go build` + `go test -count=1` 全过
- [ ] 宿主日志无 semantic rules error；前端消息名正确显示
