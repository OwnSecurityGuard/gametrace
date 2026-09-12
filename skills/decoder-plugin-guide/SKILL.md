---
name: "decoder-plugin-guide"
description: "指引用户用 Go 编写解码插件（gt.decoder/v2，基于 gt-plugin-sdk）接入 GameTrace 平台：协议分析、插件骨架、TCP 重组与握手处理、plugin.yaml、测试与验证。当用户要为新协议编写解码插件、接入自定义游戏协议或解析网络协议为业务事件时调用。"
---

# GameTrace 解码插件开发指南

## 目的

指导用户把任意 TCP/UDP 网络协议接入 GameTrace 平台。解码插件把抓包帧转成结构化业务事件，宿主（gt-pipeline）负责 TCP 重组以外的平台职责：语义规则执行、事件配对、状态分析、前端展示。

- 语言：Go 1.26+（与宿主 gt-pipeline 的 go.mod 对齐，低于此版本可能出现兼容问题）
- SDK：`github.com/OwnSecurityGuard/gt-plugin-sdk` v0.8.2
- API 版本：`api_version: gt.decoder/v2`
- 插件形态：独立可执行文件，通过 Register RPC 向 registry 注册

## 何时使用

- 用户要编写新的解码插件接入平台
- 用户要解析某个游戏/应用的网络协议为结构化事件
- 用户询问如何让自定义协议在平台中解码、显示、配对
- 参考插件：`plugins/godot-ecs`、`plugins/godot-gateway`、`plugins/wesnoth-decoder`（均可直接对照）

## 平台契约（先读，避免返工）

- **事件三分离**：Payload（业务字段）/ Meta（方向、msg_name 等平台元信息）/ Analysis（配对、状态变更分析）。解码器只产 Payload；Meta 由宿主写；Analysis 由宿主按 semantic_rules 生成。
- **消息名称**由 `name` 语义规则从 payload 提取（宿主写入 `meta.msg_name`），解码器不硬编码。
- **方向**：解码器可在事件上标注方向，宿主按端口/连接补齐。
- **配对**：宿主按 `correlation_id`（同一连接会话）与 `causation_id`（请求-响应）配对，前端左右并排展示。
- **Payload 必须是 JSON 可表达结构**：平台的语义规则（semantic_rules）、查询、前端展示全部基于 JSON path 提取，因此无论线格式是二进制、文本还是 key=value，解码器都**必须**把业务字段解析为 JSON 对象（string / int / bool / 数组 / 嵌套对象）。
- **硬约束（无论什么情况）**：Payload 只允许包含**原本游戏协议真实存在**的字段——字段名与协议属性名一致（或与用户确认的映射名），值是协议中实际传输的内容。**禁止**添加协议不存在的字段：解码器推导值、平台时间戳、内部 ID、原始文本副本等一律不得进入 Payload；这类辅助信息走 Meta（方向等平台字段）或 `_raw`（仅当用户确认保留原文时，且声明为 optional）。

## 工作流程

### 0. 确认平台连接信息（第一步必做，别猜）

插件通过环境变量连接平台。**先与用户确认要连哪个平台、怎么连**，再写代码。4 个变量中**只有 `GT_AUTH_TOKEN` 需要手填**，其余地址由平台现有接口自动获取，无需手填。

| 变量 | 含义 | 获取方式（自动 or 手填） |
|---|---|---|
| `GT_REGISTRY_ADDR` | registry 端点（插件注册） | **自动**：调平台工具 `get_registry_addr`，取返回的 `registry_addr` |
| `GT_AUTH_TOKEN` | 注册鉴权 Bearer token | **唯一手填项**：来自平台配置 `GT_AUTH_TOKENS`（格式 `owner=token`）；agent 托管（`GT_TUNNEL=1`）下平台自动注入，可留空 |
| `GT_DECODER_ADDR` | 解码器本地监听地址 | 插件**自己**决定：仅本机回拨用 `127.0.0.1:<port>`；宿主在别处/容器要回拨用 `0.0.0.0:<port>` |
| `GT_DECODER_PUBLIC_ADDR` | 注册时上报、宿主回拨的地址 | **host 自动**：取 `get_registry_addr` 返回 `registry_addr` 的 host 段（外部可达主机，与平台 `GT_PUBLIC_HOST` 一致）；端口默认 `61887` |

确认步骤（自动优先）：

1. 问用户平台部署形态：**本机单机** / **Docker 局域网** / **远端公网**。
2. 调 `get_registry_addr`（参数 `host` 传前端 `window.location.hostname`；公网/Docker 部署平台已配 `GT_PUBLIC_HOST`，可省略）→ 返回 `registry_addr` 直接填 `GT_REGISTRY_ADDR`，其 host 段就是 `GT_DECODER_PUBLIC_ADDR` 的 host。拿不到再让用户填。
3. `GT_AUTH_TOKEN`：**`.env` 留占位**，提醒用户从平台 `GT_AUTH_TOKENS` 取当前用户 token 填入；agent 托管可留空。
4. `GT_DECODER_ADDR` / 端口：问插件进程**运行在哪**、宿主能否回连；拿不准时默认 `0.0.0.0:61887` / `<外部可达host>:61887`。

> 平台侧已暴露 `get_registry_addr`（经 CaptureControl 走 `GT_PUBLIC_HOST`/`GT_PUBLIC_REGISTRY_PORT` 通告），地址无需手填。若后续平台把「插件启动所需 env 全集」一次下发，则 token 也免手填；在此之前，仅保留 token 一项由用户填写。

#### 0.1 用 `.env` 集中管理连接配置（推荐）

不要在代码里写死地址；插件目录放 `.env`（提交模板 `.env.example`），main.go 启动时加载。

**自动回填地址、只留 token 手填**：让用户（或引导 agent）调 `get_registry_addr`，把返回的 `registry_addr` 与其 host 段分别填到下面 `GT_REGISTRY_ADDR` 与 `GT_DECODER_PUBLIC_ADDR`；`GT_AUTH_TOKEN` 是**唯一需手填项**，取平台 `GT_AUTH_TOKENS` 里当前用户的值（agent 托管可留空）：

```
# .env.example —— 复制为 .env。地址按「步骤 0」调 get_registry_addr 自动回填；
#               GT_AUTH_TOKEN 是唯一手填项（agent 托管 GT_TUNNEL 下可留空）。
#               同名环境变量优先于本文件。
GT_REGISTRY_ADDR=<get_registry_addr 返回的 registry_addr，如 127.0.0.1:19091>
GT_AUTH_TOKEN=<token>   # ← 唯一需手填：平台配置 GT_AUTH_TOKENS 里当前用户的值；agent 托管可留空
GT_DECODER_ADDR=0.0.0.0:61887
GT_DECODER_PUBLIC_ADDR=<registry_addr 的 host 段>:61887   # 即外部可达主机，与平台 GT_PUBLIC_HOST 一致
```

> 提醒：不要提交 `.env` 到 git——token 是该用户凭证，提交模板只放 `.env.example`（token 为占位空值）。

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

协议笔记写进插件注释与 plugin.yaml 的 `hints`。

### 2. 插件骨架

目录：`plugins/<protocol>-decoder/`，文件清单：

```
plugins/<protocol>-decoder/
├── go.mod          # 依赖 gt-plugin-sdk v0.8.2
├── main.go         # 入口：RunRegisterLoopWithOptions + loadDotEnv(".env")
├── decode.go       # 核心：Decode(req) → []*Event
├── <fmt>.go        # 负载解析器（解压/解帧/解文本）
├── plugin.yaml     # manifest：schemas + semantic_rules
├── .env.example    # 连接配置模板：复制为 .env 后调 get_registry_addr 自动回填地址，仅 token 手填（见步骤 0）
├── <fmt>_test.go   # 解析器单测
└── decode_test.go  # 全链路解码测试 + manifest 一致性
```

go.mod：

```go
module your.org/plugins/foo-decoder

go 1.26

require github.com/OwnSecurityGuard/gt-plugin-sdk v0.8.2
```

main.go（入口必须是 `sdk.DecodeFuncV2` 函数，不是实例；每个 input 必须以 `done=true` 收尾，即使一条消息都没解出来）：

```go
package main

import (
	"os"
	"strings"

	"github.com/OwnSecurityGuard/gt-plugin-sdk"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/event"
	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
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
		// v0.8.0 契约：Payload（纯业务）与 Meta（方向等）分开传输，
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
			SchemaId:       e.SchemaID,
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
	SchemaID       string         // 对应 plugin.yaml schemas[].id，如 "wesnoth.message"
	Payload        map[string]any // 业务字段（payload 根对象），纯业务，不带平台字段
	Meta           map[string]any // 元信息（direction 等），前端「元信息」弹窗展示
	CorrelationKey string         // 会话标识：seg.Flow.Canonical()，宿主按连接配对
}
```

解码核心骨架：

```go
import (
	"sync"

	"github.com/OwnSecurityGuard/gt-plugin-sdk/framing"
	pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
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
		ev.CorrelationKey = seg.Flow.Canonical() // 会话标识（双向往返同值）
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
- 事件必须设 `CorrelationKey = seg.Flow.Canonical()`，宿主才能按连接配对。
- 长度字段注意字节序（wesnoth 是 big-endian）。

**UDP 场景**（与 TCP 差异，SDK 已处理好，解码器只需注意语义）：

- `framing.ExtractL7` 对 UDP 同样返回 Segment（`IsTCP=false`、`Seq/Flags` 为零）；每个 UDP 包是**自包含**的，`Reassembler.Push` 对 UDP 直接透传（返回整包负载、不进入重组缓冲），`Consume` 为 no-op。
- UDP **没有**连接握手与跨段重组：每包独立解码，包内第一个字段即业务数据，不存在「首 4 字节握手」逻辑（`seen` 簿记只对 TCP 有意义）。
- 方向判定与 TCP 相同（按端口），`CorrelationKey = seg.Flow.Canonical()` 同样设置（宿主按五元组会话配对）。
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
- 原始文本等辅助信息：默认进 **Meta**；仅当用户明确要求保留原文时，才以 `_raw` 字段进入 schema（声明 `optional: true`）并截断上限（如 8KB）。

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
  schema: true
schemas:
  - id: foo.message
    version: 1
    name: Foo message
    strict: false
    fields:
      msg_type:
        type: string
        queryable: true
        description: 消息类型（第一个子 tag 名）
semantic_rules:
  - id: foo.name_msg
    when:
      - path: msg_type
        op: exists
    effect:
      type: name
      key: msg_type
```

- `schemas` 必须显式声明 `strict`（true/false），字段类型用受控枚举（string/int/bool/...）。
- `semantic_rules` 用 `name` 提取消息名（写 `meta.msg_name`）、`pair`/`annotate` 声明配对与角色（request/response/notification）。格式参考 godot-ecs 插件。
- `hints` 帮助平台匹配：传输层、压缩、定界方式、端口。
- 注册前宿主会校验 manifest；规则或 schema 校验失败会在宿主日志输出（`semantic rules:` 前缀），注意查看。

### 6. 测试（质量门槛）

必须覆盖以下场景，缺一不可（标注 TCP 的条目仅对 TCP 协议适用，UDP 插件替换为分包独立/无握手用例）：

1. 解析器单测：正常输入、转义、注释、续行、坏输入不 panic。
2. 解压单测：gzip 与 bzip2 固件（固件生成用临时脚本，跑完删除）。
3. 全链路：单帧解码、**一个消息跨多个 TCP 段**（reassembly，TCP）、同包多帧。
4. 握手（TCP）：握手被消费、mid-stream attach（无握手段）。
5. 畸形输入：非法长度、截断、压缩损坏 → 不 panic、不无限循环。
6. **多连接**：两条独立连接互不干扰，correlation_key 不同。
7. **5-tuple 复用（重连，TCP）**：SYN 后新连接握手被再次正确消费（回归坑 1/坑 2）。
8. manifest 一致性：解出的 payload 字段都在 schema 中声明。
9. **Payload 纯度**：断言 payload 中不含协议外字段（对照 4.2 硬约束）。

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
- [ ] 事件设了 CorrelationKey
- [ ] 测试覆盖：跨段重组、多连接、5-tuple 复用、manifest 一致性（UDP 插件加：分包独立、无握手）
- [ ] `go vet` + `go build` + `go test -count=1` 全过
- [ ] 宿主日志无 semantic rules error；前端消息名正确显示
