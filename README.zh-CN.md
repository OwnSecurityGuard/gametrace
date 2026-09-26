<div align="center">

<img src="web/public/logo.png" alt="GameTrace 吉祥物" width="128" />

# GameTrace

**把游戏网络流量，变成可追溯的调试证据。**

抓包 → 用插件解码 → 得到请求/响应配对、协议错误、字段级状态变更 → 在 Web UI 里看，或用 MCP 查询。

[English](README.md) · [**简体中文**](README.zh-CN.md)

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

<!-- TODO: demo GIF — 一镜到底：开始抓包 → 操作游戏 → 协议事件流式出现 →
     展开一问一答 → 状态变更 hp 100 → 65 → Agent 查询并总结。产物放到 docs/demo.gif。 -->

</div>

GameTrace 抓取实时或已录制的游戏流量，通过独立的**解码插件**解析自定义/私有协议，把数据包变成
**可查询的 trace**：协议事件、请求/响应配对、服务端推送、协议错误、因果链与实体状态变更。
人在 Web 面板上看这份 trace，自动化和 AI Agent 通过 MCP 查同一份 trace。

它面向**游戏 QA、测试自动化、游戏开发、协议分析和 AI Agent**。

## GameTrace 解决什么问题

游戏测试里，屏幕上发生的和网络上的，中间有一段断层。

- QA 从 UI 复现了问题，但看不到底下的请求。
- UI 自动化用例失败了，结果并不说明是客户端发错了请求、收到了错误响应、漏了推送，还是状态没同步上。
- 测试或压测系统想要真实的游戏协议数据，但拿到并看懂这些数据，每次都要单独做一遍抓包 + 协议分析。

GameTrace 补的就是这段断层：把游戏流量变成一条**带协议语义、可查询的结构化 trace**——人能查、
自动化能消费、AI Agent 能直接问。

## 使用场景

### 1. 给测试自动化与压测提供真实协议数据

抓真实游玩流量并解码成结构化协议数据，供 API/协议自动化、压测与容量测试、回归测试、回放与场景
生成、测试数据准备复用。GameTrace 提供观测到的协议数据与语义；你的压测引擎或自动化框架保持独立。

### 2. 让 QA 看得见网络行为，并用规则主动提醒异常

抓包与正常测试流程并行，QA 照旧玩、照旧报问题。一旦出问题，会话里已经存了：发过哪些协议消息、
请求与响应如何配对、服务端推了什么、哪些解码失败、这中间每一次字段级状态变更。UI 层面的现象，
就此变成协议层面可追查的 trace。

而且 QA 不必被动地翻这条 trace：项目可以带一组**检查规则**——对解码事件字段的命中条件，加上命中后
要展示的文案。

```json
{
  "id": "game.item.grant_without_reason",
  "name": "道具发放缺少来源字段",
  "enabled": true,
  "when": [
    { "path": "_meta.msg_name", "op": "eq", "value": "item.grant" },
    { "path": "data.reason", "op": "not_exists" }
  ],
  "title": "收到道具发放，但没有 reason 字段",
  "cooldown_sec": 60
}
```

一条规则就是一个 GJSON `path` 加一个闭集内的 op（`eq` · `neq` · `exists` · `gt` · `in` · `contains` ·
`prefix` …），可以用数组隐式 `all`，也可以显式 `all`/`any` 组合。管线在每条解码事件上求值项目的规则，
命中即写入该会话的「命中提醒」视图——带触发的那条消息和它前后各若干条记录，同时在 QA 所在的探针机器
上弹一条桌面通知。「这个 buff 不该在没有请求的情况下到达」从此是一条规则，而不靠 QA 的直觉；抓包
当场就被提醒，而不是事后翻报告才发现。

规则在项目页的「检查规则」表单里写，也可以由 Agent 走 `set_project_rules`（需要项目管理员权限；一条
规则非法就整表拒绝写入）；规则在会话开始时取快照，所以改动从**下一次抓包**起生效。单项目最多 50 条；
同规则同会话有冷却窗口（默认 30 秒），避免一个反复出现的条件把提醒刷屏。

### 3. UI 自动化失败的独立网络证据

客户端 UI 自动化用例失败时，GameTrace 提供同一时间窗的独立证据源：触发这次操作的请求、对应响应、
异步推送、协议错误、以及随后的状态变更。用来区分「UI 断言写错了」和「客户端-服务端通信/状态同步
真的失败了」。

### 4. 协议分析与 AI 辅助调试

面向自定义协议做抓取与回放，协议知识以独立解码插件的形式承载。产出的 trace 同时暴露给 Web UI 和
MCP，因此 Agent 可以自己查协议行为、找关联的请求响应、追状态变更、分析解码失败，甚至自行开发和
验证解码插件。

## GameTrace 产出什么

```text
实时流量 / PCAP
        │
        ▼
   抓包              （本机或远程探针按端口 pcap 实时抓，或离线 .pcap 回放）
        │
        ▼
   解码插件          （独立 gRPC 进程：一个 Go 二进制 + 一个 plugin.yaml）
        │
        ▼
 结构化协议事件
        ├── 请求 / 响应
        ├── 服务端推送
        ├── 协议错误
        ├── 命中提醒              （项目检查规则，逐条解码事件求值）
        ├── 请求/响应配对          （causation = 父子，correlation = 同一轮对话）
        ├── 因果关系               （OpenTelemetry 风格 TraceContext）
        └── 实体状态变更           （如 Player:1001.hp: 100 → 65）
        │
        ▼
   Web UI · MCP · 自动化
```

这份 trace 可以直接当作：调试证据源、协议分析数据集、自动化测试数据源、压测场景来源，或者 AI 辅助
分析的输入。

关键不在于「存了包」，而在于这条链路：**packet → 协议事件 → 配对 → 状态变更 → trace → Agent 查询**。

## GameTrace 是什么——以及不是什么

GameTrace **是**：

- 一层游戏流量抓取与回放（本机，或由远程探针机器上报）
- 一个协议解码框架——协议知识以插件承载，不进内核
- 一条带配对、因果与状态 diff 的结构化游戏网络 trace
- 一个人与自动化共用的调试证据源
- 一个 MCP 接口，同时服务人类工作流与 AI 工作流

GameTrace **不是**：

- 压测引擎或压力脚本执行器
- UI 自动化框架，也不是通用接口测试框架
- 止步于字节的通用抓包器
- 一个「已经内置了所有游戏协议」的协议库

## 工作原理

三部分，边界是刻意划开的：

1. **`gt-agent`（探针）** 跑在有游戏的那台机器上。按端口 pcap 抓包，或通过移动代理租约接入手机
   流量，把原始帧经 gRPC 推给服务器；也可以把本机运行的解码插件隧道到注册中心，插件因此从不需要
   离开你的机器。
2. **`gt-pipeline`（运行时平面）** 负责抓包会话、把字节分发给插件、在每条解码事件上求值项目的检查
   规则，并把结果投影成配对、状态变更、命中提醒与事件索引，落盘到 SQLite。
3. **`gt-mcp`（控制平面）** 是纯适配器：把抓包、查询、插件、项目、探针等能力全部暴露成 MCP 工具，
   并托管 Web UI。它不起子进程、不写文件、也不做失败归因。

协议知识永远不进内核：**GameTrace 是流水线，不是协议百科全书**。插件决定一段字节是什么意思；平台
不保存插件源码、不编译、也不拉起进程。

## 演示

这里该有一镜到底的 GIF：开始抓包 → 操作游戏 → 协议事件流式出现 → 展开一问一答 →
`hp: 100 → 65` → Agent 查询并总结。目前还没录制——用下面的[快速开始](#快速开始)，5 分钟就能看到
同样的效果。

## 快速开始

### A. 5 分钟看到价值——不用真游戏、不用额外设备

```bash
# 合成探针 + 真实解码插件跑通真实管线，并拉起 Web UI
SIM_SERVE=1 go test ./cmd/gt-pipeline/ -run TestSimulate -v
```

打开 `http://127.0.0.1:8781/`——如果仓库根目录的 `.env` 配了 `GT_AUTH_TOKENS`，用其中第一个身份登录；
没配则直接进的就是数据。这次运行会经真实解码插件回放一整段合成游戏会话，落库数百条字段级状态变更、
覆盖上百个实体。「协议事件」视图里请求/响应左右并排，「状态变更」视图里是逐字段的前后值。Ctrl-C
停止。

### B. 抓自己的包（QA 路径）

> 前提：管理员已经部署好服务器（[团队部署指南](docs/team-deployment.md)）。
> **要抓包的那台机器**上装好抓包驱动：Windows 装 [Npcap](https://npcap.com/)（勾选
> "WinPcap API-compatible Mode"）· Linux 装 `sudo apt install libpcap-dev`。

1. **登录服务器** — 浏览器打开 `http://<服务器>:<GT_MCP_PORT>`（compose 默认宿主端口 `18781`）。
   用管理员发的 token 登录；没有令牌就点「没有令牌？快速开始」自助注册，立刻拿到自己的身份。
2. **接入这台机器** — 点顶部工具栏「接入设备」→ 选目标操作系统 →「下载探针」，在**要抓包的那台
   电脑**上解压并运行 gt-agent（无需填任何参数）。zip 已内置回连地址与凭证
   （`config.embedded.json`），探针启动即注册到你的团队；缺的平台产物可在下载页点「现场编译」
   由服务器即时生成。**全程不用手填 token、回连地址或会话 ID。**
3. **选择游戏端口** — 设备在「我的设备」显示在线后，点顶部「开始抓包」：选刚接入的机器 → 填游戏
   端口（如 `9250`）→ 选解码插件 → 开始。探针只负责接入这台机器，端口与插件在「开始抓包」时才定。
4. **开始抓包并复现问题** — 正常操作游戏客户端让流量跑起来。想换端口或插件不用重新接入，再点一次
   「开始抓包」即可。
5. **查看协议行为** — 点「停止抓包」，进入该会话的「协议事件」视图：每行是一条解码后的协议消息
   （msg_name / 方向 / 语义标签 / 字段摘要），带配对角标的行点开就是左请求 / 右响应的一问一答；
   「状态变更」按**操作 / 实体 / 时间**三个视角看字段前后值（如 `Player:1001.hp: 100 → 65`）；
   「命中提醒」列出本次抓包里项目检查规则命中的记录，每条都带触发前后上下文。

> 看不到数据？先查 [成员上手指南 · 常见问题](docs/member-onboarding.md#4-常见问题)
> （端口是否一致、防火墙是否放行 `9091/9092`、Npcap/libpcap 是否装好）。

### C. 从源码跑起来（开发者路径）

**前置：** Go 1.26+ · libpcap (Linux) / [Npcap](https://npcap.com/) (Windows)

```bash
make build        # gt-mcp + gt-pipeline + gt-singbox-agent → bin/

# 终端 1 — 运行时平面（数据路径，先启动）
./bin/gt-pipeline.exe -workdir . -log-format text

# 终端 2 — 控制平面
./bin/gt-mcp.exe -work-dir . -log-format text

# 终端 3 — 本机探针（管线已不在本机抓包，所有原始帧经 agent ingest :9092 进来）
go run ./cmd/gt-agent
```

把 MCP 客户端指向 `http://127.0.0.1:8781/mcp`（Streamable HTTP）或
`http://127.0.0.1:8781/sse`（SSE），然后冒烟整条链路：

```
start_capture(port=8984)      # 会话开始，记下 session_id
#   … 造流量：go run ./examples/http/server  (+ ./examples/http/client)
get_session_status            # packets_in > 0 · decode_errors == 0
list_decoded_data(limit=50)   # 解码事件——需要解码插件，见下文「插件与协议支持」
```

`make test` 跑测试；`make run-mcp` / `make run-pipeline` 用 `go run` 快速迭代。

## 给 AI Agent 用

GameTrace 把抓包、协议分析、调试和插件开发全流程都开放成 **MCP（Model Context Protocol）**，
Agent 处理的证据和人类测试员看到的是同一份：

```text
游戏客户端
    │
    ▼
 GameTrace ── 抓到的流量 · 解码后的协议事件 · 请求/响应关系
    │              · 状态变更 · 协议诊断
    ▼
 MCP 服务器   :8781  (/mcp · /sse)
    │
    ▼
 AI Agent
```

典型的 Agent 工作流：从网络证据入手排查一个失败的游戏用例、查看某个事件前后的协议消息、串起
请求 → 响应 → 状态变更、分析未知或只部分解码的流量、开发并验证解码插件、从抓到的流量生成或补充
测试场景。

进入点，按推荐顺序：

| 工具 | Agent 为什么先调它 |
|------|--------------------|
| `get_capabilities` | 自描述的完整工具目录（按 workflow 分组）+ 推荐调用链 |
| `read_skill` | [`skills/`](skills/) 下的分步 Agent 技能（解码插件指南、协议谱系分析） |
| `sample_bytes_plugin` | 只给事实：某会话流量的 hexdump、长度直方图、首字节分布 |
| `get_protocol_catalog` | 这个会话实际观测到了哪些具名业务协议（按方向） |
| `query_state_changes` / `get_state_change_detail` | 字段级前后值，以及一次变更背后的协议链 |
| `scaffold_plugin` → `connect_plugin` → `verify_plugin` → `explain_plugin` | 完整插件闭环，自带失败归因 |

`scaffold_plugin` 只返回模板文件内容、什么都不落盘：平台没有插件目录，不编译、不拉起插件。Agent
把骨架写进**自己的 workspace**，你在本机构建并运行，再由 `connect_plugin` 通知平台去注册中心找它。
**插件源码始终是你的。**

### MCP 端点

```text
http://127.0.0.1:8781/mcp          # Streamable HTTP（SSE 传输用 /sse）
Authorization: Bearer <token>      # 服务端配了 GT_AUTH_TOKENS 时需要
```

## 插件与协议支持

一个插件就是**一个 Go 二进制 + 一个 `plugin.yaml`**：清单声明身份与契约（`api_version`、
`protocol`、`transports`、`semantic_rules`），宿主在注册时校验契约，并在每条事件上再校验。插件是
独立 gRPC 进程：热加载、在**运行中**的会话上热切换（`set_session_plugin`）、与管线进程崩溃隔离。

插件只依赖 [`github.com/OwnSecurityGuard/gametrace/sdk`](sdk/)——本 monorepo 里 `sdk/` 下的独立
Go module，与 gametrace 内部实现零耦合。人类可读指南：
[`docs/gt-plugin-development.md`](docs/gt-plugin-development.md)。

```
scaffold_plugin ──► 本机 build + run ──► connect_plugin ──► verify_plugin
                        │                     │                  │
                        └───────────── explain_plugin ────────────┘
                        （归因最近一次 connect / verify 失败）
```

还没有你协议的解码插件？这是预期内的——写一个正是整套设计围绕的扩展点，而且上面这个闭环可以由
Agent 驱动。

### 示例

| 示例 | 说明 |
|------|------|
| [`examples/http`](examples/http) | 客户端 + 服务端产生可解析的 HTTP 流量（`:8984`），跑通第一个端到端会话 |
| [`examples/http-decoder`](examples/http-decoder) | HTTP/1.1 定界（方法行/状态行解析 + JSON 信封语义） |
| [`examples/ws-decoder`](examples/ws-decoder) | WebSocket over TCP：HTTP Upgrade 握手 + RFC 6455 帧，方向取自 MASK 位；配套生成器 [`examples/ws`](examples/ws)（`:8990`） |
| [`examples/lp-decoder`](examples/lp-decoder) | 长度前缀定界（1/2/4 字节长度字段、逐帧可变大小端），方向由消息身份推导；配套生成器 [`examples/lp`](examples/lp)（`:8998`，登录/背包/道具/资源场景，推送投影为状态前后值） |
| [http-stream-decoder](sdk/examples/http-stream-decoder) | SDK module 里的参考实现 |
| [`sdk/docs/case-study-godot-tiny-mmo.md`](sdk/docs/case-study-godot-tiny-mmo.md) | 案例：解码 Godot 调试协议（请求/响应 + 状态主体） |

各协议的 TCP 定界方式都不同，这些模板是参考不是照抄——接真实协议前先用 `sample_bytes_plugin`
核对字节流。

## MCP 工具

上面每一项能力都是一个 MCP 工具。完整工具表由工具注册代码生成在
[`docs/mcp-tools.md`](docs/mcp-tools.md)（CI 会拦截漂移），运行时 `get_capabilities` 返回同一份目录，
分组如下：

| 分组 | 代表工具 |
|------|----------|
| 抓包与会话 | `start_capture` · `stop_capture` · `get_session_status` · `set_session_plugin` |
| 探针与移动代理 | `probe_start_capture` · `probe_import_archive` · `create_proxy_lease` · `start_lease_capture` |
| 查询 trace | `list_decoded_data` · `get_protocol_catalog` · `list_connections` · `list_session_alerts` · `query_decode_errors` |
| 状态分析 | `query_state_changes` · `get_state_change_detail` · `list_state_changes` |
| 插件开发与验证 | `scaffold_plugin` · `connect_plugin` · `verify_plugin` · `explain_plugin` · `sample_bytes_plugin` |
| 项目与成员 | `create_project` · `set_project_plugins` · `add_project_member` · `move_session_to_project` |

**Agent 自检** — 把这段贴给 Agent，一口气验证全栈：

```
1. tools/list / get_capabilities            → 上面每个分组都在
2. start_capture(port=8984, plugin=<你的>)  → 记下 session_id（gt-agent 必须在运行）
3. get_session_status                       → packets_in > 0，decode_errors == 0
4. list_decoded_data(limit=50)              → 事件带 event_type / schema_id / 扁平语义字段
5. get_protocol_catalog(session_id=…)       → 实际观测到的业务协议（按方向）
6. query_state_changes                      → 字段级前后值（如 hp: 100 → 65）
```

## 架构

两个平面，划分的目的是**让 MCP 保持纯适配器**——`gt-mcp` 不起子进程、不写文件、不做失败归因，只
转发。插件的*源码、构建与进程*根本不算一个平面：它们在用户自己的机器上，平台只观察主动注册的实例。

```
团队成员主机                       AI Agent（Claude / DeepSeek / …）
┌────────────────────────────────┐      │  MCP over HTTP — :8781  (/sse + /message · /mcp)
│  gt-agent                      │      │  Authorization: Bearer <token>
│  ├─ capture ingest ── :9092 ───┐     ▼
│  └─ plugin tunnel ─── :9091 ───┼──────────────────────────────────────┐
│                                │      运行时平面 — gt-pipeline          │
│  解码插件                       │      capture → decode → project       │
│  源码 / 二进制 / 进程            │      → SQLite · PluginRegistry        │
│  （都在你手上）                  │      :9091 · AgentIngest :9092        │
│                                │      · MCP 适配器 gt-mcp :8781        │
└────────────────────────────────┼─────┘                                │
                                 └──── gRPC 注册 + 心跳 ─────────────────┘
                                   （只被观察，从不被管理）
```

| 端口 | 监听方 |
|------|--------|
| `:8781` | gt-mcp HTTP — `/sse` + `/message`（SSE）· `/mcp`（Streamable HTTP）· `/events/plugins`；配了 `GT_AUTH_TOKENS` 时走 Bearer 鉴权 |
| `:9888` | CaptureControl gRPC — gt-mcp → gt-pipeline |
| `:9091` | PluginRegistry gRPC — 解码插件 → gt-pipeline（远程成员经 gt-agent 隧道注册） |
| `:9092` | AgentIngest gRPC — gt-agent 从团队成员机器推送原始帧 |

## 团队部署

用 Docker Compose 起一台团队共享服务器——三个服务（gt-pipeline + gt-mcp + Postgres）。先在 `.env`
里填好 5 个必填项（`GT_AUTH_TOKENS=alice=gt_xxx,bob=gt_yyy`，管理员加 `:admin` 后缀、
`GT_PUBLIC_HOST`、`GT_PUBLIC_REGISTRY_PORT`、`GT_PUBLIC_INGEST_PORT`、`GT_LAN_IP`），然后：

```bash
cp .env.example .env   # 填好上述 5 个必填项（compose 用 ${VAR:?} 强制校验）
docker compose up -d --build
```

宿主发布端口默认 **`19888/19091/19092/18781`**（容器内仍是 `9888/9091/9092/8781`，偏移说明见
`docker-compose.yml` 顶部）。成员在自己机器上只需一个 `gt-agent` 二进制：向 `:9092` 推送抓包，并通过
注册中心隧道托管本机解码插件（平台会给他们注入 `GT_TUNNEL=1`）。

## 文档

- [团队部署指南](docs/team-deployment.md) · [成员上手指南](docs/member-onboarding.md)
- [插件开发指南](docs/gt-plugin-development.md) — 怎么写、怎么验证一个解码器
- [MCP 工具目录](docs/mcp-tools.md) — 由工具注册代码生成
- [协议事件模型](docs/event.md) — 解码事件、语义与状态变更的形状
- [Agent 技能](skills/) — `decoder-plugin-guide`、`protocol-lineage-analysis`，通过 `read_skill` 获取

## 参与贡献

```bash
make vet && make lint && make test     # CI 门槛
make docs                              # 新增 MCP 工具后重新生成 docs/mcp-tools.md
```

- 新增 MCP 工具就在 `cmd/gt-mcp/` 里加，然后重跑 `make docs`——生成表与代码不一致时 CI 会失败。
- 解码器与实验放在 `examples/` 和 `sdk/examples/`；SDK 是独立 module，用 `make sdk-test` 覆盖。
- 边界要保持：`gt-mcp` 始终是纯适配器，平台任何部分都不得保存、编译或拉起插件源码。

## Roadmap

- [ ] **会话时间轴与回放视图** — 在 `web/` 已有的抓包 / 会话 / 连接 / 协议数据 / 插件 / 项目 / 探针
  之上，补一等公民的时间轴与回放
- [ ] **回放与压测脚本生成** — 由 Agent 从一次会话的 trace 生成回放/压测脚本（Scenario → Replay 两阶段）
- [ ] 为 `event_index` 与 `plugin_debug_access` 审计表提供**一等公民读工具**
- [ ] **SDK 进阶文档** — 重组控制（`Reassembler.Forget/Reset`）、`MetaValue`、`FlowKey.Canonical`，
  以及更多示例解码器
- [ ] **英文文档与英文界面** — 当前 Web UI 与设计文档以中文为主

## License

[MIT](LICENSE)
