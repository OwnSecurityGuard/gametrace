---
name: "decoder-plugin-guide"
description: "指引用户或 AI Agent 用 Go 编写解码插件（gt.decoder/v2，基于 gametrace/sdk）接入 GameTrace 平台：协议分析、编码前必交的数据包处理链路、Event 字段与持久化声明、插件骨架、TCP 重组与握手处理、plugin.yaml、decode 诊断指标、兜底判断、必交材料清单、测试与验证；§0 为 Agent 执行工作流——GameTrace MCP 工具时序（scaffold/connect/test/verify 的两阶段验证、落库验证路径、插件源码与进程都在用户本机/workspace、平台不保存源码也不编译不拉起插件）。semantic_rules 的选取标准、约束、校验机制与验收标准见 §5.2–§5.6。当用户要为新协议编写解码插件、接入自定义游戏协议、解析网络协议为业务事件，要编写/审查/修正 semantic_rules，或 Agent 要经 MCP 开发/运行/验证解码插件时调用。"
---

# GameTrace 解码插件开发指南

## 目的

指导用户把任意 TCP/UDP 网络协议接入 GameTrace 平台。解码插件把抓包帧转成结构化业务事件，宿主（gt-pipeline）负责 TCP 重组以外的平台职责：语义规则执行、事件配对、状态分析、前端展示。

- 语言：Go 1.25.5+（对齐 SDK 模块 `sdk/go.mod` 的 go 指令；宿主 gt-pipeline 自身用 1.26.8，与插件作者无关，插件是独立进程/独立 module）
- SDK：`github.com/OwnSecurityGuard/gametrace/sdk` v0.10.0
- API 版本：`api_version: gt.decoder/v2`
- 插件形态：独立可执行文件，通过 Register RPC 向 registry 注册

## 何时使用

- 用户要编写新的解码插件接入平台
- 用户要解析某个游戏/应用的网络协议为结构化事件
- 用户询问如何让自定义协议在平台中解码、显示、配对
- 参考插件：`examples/http-decoder`、`examples/ws-decoder`、`examples/lp-decoder`（本仓库 TCP 模板，plugin.yaml 每条语义规则均先陈述协议事实）；wesnoth 解码器的规则注释是"证据注解"范式（§5.2 有摘录）

## 0. Agent 执行工作流（MCP 工具优先，先读再动）

本节不是协议开发知识，而是通过 GameTrace MCP 完成插件开发的**实际执行协议**：什么阶段调用哪个工具、什么结果代表成功、什么情况下不要调用。

### 0.1 两类工具别混

- **GameTrace MCP 工具**：抓包/会话、插件接入、运行验证、事件查询（`scaffold_plugin` / `connect_plugin` / `status_plugin` / `test_plugin` / `verify_plugin` / `sample_bytes_plugin` / `list_decoded_data` 等）。
- **Agent 自身工具**：读源码、写文件、跑 `go test` / `go vet`、访问协议官方文档。写代码、跑单测是 Agent 自己的事，不走 MCP。

> 平台初始化 instructions 恒定声明：**"Plugin source code is created in YOUR workspace: GameTrace does not store plugin source code, nor does it compile or launch plugins."**

本 skill 本身会注册为 MCP resource（`gametrace://skills/decoder-plugin-guide`），MCP 初始化即可读；`get_plugin_dev_guide` 是开发指南的 MCP 版本，本 skill 已含工作流时不必重复拉取整份文档——需要核对 SDK/平台最新契约细节时再读。

### 0.2 工具时序（按插件推进阶段）

| 阶段 | 该做什么 | 不要做什么 |
|---|---|---|
| 协议分析 | 优先找**已有抓包**：`get_session_status` / `list_all_sessions` 确定可用 session → `sample_bytes_plugin` 拿字节事实（≤20 包、每包 ≤64 字节）→ 需要**连接级事实**（重组后的完整帧）再 `list_connection_frames` | 不为"写插件"默认 `start_capture`；`sample_bytes_plugin` 只能形成协议**假设**，不构成帧结构/握手已确认的证据——确认靠协议源码/文档/`test_plugin` |
| 编码 | `scaffold_plugin` 拿模板内容（`{template, files, contents, sdk_version, framing_available}`）→ 把 `contents` 每个 key 作为相对路径写进**自己的 workspace** → 补齐 decode.go / 解析器 / 测试 / `.gitignore`（见 0.3）→ Agent 自己跑 `go test` / `go vet` | 不反复 `get_capabilities` 探索工具（只在不确定工具职责时调一次） |
| 构建 | 在**本机** `go build` 出二进制；编译错误看本机 go 输出直接修 | 普通编译错误不调 `explain_plugin`（它做的是运行期归因，不是编译器） |
| 运行 | 本机设置 `GT_REGISTRY_ADDR`（`get_registry_addr` 的对外地址）/ `GT_TUNNEL=1` / `GT_AUTH_TOKEN`（`get_plugin_env` 的属主 token）→ **自己启动插件进程** → `connect_plugin` 让平台确认 | `get_registry_addr` / `get_plugin_env` **只在准备实际启动插件时调**；协议分析阶段不需要 |
| 验证 | `test_plugin` → `verify_plugin`（见 0.5 两层语义） | 把 `verify_plugin` 当成"事件已落库" |

### 0.3 `scaffold_plugin` 只是最小骨架

`scaffold_plugin` **只渲染模板内容并返回** `{template, files, contents, sdk_version, framing_available}`，**不在平台落盘，也没有指定输出目录的参数**。它当前只含 3 个文件：`go.mod`、`main.go`、`plugin.yaml`。Agent 必须把返回的 `contents` 里**每个 key 作为相对路径**写进**自己的 workspace**；`decode.go`、解析器文件、`docs/packet-line.md`、单测、`.env`、`.gitignore` 都是 **Agent 自行补齐**的交付物（清单见 §2 / §7.3），不要误判"骨架已完整"。

平台**不保存插件源码、不编译、不拉起插件进程**，也没有插件目录——`scaffold_plugin` 的产物落在你自己的机器上，后续构建、启动、验证都在本机完成（§0.4）。

### 0.4 插件在你自己的机器上运行

- **插件源码、二进制、进程全部在你的机器上**：`scaffold_plugin` 拿模板内容写进自己的 workspace → 本机编码 + 单测 → 本机 `go build` 出二进制 → 本机设置 `GT_REGISTRY_ADDR`（`get_registry_addr` 的对外地址）/ `GT_TUNNEL=1` / `GT_AUTH_TOKEN`（`get_plugin_env` 的属主 token）→ **自己启动插件进程** → `connect_plugin` 让平台连接它。平台**不 exec、不注入、也不编译**，只轮询 registry 的接入状态。
- **`connect_plugin` 只给一个结论**：`status="ready"`（已接入）或 `status="failed"`（带 `stage` 失败环节——取值 `auth` / `connection` / `manifest`——+ `reason` 原因 + `next[]` 可执行步骤）。`failed` 时先按 `next` 处理（多为刷新 token / 重启插件），仍不通再 `status_plugin` → `explain_plugin` 诊断。
- **远程机器运行**：插件进程始终跑在**用户自己的机器**上（可能不是调用 MCP 的那台）。跨机时用 `get_registry_addr` / `get_plugin_env` 取**对外可达**的 registry 地址与属主 token，由**用户自己在插件所在机器**设置并启动进程，再用 `connect_plugin` 确认；`list_registered_plugins` / `get_plugin_manifest` 复核注册。平台不做任何远程启动。

### 0.5 验证分两层（关键：哪些工具不落库）

- **第一层 快速验证（离线回放，不落库）**：`test_plugin`——看"到底解出了什么"（`decoded` / `decode_errors` / `type_histogram` / `sample_events` / `error_samples`）；再 `verify_plugin`——看分层结论：`session_profile`（这个会话有多少包、命中 target port 多少）→ `applicability`（会话是否适用）→ `checks`（decode / semantic 两条轴）→ `verdict`。两者都是对离线会话的隔离回放，**不修改 session events**。连 `sample_events` 都没看就直接 verify，容易陷入"verdict=warn 但不知道改哪"。
- **`verdict=not_applicable` ≠ 插件坏了**：它表示该会话窗口里没有插件该解的流量（`applicability.result=not_match`，`quality` 为 `null`）。这时要做的是**换一个带该协议流量的会话重跑**，不要去改插件。
- **quality 的分母**：`quality.input.raw` 是窗口内全部原始包，`quality.input.candidate` 才是命中 target port、插件真正该解的包；`quality.decode.*`（含 `unknown_ratio`）只对 candidate 统计。看到 `unknown` 很多先看 `candidate` 是多少——candidate 很小说明问题在会话选择，不在解码。
- **第二层 持久化验证（真实落库）**：要看 `list_decoded_data` 的真实宿主结果，必须先产生持久化事件——要么 live capture（插件 `connect_plugin` 返回 `status=ready` 后在测试 session 使用该插件并产生流量），要么 `decode_raw_packets`（**raw-debug 能力，服务端 `-enable-raw-debug` 才注册，默认不存在**）→ 然后 `list_decoded_data` 下钻、`get_protocol_catalog` 做解码成功后的协议索引。

`get_protocol_catalog` 是**成功解码后的索引**（msg_name 分布、字段、pair 覆盖），不是未知协议的分析入口；`unnamed_events > 0` 优先查 name 规则或 payload，不是猜协议。

### 0.6 构建与启动都在本机（平台不编译、不拉起）

插件是**独立进程、独立 module**，构建与启动完全在你的机器上完成：源码就绪后在本机 `go build` 出二进制，由你自己启动进程（自行提供 `GT_REGISTRY_ADDR` / `GT_TUNNEL` / `GT_AUTH_TOKEN`），再用 `connect_plugin` 让平台确认接入。**平台不保存插件源码、不编译、不拉起插件进程**——任何"改过源码要不要让平台重新构建"的顾虑都不成立：改完源码就在本机重新 `go build` 并重启插件即可。

### 0.7 最小默认链（本机新插件）

```
读本 skill → scaffold_plugin（把 contents 写进自己的 workspace）→ 编码+单测 → 本机 go build
→ 本机设 GT_REGISTRY_ADDR / GT_TUNNEL / GT_AUTH_TOKEN 并启动插件 → connect_plugin
→ test_plugin → verify_plugin
→ （需要真实落库时）live capture / decode_raw_packets
→ list_decoded_data → get_protocol_catalog
```

问题路径才加：`status_plugin` / `explain_plugin` / `get_registry_addr` / `get_plugin_env` / `sample_bytes_plugin` / `list_connection_frames`。不要为"完整"把所有工具调一遍。

### 0.8 插件运行环境详情（4 变量 / .env / 加载器 / 注册排错）

插件通过环境变量连接平台。插件在**用户自己的机器**上运行，这些环境变量**由用户自己提供，平台不代注入**：从 `get_registry_addr` 取**对外可达**的 registry 地址，从 `get_plugin_env` 取属主 token，然后在**插件所在机器**上设置后启动插件。协议分析、编码、单测阶段都不需要它们（时序见 §0.2）。

**平台统一以隧道模式运行插件**：用户在本机启动进程时设置 `GT_TUNNEL=1`，
插件不起本地端口、宿主不回拨，解码流量与注册/心跳共用同一条连接 —— 插件在 NAT / 容器 / 手机
后面也能直接接入。插件代码只透传 `GT_TUNNEL`，**不要**在代码里分支判断模式。

| 变量 | 含义 | 获取方式 |
|---|---|---|
| `GT_REGISTRY_ADDR` | registry 端点（注册 + 心跳 + 隧道帧都走它） | `get_registry_addr` 的**对外可达**地址 |
| `GT_TUNNEL` | 非空即隧道模式；由**运行方**注入 | 用户在本机启动插件时设为 `1`，插件代码只读透传 |
| `GT_AUTH_TOKEN` | 注册鉴权 Bearer token | `get_plugin_env` 返回调用者自己的**属主 token**；匿名模式（平台未配 token）下为空属正常 |

确认步骤：

1. 问用户平台部署形态：**本机单机** / **Docker 局域网** / **远端公网**。
2. 调 `get_registry_addr`（跨机部署、插件与调用方不在同一台机器时，参数 `host` 传插件所在机器视角的可达主机；平台已配 `GT_PUBLIC_HOST` 的公网/Docker 部署可省略）拿对外地址，`get_plugin_env` 拿属主 token，交由用户在**插件所在机器**上设置。
3. 写错了也不怕：用户在插件机器上设置 `GT_REGISTRY_ADDR` / `GT_TUNNEL=1` / `GT_AUTH_TOKEN` 并启动进程，跑起来后调 `connect_plugin`——只看 `status=ready|failed`；`failed` 时按返回的 `stage` / `reason` / `next[]` 处理（多为刷新 token / 重启插件）。隧道模式下「注册成功但一直不在线」的典型原因是 Connect 没建起来（插件用旧 SDK 编译、缺 `instance_id`，宿主拒流）——升级 SDK 在本机重新 `go build` 并重启。

#### 0.8.1 用 `.env` 集中管理连接配置（推荐）

不要在代码里写死地址；插件目录放 `.env`（**直接生成，不再用 `.env.example` 占位模板**），main.go 启动时加载。

**由用户在本机填写**：调 `get_registry_addr` 拿对外可达的 registry 地址、`get_plugin_env` 拿属主 token，写进插件所在机器的 `.env`（或同名环境变量）：

```
# .env —— 由用户在插件所在机器设置；同名环境变量优先于本文件。
GT_REGISTRY_ADDR=<get_registry_addr 的对外地址>
GT_AUTH_TOKEN=<get_plugin_env 的属主 token；匿名模式可留空>
GT_TUNNEL=1                     # 用户在本机启动插件时注入；插件代码只透传
```

> 提醒：`.env` 含用户 token，**不提交 git**——`scaffold_plugin` 只返回 3 个模板文件、不含 `.gitignore`，Agent 自行创建（内容一行 `.env`），不要省略。

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

## 平台契约（先读，避免返工）

- **事件三分离**：Payload（业务字段）/ Meta（方向、msg_name 等平台元信息）/ Analysis（配对、状态变更分析）。Payload 由插件负责；Meta / Analysis 插件**可以提供**（协议元信息、`_state_changes` 等分析 hint，见 SDK `event.Draft`），宿主会补齐/规范化——方向判不了就留空由宿主按端口补齐，别猜；Analysis 的配对/投影最终由宿主执行。
- **消息名称**由 `name` 语义规则从 payload 提取（宿主写入 `meta.msg_name`），解码器不硬编码；解码器也可在 Meta 里提供 `msg_name`（有真实依据时），规则提取优先。
- **方向**：解码器可在事件上标注方向，宿主按端口/连接补齐。
- **配对**：宿主按 `correlation_id`（同一连接会话）与 `causation_id`（请求-响应）配对，前端左右并排展示。
- **Payload 必须是 JSON 可表达结构**：平台的语义规则（semantic_rules）、查询、前端展示全部基于 JSON path 提取，因此无论线格式是二进制、文本还是 key=value，解码器都**必须**把业务字段解析为 JSON 对象（string / int / bool / 数组 / 嵌套对象）。
- **硬约束（无论什么情况）**：Payload 只允许包含**原本游戏协议真实存在**的字段——字段名与协议属性名一致（或与用户确认的映射名），值是协议中实际传输的内容。**禁止**添加协议不存在的字段：解码器推导值、平台时间戳、内部 ID、原始文本副本等一律不得进入 Payload；这类辅助信息走 Meta（方向等平台字段）或 `_raw`（仅当用户确认保留原文时，且声明为 optional）。

## 工作流程

### 1. 协议分析（必做，占一半工作量）

写代码前必须确认线格式，优先**读官方客户端/服务端源码**（最权威），其次抓包。产出协议笔记，明确：

1. 传输层与帧定界：TCP/UDP？固定头 + 长度字段 / 分隔符 / 定长？
2. 连接握手：首个包是否有固定字节（如 4 字节握手）？哪个方向？
3. 负载是否压缩/加密：gzip / bzip2 / TLS / 自研算法？
4. 方向判定依据：固定服务器端口 / 标志位 / 无法判定（由宿主补齐）？
5. 语义证据：候选 semantic_rules 的出处（哪个消息、哪个字段、哪一侧；双向都发的标签单独标记）。

协议笔记写进插件注释与 plugin.yaml 的 `hints`；另产出一份**语义规则证据表**（每条候选规则标注依据来源；证据不足的标「待确认」），它是 §5.3 四道准入门的输入——写 plugin.yaml 前与用户对齐，不猜。

#### 1.1 编码前必交：Packet 处理链路（先写链路，再写解析器）

**禁止**用 protobuf/默认结构体推导代替协议分析——"proto 长什么样就默认怎么解"是实际踩过的最常见坑。开始写 `decode.go` **之前**，必须先产出一份**数据包处理链路**（口头跟用户过一遍不算，要写下来进插件目录，如 `docs/packet-line.md`），把"拿到一个包，接下来每一步做什么"完整想清楚：

1. **包头字段表**：每个字段的偏移、长度、字节序、取值范围、含义。逐字段列出，不跳。形式：
   ```markdown
   | 偏移 | 长度 | 字节序 | 字段 | 含义 |
   |------|------|--------|------|------|
   | 0    | 1    | —      | kind  | 消息类型（1=心跳/2=登录/3=移动）|
   | 1    | 4    | BE     | body_len | 负载长度（含 body 头）|
   | 5    | 1    | —      | flags | 位标志（bit0=推送/pull，bit1=压缩）|
   | 6    | n    | —      | body  | 负载体，按 kind 分型解析 |
   ```
2. **转化链路**：原始字节 → 剥头 → 重组 → 解析 → `event.Value` JSON，每步用什么、产出什么。明确"哪一帧/哪一段字节对应哪个 JSON 字段"。
3. **JSON 化规则**：如何变成人类可读 JSON（字段名映射、枚举→字符串还是留数字、嵌套对象拆分）。让测试人员看完 JSON 能还原业务的真实含义。
4. **服务端推送的处理**：推送类数据（无请求对应、服务器主动下发）怎么识别、怎么处理——是单独消息类型，还是复用同一类型加 `is_push` 语义？**在链路上先写清楚**再决定 `pair` 规则与 `annotate` 怎么写（推送不参与 `pair`，见 §5.4）。
5. **不知道的字段**：标明"待确认"，与用户对齐后再定，不猜。

链路文档是交付必交材料之一（见 §7.3 必交材料清单）；写不清链路 = 协议分析没过，不允许进入编码。

### 2. 插件骨架

**`scaffold_plugin` 只返回其中 3 个文件的内容**（`go.mod` / `main.go` / `plugin.yaml`，需由你写入自己的 workspace），其余（decode.go、解析器、`.env`、`.gitignore`、单测、docs/）都是 Agent 自行补齐的交付物（§0.3）。在**你自己的 workspace** 下，最终目录形态：

目录：`plugins/<protocol>-decoder/`，文件清单：

```
plugins/<protocol>-decoder/
├── go.mod          # 依赖 github.com/OwnSecurityGuard/gametrace/sdk v0.10.0
├── main.go         # 入口：RunRegisterLoopWithOptions + loadDotEnv(".env")
├── decode.go       # 核心：Decode(req) → []*Event
├── <fmt>.go        # 负载解析器（解压/解帧/解文本）
├── plugin.yaml     # manifest：semantic_rules
├── .env            # 连接配置：由用户在本机设置 GT_REGISTRY_ADDR / GT_TUNNEL / GT_AUTH_TOKEN（§0.8）
├── .gitignore      # 一行 .env——token 是用户凭证，不入库
├── <fmt>_test.go   # 解析器单测
└── decode_test.go  # 全链路解码测试 + manifest 一致性
```

go.mod（go 指令 ≥ SDK 模块要求——当前 SDK v0.10.0 对应 **`go 1.25`**（模板值）/ `go 1.25.5`（SDK go.mod 值）。**别写成 1.26**：插件是独立进程/独立 module，宿主 gt-pipeline 用 1.26.8 与插件作者无关）：

```go
module your.org/plugins/foo-decoder

go 1.25

require github.com/OwnSecurityGuard/gametrace/sdk v0.10.0
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
	// 连接配置优先 .env（见「§0.8」）；同名环境变量（如用户在本机注入的
	// GT_AUTH_TOKEN）优先于文件。
	loadDotEnv(".env")

	d := newDecoder()
	// GT_REGISTRY_ADDR 由 SDK 原生读取（隧道模式下注册/心跳/解码帧都走它）。
	// 这里只需显式传入鉴权 token；平台统一以隧道模式运行：
	// 用户在本机启动插件时注入 GT_TUNNEL=1，代码只透传不判断。
	sdk.RunRegisterLoopWithOptions(d.decodePacket, sdk.RegisterOptions{
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

#### 3.0 Event 字段与持久化声明（解码产物落库口径，写之前先读）

解码器发出去的事件最终落进宿主数据库。**插件作者必须知道"我发的每个字段去哪了、后面能不能查回来"**——否则会默认 proto 字段直接落库、设计出无法持久化的事件。宿主 `pkg/store` 的 `events` 表实际列如下（SQLite/PG 同一套）：

| events 列 | 来源 | 说明 |
|---|---|---|
| `id` | 宿主 Identity | 自动生成 UUIDv7，解码器不指定 |
| `session_id` | 宿主赋值 | 来自会话上下文 |
| `type` | `event_type` | 形如 `http.request`，解码器返回 |
| `source` | 宿主赋值 | 抓包来源标识 |
| `timestamp` | 宿主赋值 | 纳秒时间戳，解码器不指定 |
| `causation_id` | Trace | 宿主按 pair 规则写 `causation_id` |
| `correlation_id` | pair 规则 / `correlation_key` | 配对成功后覆盖为请求方事件 id；未配对时保留 `CorrelationKey` |
| `origin_id` | Trace | 派生事件链，当前解码器可忽略 |
| `context` | Context | `flow_id` / `raw_packet_id` / `message_ordinal` / `direction` 的 MsgPack |
| `payload` | Payload + Meta + Analysis **合并** | 见下方"存储口径" |
| `created_at` | 宿主赋值 | 入库时间 |
| `scenario_id` / `replay_id` | 宿主赋值 | 重放链路用，解码器不指定 |

**存储口径（关键）**：`payload` 列存的是 Payload/Meta/Analysis **三段合并后的扁平 MsgPack**（`event.MergeReservedKeys(Payload, Meta, Analysis)`），读取时宿主用 `event.SplitReservedKeys` 拆回 —— 因此：

- 解码器发的 Meta 键（如 `direction`）会以保留键形式落进 `payload` 列，**前端用 `list_decoded_data` 查回来的行，`meta` 字段是从 payload 拆出的**；
- **Meta / Analysis 不存在独立列**，不要假设"改了 Meta 就能单独更新某列"——事件是不可变、追加写入的；
- 协议业务字段与平台字段分属不同 PATH 空间（`data.*` 与 `_meta.*`），落库后 json path 互不污染；
- 状态类数据走 `_state_changes`（Analysis 通道保留键），宿主投影进独立的 `state_changes` 表；解码器**不能**直接写 `state_changes` 表。

对插件作者的实际约束：**你返回的事件上每个字段（event_type / payload 键 / meta 键）都要能在落库后通过 `list_decoded_data` 原样查回**。验收时用真实抓包会话查一遍（§7 宿主侧验证），对照这里的列映射确认没有字段丢在"只进日志不出库"的地方。

解码核心骨架（注意 panic 边界的分层：**stream 发送层（`decodePacket`）不要写吞掉 panic 的 recover**——SDK 的 `Decoder.DecodeV2` 外层已把 panic 转成该 input 的 `Error+Done:true`，插件内吞掉反而漏发 done、丢错误信息；内部 Decode 层的 recover 是把坏包降级为"空事件"的兜底，可留可去，但别在 stream 层吞）：

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

声明合法 ≠ 运行期生效。覆盖率验证分**两层**，工具和判据都不同：

- **A. 规则可命中性**（本节，离线可跑）：用真实抓包固件解码出的 payload + meta 回放 `rule.Evaluate`，验证 path 存在、value 真实出现、side 能命中、类型一致。这只证明**规则本身具备命中能力**（G3/G4 的机器验证），**不证明平台真的完成了配对**——pair 的运行期行为（ConnID 分片、pending 池、30s TTL、flush）是 Runtime Plane 的，`rule.Evaluate` 触碰不到。
- **B. 平台实际效果**（落库后）：真实宿主运行（live capture / `decode_raw_packets`）后用 `list_decoded_data` 检查 `meta.msg_name` / `meta.semantic` / `correlation_id` / `causation_id`。**pair 只有在真实事件上看到正确的 `causation_id`（响应方→请求方）与一致的 `correlation_id`，才算运行期配对成功**——A 层全绿但 B 层没配上，说明问题在规则声明之外的运行期语义（如 key 连接内不唯一、往返超 30s）。

规则写完后**必须**先跑 A 层产出覆盖率表，**把表交给用户逐条判定**；涉及 pair 的规则在有机会落库时补跑 B 层。0 命中的规则不得静默保留：删除 / 修正 / 在注释写明"已知未覆盖 + 原因 + 判定人"，三选一。

A 层两个指标，判据不同（示例代码可直接放进 `decode_test.go`）：

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
			if len(res.Names)+len(res.Pairs)+len(res.Semantics) > 0 {
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
9. **规则覆盖率回放（§5.5 A 层，必做）**：用真实抓包固件跑 `rule.Evaluate`，产出「逐条命中数 + name 实际生效」覆盖率表并**交用户判定**；0 命中规则不得静默保留。声明期校验（`gt.semantic.*`）不查语义事实，只过它不能证明规则有效。pair 规则在事件落库后补跑 §5.5 B 层（`list_decoded_data` 核对 `causation_id`/`correlation_id`）。
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

- **第一层 快速验证（不落库）**：`test_plugin`（session_id + plugin）看 `decoded` / `decode_errors` / `type_histogram` / `sample_events`——先确认"到底解出了什么"；再 `verify_plugin` 看分层结论（`session_profile` / `applicability` / `checks` / `verdict`，详见 §0.5）。这两个工具是离线隔离回放，**不写 events 表**。
- **第二层 持久化验证（真实落库）**：live capture 使用该插件产生流量（或 `decode_raw_packets`，需服务端 `-enable-raw-debug`）后，`list_decoded_data` 是 **machine-readable 验收面**——Agent 先按工具返回值核对 `payload`（业务字段）、`meta.msg_name`、`correlation_id` / `causation_id` 配对（pair 规则的运行期效果，§5.5 B 层）再下结论；同时观察宿主日志 `semantic rules:` 无 error 级问题。
- 前端协议数据页只作最终人工视觉确认，不作 Agent 的判定依据。

### 7.1 decode 诊断指标（跑起来后如何判断解码质量）

解码质量不是"切到页面看有没有数据"就能判断的。宿主把"解不开"的事实落库并暴露，插件作者必须会用这几个指标，不能只看成功数自我感觉良好：

| 指标 | 在哪里看 | 含义 | 常见误读 |
|---|---|---|---|
| 会话 `decode_errors` 计数 | `get_session_status` 的 `decode_errors` | 该会话累计解码失败次数（插件主动报错 + 链路层失败 + 解码器未接入合计） | `0` 只代表没有报错，不代表每条包都解出了事件——**还要看事件数**；`binding` 组按状态跳变计数，所以 `1` 也可能代表整场没解码 |
| 解码失败分组 | `list_decoded_data` 的 `decode_error_groups` / 宿主 `ReplaceDecodeErrorGroups` 落库表 | 按**归一化错误模板**聚合 `ErrorCollector`：每条含 `kind`（`plugin`=插件主动报错，`transport`=链路层失败，`binding`=会话绑定的插件没有可用实例，宿主侧产生、与插件代码无关）、`template`（模板，如 `unknown message type <n>`）、`count`、`sample`（首条原始错误） | 「有计数但看不到原因」= 没有用模板归一化，写错误时参数化了每次变化的部分 |
| 插件 `kind=plugin` 报错 | 错误响应的 `Error` 字段 | 解码器在 `done=true` 时附带 `Error` 即视为插件主动报错 | 把"这条解不了"当 `Error` 返回会让**整帧算失败**；能恢复的坏消息应跳过并继续，只有整帧不可解时才报错 |
| 事件产出数 | 会话 `raw_packets` vs `events` | 原始包 vs 解码事件 | 事件数远小于包数是正常的（ACK/握手/推送过滤），但**长期为 0** 要先看分组里有没有 `kind=binding`——那是插件压根没接上（未启动 / 离线 / owner 与项目不符），不是 framing 写错；确认接上了仍为 0，才回到链路分析（最常见的 framing 坑） |

**decode_errors 与 template 归一的验收要求**：错误消息必须参数稳定——固定前缀 + `<n>` 占位符（如 `unexpected EOF at offset <n>`），让大量同类失败聚合到同一 `template`；把每次不同的明文直接写进错误（如 `unexpected EOF at offset 12345`）会导致 `decode_error_groups` 每帧一条、`count` 永远 1，前端无法归因。SDK 的 `ErrorCollector` 已按模板哈希聚合并限量，插件只需按上面规范产错。

### 7.2 解码插件的兜底判断（坏包/未知协议不 panic，还要有明确去向）

解码器运行在宿主热路径上，**一个 panic / 死循环 / 无限内存增长会拖垮整条抓包链路**。兜底不是"try 一下"而是分层设计，四层缺一不可：

1. **帧解析层**（每个请求入口）：panic 边界交给 SDK 的 `Decoder.DecodeV2` 外层（panic → 该 input 的 `Error+Done:true`）——stream 层**不要**写吞 panic 的 recover，吞掉会漏发 done；`framing.ExtractL7` 返回 `!ok` 时直接 `done=true` 返回，不算错误。
2. **消息级容错**（解析循环内）：非法长度 / 截断 / 校验失败 → 跳过（`Consume` 部分或 `Forget` 整流）继续，不中断后续包；**不要**把"一条坏消息"升级为整帧错误（见 7.1 的 plugin 报错误读）。
3. **流状态兜底**：重并发/攻击性流量下,`Reassembler` 必须设上限（参考插件 4 MiB / 流），失步时 `Forget` 丢弃流状态让下一段重新自同步（§3 坑 3）；不会无限增长。
4. **协议覆盖兜底**：就是不认识的字节——**先喂现场抓包**（`sample_bytes_plugin`）确认协议范围，编码时对"未识别消息"要么**不产出事件仅 done**，要么**仅当用户确认后**产出事件并附 `Meta` 标注（如 `unknown`），绝不臆造 payload 字段。
5. **超时/迟到兜底**：宿主侧对无响应对应用侧兜底——`pair` 待配对 30s TTL（F7）；插件侧对**长轮询/异步**响应不声明 `pair`（无法在 30s 内配对）。

### 7.3 协议插件必交材料（交付清单，逐项验收）

插件交付 = 能跑的程序 + **能让人独立复核的证据链**。以下材料缺一不可，按序逐个核对（§1.1 链路、§5.5 覆盖率表在交付时要有最终版）：

| # | 材料 | 出处/要求 |
|---|---|---|
| 1 | `docs/packet-line.md`（或插件目录内同义文档） | §1.1：包头字段表 + 转化链路 + JSON 化规则 + 服务端推送处理 |
| 2 | 语义规则证据表 | §1：每条候选规则的依据（哪个消息/字段/哪一侧，源码还是抓包） |
| 3 | 覆盖率回放表 + 用户判定结果 | §5.5：逐条命中数 + name 实际生效，0 命中规则已处理 |
| 4 | `plugin.yaml` | `contract.NewPluginChecker().Check(m)` 零 violation |
| 5 | 单测 + 固件 | §6：跨段重组、多连接、5-tuple 复用、manifest 一致性、畸形输入 |
| 6 | 真实抓包样例（可选但强烈建议） | `sample_bytes_plugin` 采样，供 reviewer 直接用 `list_decoded_data` 复核 |
| 7 | 宿主侧验证结果 | §7：`semantic rules:` 无 error；`list_decoded_data` 字段能原样查回（§3.0 约束） |
| 8 | 诊断指标自查 | §7.1：会话 `decode_errors`、`decode_error_groups` 分组合理、模板参数稳定 |

**评审红线**：缺 §1.1 链路或 §5.5 覆盖率表的插件==协议分析未完成，不允许进入上线。

## 踩坑清单（完成前逐条自查）

- [ ] go.mod 的 go 指令 ≥ SDK 要求（当前 **1.25**；宿主用 1.26.8 与插件无关，勿写 1.26）
- [ ] **编码前已产出 Packet 处理链路文档**（§1.1：包头字段表 + 转化链路 + JSON 化规则 + 服务端推送处理）并进插件目录
- [ ] SYN/RST 空 payload 段 Push 给了 Reassembler（坑 2）
- [ ] SYN/RST 重置了握手簿记（坑 1；仅 TCP 需要，UDP 跳过）
- [ ] 长度字段字节序正确（wesnoth 为 big-endian）
- [ ] 解析器不 panic：宽容解析 + 消息级跳过；stream 层（decodePacket）**没有**吞 panic 的 recover（panic 边界归 SDK 外层，吞掉会漏发 done）
- [ ] 已用 `scaffold_plugin` 拿到模板内容并把 `contents` 写进自己的 workspace；它只含 3 个文件，decode.go / 解析器 / `.env` / `.gitignore` / 单测 / docs 已自行补齐（§0.3）
- [ ] 已在本机 `go build` 并启动插件（自行设置 `GT_REGISTRY_ADDR` / `GT_TUNNEL` / `GT_AUTH_TOKEN`），并用 `connect_plugin` 确认 `status=ready`（§0.4）
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
- [ ] **诊断指标已核对**（§7.1）：会话 `decode_errors`、`decode_error_groups` 分组合理、错误模板参数稳定（`<n>` 占位符）
- [ ] **兜底判断四层齐备**（§7.2）：recover + ExtractL7 !ok 回 done；消息级跳过；Reassembler 上限 + Forget；未识别消息不臆造 payload
- [ ] **必交材料齐全**（§7.3）：packet-line 文档、语义证据表、覆盖率回放表、plugin.yaml、测试与固件
- [ ] **运行实例 ↔ 本机源码一致**（§7.3）：`list_registered_plugins` 返回的运行实例与你的 workspace 中源码对应；**改过任何插件源码（decode.go / parser.go 等）就重新在本机 `go build` 并重启插件**（平台不编译、不保留二进制）
- [ ] `go vet` + `go build` + `go test -count=1` 全过
- [ ] 宿主日志无 semantic rules error；前端消息名正确显示
