---
name: "decoder-plugin-guide"
description: "指引用户或 AI Agent 用 Go 编写解码插件（gt.decoder/v2，基于 gametrace/sdk）接入 GameTrace 平台：协议分析、编码前必想清的 Packet 处理链路（形式不限）、Event 字段与持久化声明、插件骨架、TCP 重组与握手处理、plugin.yaml、decode 诊断指标、兜底判断、必交材料清单、测试与验证；§0 为 Agent 执行工作流——GameTrace MCP 工具时序（scaffold/connect/test/verify 的两阶段验证、落库验证路径、插件源码与进程都在用户本机/workspace、平台不保存源码也不编译不拉起插件）。语义规则是**按协议发现的可选项**：request/response 由平台按方向自动补，notification/error/pair/state_change 仅在协议真实存在该语义时声明，未发现是合法结果；选取标准与校验机制见 §5.2–§5.6。当用户要为新协议编写解码插件、接入自定义游戏协议、解析网络协议为业务事件，要编写/审查/修正 semantic_rules，或 Agent 要经 MCP 开发/运行/验证解码插件时调用。"
---

# GameTrace 解码插件开发指南

## 目的

指导用户把任意 TCP/UDP 网络协议接入 GameTrace 平台。解码插件把抓包帧转成结构化业务事件，宿主（gt-pipeline）负责 TCP 重组以外的平台职责：语义规则执行、事件配对、状态分析、前端展示。

**语义定位（贯穿全文）**：插件的硬要求只有「把协议看懂」——**解码正确 + event_type 正确**（必须有，它是解码事件的基本身份）；**direction 能可靠判断就提供、判不了留空由宿主按端口补齐，绝不猜**；**msg_name 有稳定可提取字段才提供**（没有稳定字段就不给，不算缺陷）。注意区分：event_type ≠ msg_name——`name` 规则只是把更细的消息名写进 `meta.msg_name`，不写 name 规则、只有 decode + event_type 的插件依然合格。request/response 由宿主按 direction 自动补标，不需要插件写规则。notification / error / pair / `_state_changes` 是**按协议发现的可选语义增强**，不是必做盘点——协议里没有，就不做，「未发现」本身就是合格结果。整个语义系统是「协议解码增强项」，不是「插件必须完成的协议建模」。

对应地，全文的要求按适用面分层，**每层内才谈"缺一不可"**：基础必做（正确解码 + event_type；direction 确定时提供、msg_name 稳定可提取时提供 + 通用单测 + build/connect/test）→ 发现了才做（push/error/pair/state）→ 声明了什么才验证什么（§5.3、§6）。

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

`scaffold_plugin` **只渲染模板内容并返回** `{template, files, contents, sdk_version, framing_available}`，**不在平台落盘，也没有指定输出目录的参数**。它当前只含 3 个文件：`go.mod`、`main.go`、`plugin.yaml`。Agent 必须把返回的 `contents` 里**每个 key 作为相对路径**写进**自己的 workspace**；`decode.go`、解析器文件、处理链路笔记（§1.1，注释/README 或独立文档）、单测、`.env`、`.gitignore` 都是 **Agent 自行补齐**的交付物（清单见 §2 / §7.3），不要误判"骨架已完整"。

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

§0.4 的展开收束成一句：改完源码就在本机重新 `go build` 并重启插件进程即可——平台不保存源码、不编译、不拉起，任何"要不要让平台重新构建"的顾虑都不成立。

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

**凭证边界（硬性）**：`.env` 与 `.gitignore` 两个文件 Agent 可以创建，但 **Agent 创建的 `.env` 里所有值留空**——真实地址与 token 由用户在插件所在机器上填写。Agent **不得**把 `get_plugin_env` 等渠道拿到的真实 token 写进 `.env`、源码、注释、测试或任何可能提交的文件，也不得硬编码进代码。token 只存在于用户本机。

Agent 创建的 `.env`（空值占位）：

```
# .env —— Agent 建文件时值全部留空；由用户在插件所在机器填写真实值。
# 同名环境变量优先于本文件。
GT_REGISTRY_ADDR=
GT_AUTH_TOKEN=
GT_TUNNEL=1
```

`GT_TUNNEL=1` 是平台统一运行方式（非凭证），可以预填；其余两行留空。用户填写来源：`get_registry_addr` 的对外地址、`get_plugin_env` 的属主 token（匿名模式下 token 留空属正常）。

> 提醒：`.env` 含用户凭证，**不提交 git**——`scaffold_plugin` 只返回 3 个模板文件、不含 `.gitignore`，Agent 自行创建（内容一行 `.env`），不要省略。

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

- **语义 = 协议解码的增强项，不是必须完成的协议建模**：插件的硬要求只有"把协议看懂"——**解码正确 + event_type 正确**；direction 能可靠判断就提供、判不了留空由宿主按端口补齐；msg_name 有稳定可提取字段才提供。request/response 标签由平台按 direction 默认补；notification / error / pair / state_change 是按协议实际情况**可选实现**的增强，协议里没有就不做。
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
5. 语义发现：线格式弄懂后快速回答四个问题——协议有没有**主动推送**（服务端不经请求触发下发）、**明确业务错误语义**（有可证明失败的字段）、**持续实体状态**（等级/金币/背包等随时间变更）、**可靠关联的请求-响应键**（有回显）？答"有"才做对应增强；答"没有"就不做，笔记里记一句 none 即可。

协议笔记写进插件注释与 plugin.yaml 的 `hints`。不需要产出庞大的语义盘点表：要写的规则必须有依据（注释写明出处），没依据的候选直接不写、也不必列清单。**「没有发现」本身是合法结果**——最终报告里最多写几句 Semantic review（§5.3），全 none 也正常。

#### 1.1 编码前必须形成可复核的 Packet → Event 处理结论（形式不限）

**禁止**用 protobuf/默认结构体推导代替协议分析——"proto 长什么样就默认怎么解"是实际踩过的最常见坑。开始写 `decode.go` **之前**，必须形成一份可复核的处理结论：**拿到一个包，接下来每一步做什么**。硬门槛是**结论清晰**，不是文档形式：插件注释、README、`docs/packet-line.md` 任选，简单协议（固定头 + 长度字段 + 单一编码）几句话讲清即可。

结论必须能回答这 5 个问题（这就是全部硬要求）：

1. **定界**：怎么知道一条完整消息从哪开始、到哪结束——长度前缀 / 分隔符 / 定长？字节序是什么？
2. **重组**：跨 TCP 段的字节流怎么拼回完整消息（用 SDK `framing.Reassembler`，不手写）？握手/连接重置怎么处理？
3. **哪些字节进入业务解析**：链路层与头部剥掉后，从第几字节开始才是业务负载？负载里各字段的位置/长度/含义——简单协议一句话，复杂协议（多种定界、压缩加密、推送混布）才逐字段列表：
   ```markdown
   | 偏移 | 长度 | 字节序 | 字段 | 含义 |
   |------|------|--------|------|------|
   | 0    | 1    | —      | kind  | 消息类型（1=心跳/2=登录/3=移动）|
   | 1    | 4    | BE     | body_len | 负载长度（含 body 头）|
   | 5    | 1    | —      | flags | 位标志（bit0=推送/pull，bit1=压缩）|
   | 6    | n    | —      | body  | 负载体，按 kind 分型解析 |
   ```
4. **JSON 字段来源**：输出的每个 JSON 字段对应包里哪段字节？字段名映射依据是什么？枚举留数字还是转字符串——看完 JSON 能还原业务含义。
5. **push / 压缩 / 加密存不存在**：有推送就写清识别方式（不参与 `pair`，见 §5.4）；有压缩/加密写明在哪一步解、用什么解；没有的记一句 none 就结束，不必去"找出所有 server→client 消息再判断 notification"。

答不出的字段标"待确认"，与用户对齐后再定，不猜。链路结论是交付材料之一（见 §7.3 材料 1，落在哪由协议复杂度定）；回答不清这 5 问 = 协议分析没过，不允许进入编码——但这不构成对"独立文档 / 字段表"的要求。

### 2. 插件骨架

**`scaffold_plugin` 只返回其中 3 个文件的内容**（`go.mod` / `main.go` / `plugin.yaml`，需由你写入自己的 workspace），其余（decode.go、解析器、`.env`、`.gitignore`、单测、docs/）都是 Agent 自行补齐的交付物（§0.3）。在**你自己的 workspace** 下，最终目录形态：

目录：`plugins/<protocol>-decoder/`，文件清单：

```
plugins/<protocol>-decoder/
├── go.mod          # 依赖 github.com/OwnSecurityGuard/gametrace/sdk v0.10.0
├── main.go         # 入口：RunRegisterLoopWithOptions + loadDotEnv(".env")
├── decode.go       # 核心：Decode(req) → []event.Draft
├── <fmt>.go        # 负载解析器（解压/解帧/解文本）
├── plugin.yaml     # manifest：semantic_rules
├── .env            # 连接配置：Agent 创建时空值占位，用户在插件所在机器填真实值（§0.8.1）
├── .gitignore      # 一行 .env——token 是用户凭证，不入库
├── <fmt>_test.go   # 解析器单测
└── decode_test.go  # 全链路解码测试 + manifest 一致性
```

go.mod（go 指令对齐 SDK 模块 `sdk/go.mod`——当前为 **`go 1.25.5`**。**别写成 1.26**：插件是独立进程/独立 module，宿主 gt-pipeline 用 1.26.8 与插件作者无关）：

```go
module your.org/plugins/foo-decoder

go 1.25.5

require github.com/OwnSecurityGuard/gametrace/sdk v0.10.0
```

main.go（入口必须是 `sdk.DecodeFuncV2` 函数，不是实例；每个 input 必须以 `done=true` 收尾，即使一条消息都没解出来）：

```go
package main

import (
	"os"
	"strings" // loadDotEnv 用（见 §0.8.1，与 main.go 同文件）

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
//
// 传输层唯一正确姿势：内部 decode 层返回 []event.Draft，每条经 Draft.ToResponse
// 编码发送。ToResponse 一次性带全 Payload/Meta/Analysis/CorrelationKey/
// CausationInputID 五个通道——手拼 DecodeResponseV2 极易漏发 Analysis，
// 而漏发是静默失效：state_changes 投影、_meta 保留键全部丢，不报任何错。
func (d *decoder) decodePacket(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
	drafts, err := d.Decode(req)
	if err != nil {
		return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true, Error: err.Error()})
	}
	for _, draft := range drafts {
		resp, err := draft.ToResponse(req.GetInputId())
		if err != nil {
			return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true, Error: "draft: " + err.Error()})
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
	return stream.Send(event.Done(req.GetInputId()))
}
```

### 3. 解码核心与 TCP 重组（高频踩坑区，重点阅读）

解码入口接收 `*pb.DecodeRequest`（payload = **完整链路层帧**），返回 `[]event.Draft`（SDK 提供的事件草稿类型）。宿主把同一个 TCP 连接上的帧串行喂给插件，但**插件必须自己处理传输层与字节流重组**。使用 SDK 的 `framing` 包，禁止手写链路层剥离。

事件产出类型统一用 SDK 的 `event.Draft`，**不要再自定义一套内部 Event 结构**——自定义结构意味着要手拼 `DecodeResponseV2` 传输，而手拼必然漂移（§2 已述：Analysis/CausationInputID 漏发是静默失效）。`Draft` 字段：

```go
// sdk/event.Draft —— 插件侧事件产出；Identity/Trace 由宿主补齐。
type Draft struct {
	Type             EventType    // event_type，如 "wesnoth.login"（必填）
	Value            Value        // 业务载荷，根必须是 object（payload 通道）
	Meta             Value        // 元信息（direction / msg_name 等），可空
	Analysis         Value        // 分析通道（_state_changes 等），可空
	CorrelationKey   string       // 业务会话/操作标识（battle_id / txn_id）；不是连接标识，可空
	CausationInputID string       // 因果输入 id，可空
}
```

`Draft.Validate()` 强制两条最小不变量：Type 非空、Value 根为 object；`ToResponse(inputID)` 负责把五个通道全部编码进 `DecodeResponseV2`。

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
- 状态类数据走 `_state_changes`（Analysis 通道保留键），宿主投影进独立的 `state_changes` 表；解码器**不能**直接写 `state_changes` 表。**不要为了使用 state_change 而硬造 state_change**——只有协议存在持续、可识别、对调试有价值的业务状态时才产出（判据见 §5.4）。

对插件作者的实际约束：**你返回的事件上每个字段（event_type / payload 键 / meta 键）都要能在落库后通过 `list_decoded_data` 原样查回**。验收时用真实抓包会话查一遍（§7 宿主侧验证），对照这里的列映射确认没有字段丢在"只进日志不出库"的地方。

解码核心骨架（注意 panic 边界的分层：**stream 发送层（`decodePacket`）不要写吞掉 panic 的 recover**——SDK 的 `Decoder.DecodeV2` 外层已把 panic 转成该 input 的 `Error+Done:true`，插件内吞掉反而漏发 done、丢错误信息；内部 Decode 层的 recover 是把坏包降级为"空事件"的兜底，可留可去，但别在 stream 层吞）：

```go
import (
	"sync"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
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

func (d *decoder) Decode(req *pb.DecodeRequest) (drafts []event.Draft, err error) {
	drafts = []event.Draft{}
	defer func() { if r := recover(); r != nil { drafts = []event.Draft{} } }() // 兜底：坏包不 panic

	seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
	if !ok {
		return drafts, nil
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
		return drafts, nil
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
		draft := d.decodePayload(raw[4:4+n], seg) // 返回 event.Draft
		// 不设 CorrelationKey：连接身份由宿主派生的 ConnID 承担。
		// 该字段只在协议里有真正的业务会话/操作 id 时才填。
		drafts = append(drafts, draft)
		s.Consume(4 + int(n))
	}
	return drafts, nil
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

- `semantic_rules` 是接入平台语义能力的入口，effect 闭集 3 类：`name`（消息名→`meta.msg_name`）、`annotate`（角色标签→`meta.semantic`）、`pair`（请求/响应配对→`correlation_id`+`causation_id`）。它是**可选增强**入口：`name` 属于"看懂协议"的基础（能稳定提取 msg_name 就写），`annotate`/`pair` 只在协议真实存在该语义时才写。接入要点见下节。
- `hints` 帮助平台匹配：传输层、压缩、定界方式、端口。
- 注册前宿主会校验 manifest；规则校验失败会在宿主日志输出（`semantic rules:` 前缀），注意查看。

#### 5.1 语义规则接入（semantic_rules）——复用平台的配对/命名/标注能力

规则 = Predicate(`when`) + Effect，**决定事件怎么被解释/关联，权重高于解码器硬编码**。effect 是一个闭集，插件只需声明，执行由平台完成。

Agent 的推进顺序——前三步是基础，第四步起才是按发现增补：

```
1 分析抓包/协议源码 → 2 解码消息 → 3 写 direction（request/response 平台自动补）→ 4 提取稳定 msg_name
→ 5 观察协议：有 Push？有 Error？有 State？有可靠 Pair？
→ 6 只实现"发现存在"的能力 → 7 用真实数据验证 → 8 结束
```

> **补充原则：能上规则不硬编码；但规则只为协议真实存在的语义声明——有证据就写，没证据就不写，"未发现"是合法结果，不为凑能力齐全而造规则。**
> 写出去的每条规则，先读 §5.2 的运行期执行事实——平台只校验声明形状，运行期失效是静默的。

| effect | 作用 | 产出(host) | 必填字段 |
|---|---|---|---|
| `name` | 从 payload 提取消息名 | `meta.msg_name` | `key` |
| `annotate` | 角色标签：request/response/notification/error。**request/response 有宿主默认**（按方向补标），annotate 用于覆盖角色/追加 error | `meta.semantic` | `semantic` |
| `pair` | 请求-响应配对（同一个“来回”） | `correlation_id`（双方相同）+ `causation_id`（响应方→请求方） | `sides`（恰好 2 个，每侧自带 `key`） |

**① `name` —— 消息名**（替代解码器写死）：

```yaml
- id: foo.name_msg
  when: [ { path: msg_type, op: exists } ]
  effect: { type: name, key: msg_type }
```

规则求值**先执行 name 并把结果注入 `_meta.msg_name`**，因此后续规则可用 `_meta.msg_name` 判定（见 `pair`）。

**② `annotate` —— 角色**：

request/response **不需要声明规则**：事件没有角色标签（request/response/notification）时，
宿主按方向补标（client_to_server→request、server_to_client→response；方向判不了不补）。
annotate 的职责是**覆盖与补充**——表达方向推不出的语义：

```yaml
- id: foo.mark_push
  when: [ { path: seq, op: eq, value: 0 } ]       # 服务器主动下发，方向推不出来
  effect: { type: annotate, semantic: notification }
- id: foo.mark_error
  when: [ { path: error_code, op: neq, value: 0 } ]
  effect: { type: annotate, semantic: error }     # error 非角色：与默认 response 并存追加
```

annotate 出的角色（含插件在 Meta 自报的 `semantic`）覆盖默认值；为 request/response
重复声明 annotate 规则是冗余的，仅在方向判定会出错（如双向复用同一消息）时才需要。

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
// 世界快照 → 每条实体状态一个 Draft（各自带完整 Meta 与 Analysis）
drafts := make([]event.Draft, 0, len(snapshot.Ents))
for _, ent := range snapshot.Ents {
    drafts = append(drafts, event.Draft{
        Type:  "entity_snapshot",
        Value: ent.Value(),
        Meta: event.ValueObject(map[string]event.Value{
            "msg_name":  event.ValueString("EntityState"),
            "direction": event.ValueString("server_to_client"),
        }),
        // 需要状态投影就挂 _state_changes（走 Analysis 通道）
        Analysis: event.ValueObject(map[string]event.Value{
            "_state_changes": ent.StateChanges(),
        }),
    })
}
// Decode 返回切片，decodePacket 逐条 ToResponse 发出（§2）；
// 每条 Draft 的 Analysis 都会完整传输，无需手拼 DecodeResponseV2。
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

**何时用哪个**（前提都是**协议里发现了该语义**，没发现就都不写）：跨消息的“一问一答”→ `pair`；消息名 → `name`（这项属于"看懂协议"的基础）；方向推不出的角色（推送）与状态属性（error）→ `annotate`（request/response 由宿主默认按方向补，不必为其建规则）。**发现之后，优先声明规则而非解码器硬编码**去表达这类语义，便于复用平台的配对/前端联动能力。同消息内“一拆多”（快照→多实体、批量→逐条）**不属于规则层**——见上「附」。

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

#### 5.3 规则选取：证据先行，验证看声明了什么

目标是"协议里真实存在的语义用规则表达，且每条都站得住"，**不是写得越多、越全越好**。默认流程只有四条发现式提问，每一项"没有"就直接跳过：

| 提问 | 答"有" | 答"没有" |
|---|---|---|
| **Push**：有没有不经请求触发的服务端主动下发，且判据能指到具体字段？ | 声明 `annotate: notification` | 不做 |
| **Error**：有没有能证明业务失败的字段（如可证实 `error_code != 0` 即失败）？ | 声明 `annotate: error` | 不做 |
| **State**：协议是否承载持续、可识别、对测试/调试有价值的实体状态？ | 解码器产出 `_state_changes`（§5.4） | 不做 |
| **Pair**：请求与响应之间有没有可靠回显的关联键，且 30s 内往返？ | 声明 `pair` | 不做 |

铁律只有一条：**写出去的任何一条规则必须有证据**——说得出出处（源码 > 真实抓包 > 文档）、注释能写明依据。没有证据的候选**直接不写**，不立清单、不为它问用户（除非用户主动要这个能力）。一个插件最终只有 `decode + name`，甚至只有 `decode`，完全合格。request/response 由宿主默认打标，**永远是"零规则"就有**的能力。

交付时在最终报告写几句 **Semantic review** 即可，不需要庞大的语义盘点表；全 none 是合格输出：

```
Semantic review:
- notification: found 2（ChatMessage/MailArrived，判据 seq==0）
- error: none found
- state_change: found player.gold（gold 1000→1200→1500 持续变更，值得投影）
- pair: found request_id
```

**验证深度按「声明了什么能力」触发，不按「协议复杂不复杂」触发**——前者客观可数，后者又要 Agent 猜：

| 插件声明了什么 | 就验证什么 | 其余一概不做 |
|---|---|---|
| 无 `semantic_rules`、无 `_state_changes` | 解码正确、event_type 正确；direction/msg_name 有则抽查 | 一切语义测试 |
| `name` | 真实数据上命中，且首条生效的是预期规则（F5） | 覆盖率表 |
| `annotate` | 每条有至少一次真实命中依据（判据见 §5.5 判定规则） | 覆盖率表 |
| `pair` | §5.4 五条判据 + 落库后 B 层实际配对（风险最高=验证重点） | —— |
| `_state_changes` | 落库后投影与契约一致（op/after 口径） | —— |

不需要单独的"复杂协议评审"层级：证据铁律在上面，路径/取值/效果的逐项判据已由 §5.2（F1–F10）与 §5.4 承载，再立一套门只是重复。

排序约束：先 `name` → 再 `annotate` → `pair` 放最后。pair 风险最高（错配会直接污染前端左右并排展示），**可以放到最后甚至不做**。

#### 5.4 各 effect 的判据与反例

**`name`** —— 判据：提取字段在**全部目标消息**里都存在且能区分消息。
反例：只在部分消息存在的字段当全局 name；声明多条 name 规则指望"互补"（F5：只有第一条生效）。

**`annotate`** —— push 只需抓住一个问题：**"这条 server→client 消息是不是不由某个刚发生的请求直接触发的主动通知？"** ChatMessage / MailArrived / GuildWarStarted / PlayerOnline 这类才是 notification；AttackResp 等回包即使也是 server→client，平台默认已标 response，不需要逐条扫所有下行消息去找 notification。error 同理：必须有可证明的业务失败语义（如 `error_code != 0` 即失败，注释写明依据来源），标 error 与默认角色并存；错误全靠普通 payload 表达、证明不了，就不做。协议里既没发现推送也没发现错误语义？**一条 annotate 都不写，正常**。
反例：为 request/response 建 annotate 规则（宿主默认已按方向补标）；凭"我觉得这是请求"标注；照搬别的协议的标签；为了"error 能力完善"给 `status`/`code`/`result` 字段硬编失败含义。

**`_state_changes`**（不是规则，是解码器的 Analysis 产出）—— 判据：协议中存在**持续、可识别、对测试/调试有价值**的业务状态才做。值得做：`gold: 1000 → 1200 → 1500` 这类同一实体字段持续变更，投影 `player.gold set`；不值得做：每条消息都是一次性结果（`{result:"ok", damage:120}`），没有稳定实体状态体系——**不要为了使用 state_change 而硬造 state_change**，"没有状态可投影"是常见且正确的结论。

**`pair`** —— 判据（五条全满足）：
1. 两侧 key 在真实"一问一答"里取值相等（回显字段，抓包核对过）；
2. 两侧谓词互斥（F8），都指向 `_meta.direction` 或其他互斥事实；
3. 配对键**连接内唯一**（F6：pair 池已按 ConnID 分片，per-connection 自增 seq 可用）；
4. 往返在 30s 内且未经 flush（F7）；
5. `when` 已拦截推送/广播。

反例：两侧谓词都用 `exists` 导致永远命中 side 0（F8）。找不到可靠关联键、或请求不回显任何键——**不做 pair**，插件停在 name（外加发现过的 annotate）同样是合格形态。
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

- **A. 规则可命中性**（本节，离线可跑）：用真实抓包固件解码出的 payload + meta 回放 `rule.Evaluate`，验证 path 存在、value 真实出现、side 能命中、类型一致。这只证明**规则本身具备命中能力**，**不证明平台真的完成了配对**——pair 的运行期行为（ConnID 分片、pending 池、30s TTL、flush）是 Runtime Plane 的，`rule.Evaluate` 触碰不到。
- **B. 平台实际效果**（落库后）：真实宿主运行（live capture / `decode_raw_packets`）后用 `list_decoded_data` 检查 `meta.msg_name` / `meta.semantic` / `correlation_id` / `causation_id`。**pair 只有在真实事件上看到正确的 `causation_id`（响应方→请求方）与一致的 `correlation_id`，才算运行期配对成功**——A 层全绿但 B 层没配上，说明问题在规则声明之外的运行期语义（如 key 连接内不唯一、往返超 30s）。

**何时跑**（按 §5.3 声明表）：没声明 `semantic_rules` 就没有本节；`name`/`annotate` 用 `test_plugin` 的 `sample_events` 抽查命中即可，**不必产出整张 A 层表**；只有 `pair` 值得跑一遍 A 层（逐条命中 + 两侧成对）再落库补 B 层。已声明规则没有真实命中依据时不得静默保留，按下面「判定规则」处置。

A 层两个指标，判据不同（示例代码可直接放进 `decode_test.go`）：

```go
// 指标一：逐条孤立回放 —— 该规则在固件上是否具备命中能力。
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
| `foo.mark_push` | annotate | 0 | — | **删**：固件无推送样本，协议证据也说不出 |

判定规则（三档）：
- 当前样本命中 → 保留。
- 当前未命中，但有**明确协议证据**（稀有/异常/活动分支，出处可引用）→ 保留，注释写明"已知未覆盖 + 依据"——真实流量稀有不是删规则的理由。
- 既无命中又无证据 → 删除。
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
| "平台有 state_change 能力，等级/金币/背包应该做出来" | 能力是增强项不是义务；协议里没有持续状态体系，就不产出 `_state_changes`（§5.4） |
| "还没发现 push/error，先继续找全" | 发现式工作流：证据看完一轮后答"没有"即结束，"未发现"是交付结论不是中间态 |

**红旗（出现即停，先找证据或问用户）**：

- "我觉得这个应该是请求/响应"
- "这个字段看起来像配对键"
- "先写上去，后面再验证"
- 规则注释里写不出依据来源

### 6. 测试（按适用面组织；"缺一不可"只在各自层内成立）

**A. 通用必测（所有插件）**

1. 解析器单测：正常输入解码正确；畸形输入（非法长度、截断、垃圾字节）不 panic、不无限循环。
2. 全链路：固件字节 → 事件，断言 payload 字段、Meta direction、同包多帧。
3. manifest 一致性：`semantic_rules` 引用的 payload 路径能实际解析；`contract.NewPluginChecker().Check(m)` 零 violation（没声明规则就只查 manifest 其余项）。
4. **Payload 纯度**：payload 中不含协议外字段（对照 §4.2 硬约束）。

**B. 协议适用才测（没有该特性，整条删掉，不算缺）**

- **TCP**：一个消息跨多个 TCP 段（reassembly）、握手消费 + mid-stream attach、SYN/RST 重置、**5-tuple 复用重连**（回归坑 1/坑 2）、两条并发连接各自独立解码。
- **UDP**：分包独立解码、无握手、多对端互不影响。
- **压缩/加密（协议真用了才有这条）**：gzip / bzip2 等对应固件单测（固件生成用临时脚本，跑完删除）。普通 TCP+JSON 协议不需要解压测试。

> 责任边界：插件测的是**解码器 `FlowKey` 的重组隔离**（两条流状态互不污染）；宿主 `ConnID` 对 pair 池的按连接分片（F6）属平台 Runtime 行为，由主仓测试护栏保证，不作为插件单测要求。

**C. 声明了什么能力就验证什么（对应 §5.3 验证表；没声明就没有这条）**

- 声明 `name`/`annotate` → 每条规则至少一次真实命中依据（`test_plugin` 抽样或 decode 测试里跑 `rule.Evaluate` 即可，不必产出覆盖率整表）。
- 声明 `pair` → §5.5 A 层逐条命中成对 + 落库后 B 层核对 `causation_id`/`correlation_id`。
- 声明 `_state_changes` → 落库后核对投影（`list_state_changes`，op/after 口径见 SDK 契约）。
- 一个都没声明 → C 层整体不存在。**未声明任何规则不构成测试缺口。**

TCP 固件构造用 `gopacket` 拼以太网 + IPv4 + TCP 帧，使 `framing.ExtractL7` 能读出端口（用于方向判定）；压缩负载在测试内直接预压缩后写入。

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

解码器运行在宿主热路径上，**一个 panic / 死循环 / 无限内存增长会拖垮整条抓包链路**。兜底分两档：通用安全底线人人要有，其余按协议特性适用。

**通用安全底线（所有插件）**

1. **panic 边界归 SDK**：`Decoder.DecodeV2` 外层已把 panic 转成该 input 的 `Error+Done:true`——stream 层（`decodePacket`）**不要**写吞 panic 的 recover，吞掉会漏发 done、丢错误信息。`framing.ExtractL7` 返回 `!ok` 时直接 `done=true` 返回，不算错误。
2. **不认识的字节不臆造 Payload**：先喂现场抓包（`sample_bytes_plugin`）确认协议范围；"未识别消息"要么**不产出事件仅 done**，要么（用户确认后）产出事件并附 `Meta` 标注（如 `unknown`）。
3. **有界内存**：任何持有流状态的实现必须有上限与丢弃策略（上限数值由协议/实现自定，本指南不规定具体数字），失步/超限时丢弃该流状态让后续重新自同步（§3 坑 3），绝不允许无限增长。

**协议适用才要求**

- **消息级容错怎么降级，按协议定**：非法长度/截断/校验失败时，帧定界能从消息边界恢复 → 跳过该消息继续；一条坏消息即污染整条流同步（多数流式 framing）→ `Forget` 整流丢弃才是正确方案。**不要**把"一条坏消息"升级为整帧错误（见 §7.1 的 plugin 报错误读）。
- **有 TCP 重组才有**重组缓冲上限、SYN/RST 流状态重置。
- **声明了 `pair` 才有**超时考量：`pair` 池 30s TTL（F7），长轮询/异步响应不声明 `pair`；迟到兜底归宿主，插件只需不声明配不上的 pair。

### 7.3 协议插件必交材料（交付清单，逐项验收）

插件交付 = 能跑的程序 + **能让人独立复核的证据链**。以下材料按序逐个核对——注意 2/3 是**按协议实际存在**的：一条规则都没声明的插件（Semantic review 全 none）这两项天然不存在，不算缺（§5.3）：

| # | 材料 | 出处/要求 |
|---|---|---|
| 1 | Packet 处理链路结论 | §1.1：能回答 5 问（定界 / 重组 / 哪些字节进业务解析 / JSON 字段来源 / push·压缩·加密存在性）；字段表等具象形式只在复杂协议要求，简单协议写在插件注释/README 即可 |
| 2 | 已声明规则的依据 + Semantic review | §5.3：plugin.yaml 注释里每条规则写明出处（哪个消息/字段/哪一侧）；Semantic review 就是最终报告里的一句话清单（found/none），不是独立文档 |
| 3 | 规则命中核对结果 | §6.C：已声明规则均有命中依据；未命中的按 §5.5 判定规则保留（注明"未覆盖 + 协议证据"）或删除；声明了 pair 的补 B 层配对核对 |
| 4 | `plugin.yaml` | `contract.NewPluginChecker().Check(m)` 零 violation |
| 5 | 单测 + 固件 | §6：A 通用必测全过；B 按协议特性（TCP 重组/握手/重连，或 UDP 分包，或压缩加密固件）齐全 |
| 6 | 真实抓包样例（可选但强烈建议） | `sample_bytes_plugin` 采样，供 reviewer 直接用 `list_decoded_data` 复核 |
| 7 | 宿主侧验证结果 | §7：`semantic rules:` 无 error；`list_decoded_data` 字段能原样查回（§3.0 约束） |
| 8 | 诊断指标自查 | §7.1：会话 `decode_errors`、`decode_error_groups` 分组合理、模板参数稳定 |

**评审红线**：§1.1 链路结论写不清 == 协议分析未完成，不允许进入上线；写出去却没有依据、且样本也无命中的规则，打回删除。**反过来也成立**——没有声明任何增强规则、协议没有 TCP/压缩所以没有对应测试，都不构成打回理由，"未发现"是合法交付形态。

## 踩坑清单（完成前逐条自查）

- [ ] go.mod 的 go 指令对齐 `sdk/go.mod`（当前 **1.25.5**；宿主用 1.26.8 与插件无关，勿写 1.26）
- [ ] 编码前已形成 Packet → Event 处理结论（§1.1 五问：定界 / 重组 / 哪些字节进业务解析 / JSON 字段来源 / push·压缩·加密存不存在；简单协议写注释/README 即可，字段表只在复杂协议要求）
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
- [ ] 方向能可靠判断时解码器写了 `Meta["direction"]`，取值只用 `client_to_server` / `server_to_client`；判不了留空由宿主按端口补齐，绝不猜（F2）
- [ ] **semantic_rules 只声明发现的语义**（§5.3）：写出去的每条规则有出处证据；没证据的候选直接不写；验证只做"声明了什么验证什么"，不跑用不上的流程
- [ ] Semantic review 已写进报告（notification/error/state/pair 各自 found 或 none，**全 none 也算合格**）；没有为"能力齐全"硬造 push/error/state_change
- [ ] 规则注释写了依据来源（哪个消息/字段/哪一侧、源码还是抓包）
- [ ] `when` 没有用 `neq` 表达"字段不存在"（F3）；`value` 字面量类型与 payload 一致（F4）
- [ ] `name` 规则至多一条生效，多条时确认过声明顺序（F5）
- [ ] `pair` 配对键**连接内唯一**（F6，per-connection seq 可用）且已抓包核对；两侧谓词互斥（F8）；往返 30s 内（F7）；推送/广播已被 `when` 拦截
- [ ] 方向区分不了的双向消息已用 `annotate` 显式覆盖默认角色（宿主默认按方向补 request/response，标错比不标更糟）
- [ ] 已声明规则均有命中依据：样本命中→保留；未命中但有协议证据→保留并注明"未覆盖 + 依据"；无命中无证据→删（§5.5 判定规则）；声明了 pair 已补 B 层实际配对核对
- [ ] 测试按 §6 三层齐备：A 通用必测（解码/畸形输入/manifest/Payload 纯度）全过；B 按协议特性（TCP：跨段+握手+重连+FlowKey 隔离 / UDP：分包独立无握手 / 压缩加密固件）只测用得上的；C 只为已声明能力做验证
- [ ] **诊断指标已核对**（§7.1）：会话 `decode_errors`、`decode_error_groups` 分组合理、错误模板参数稳定（`<n>` 占位符）
- [ ] **兜底判断齐备**（§7.2）：通用底线——panic 边界归 SDK 不吞、未识别字节不臆造 payload、流状态有界内存；协议适用项——降级策略按协议选（跳消息 vs Forget 整流）、有 TCP 重组才有上限/重置测试
- [ ] **必交材料齐全**（§7.3）：处理链路（形式不限）、已声明规则的依据 + Semantic review、规则命中核对结果、plugin.yaml、测试与固件
- [ ] **运行实例 ↔ 本机源码一致**（§7.3）：`list_registered_plugins` 返回的运行实例与你的 workspace 中源码对应；**改过任何插件源码（decode.go / parser.go 等）就重新在本机 `go build` 并重启插件**（平台不编译、不保留二进制）
- [ ] `go vet` + `go build` + `go test -count=1` 全过
- [ ] 宿主日志无 semantic rules error；前端消息名正确显示
