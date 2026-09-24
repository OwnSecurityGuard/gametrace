# GameTrace Plugin SDK (`gametrace/sdk`)

GameTrace（Game Traffic Analysis）解码插件的官方 Go SDK。插件是一个独立进程：
启动 gRPC `Decoder.DecodeV2` 服务、读取 `plugin.yaml` 清单、向 GameTrace 宿主注册，
然后接收抓包记录并解码为带语义契约的逻辑事件。

```go
package main

import (
    pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
    sdk "github.com/OwnSecurityGuard/gametrace/sdk"
)

func main() {
    sdk.RunRegisterLoop(decode) // 注册、心跳、重试、端点选择全部由 SDK 托管
}

func decode(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
    // 解码零或多条事件，最终必须为每个 input_id 回 done=true
    ...
}
```

## 快速开始

```bash
# 发布 tag 带 sdk/ 前缀（module 位于 monorepo 子目录），最新版见「版本」一节
go get github.com/OwnSecurityGuard/gametrace/sdk@v0.10.0
```

在 gametrace 仓库内开发时改用 replace 指向仓库内源码，避免依赖网络：

```bash
go mod edit -replace github.com/OwnSecurityGuard/gametrace/sdk=./sdk
```

1. 从 [examples/http-stream-decoder](examples/http-stream-decoder/) 复制骨架
   （`plugin.yaml` + 解码回调）。
2. 实现 `DecodeFuncV2`：`framing.ExtractL7(payload, link_type)` →
   `framing.Reassembler` 重组 → 解析应用消息 → `event.Draft` 发送。
3. `go build` 后设置 `GT_REGISTRY_ADDR` 在本机运行插件，再走 GameTrace 侧
   `connect_plugin` → `verify_plugin` 闭环（平台不编译、不拉起插件，也不保存其源码）。

## 包结构

| 包 | 职责 |
|---|---|
| `sdk` 根包 | `RunRegisterLoop` / `RunRegisterLoopWithOptions`（注册/心跳/重试/隧道/认证）、manifest 读写校验、`DecodeFuncV2` |
| `contract/` | 线上契约 SSOT（`contract.yaml`）+ 分层 `PluginChecker`（semantic 规则） |
| `rule/` | Protocol Semantic Rule：GJSON 取值 + Predicate + Effect（pair/annotate/name） |
| `framing/` | `ExtractL7`（按 link_type 剥链路层）、`Reassembler`（TCP 逐流重组）、FlowKey |
| `event/` | `Draft`（Type/Value/Meta/Analysis 四段）、`Value`（MsgPack 编解码）、Split/Merge |
| `proto/` | 插件 ↔ 宿主 gRPC 契约（`plugin.proto` 为 SSOT，仅本仓库生成一份） |
| `examples/http-stream-decoder` | 完整可运行的参考插件（semantic_rules 全声明） |

## plugin.yaml 清单

每个插件需要当前目录下的 `plugin.yaml`。最小合法清单：

```yaml
api_version: gt.decoder/v2
name: example-game
protocol: example-game
type: decoder
```

当前 SDK 校验：`api_version` 匹配 `gt.decoder/v<digit>`、`name` 为小写 kebab-case、
`protocol` 必填、`type` 固定 `decoder`；宿主在注册期按自身大版本（当前 `gt.decoder/v2`）
复核 `api_version`。

可选声明段（v0.10.0 现状）：

```yaml
protocol_version: "1.1"
transports:        # v0.8.1 新增：声明能解的 L4 传输层，取值 tcp|udp
  - tcp
hints:
  - example-game

contract:
  name: gt.plugin
  version: 1
# Protocol Semantic Rule：插件定义协议语义，平台执行
semantic_rules:
  - id: example.mark_push
    when: [ { path: seq, op: eq, value: 0 } ]
    effect: { type: annotate, semantic: notification }

meta:
  author: example
  description: Decoder for example game protocol
```

## 契约层

`contract.yaml` 是插件与宿主之间线上协议的 SSOT（`spec_version: 7`），
SDK 侧当前承载语义契约层：

- **Protocol Semantic Rule 层**（`LayerSemantic`）：`semantic_rules` 声明在
  注册期校验（错误拒绝 Register RPC），规则评估在 `plugin.verify` 期逐事件执行。

`_state_changes` 是分析载荷的保留键：宿主经 `event.ExtractStateChanges` 投影到
`state_changes` 表，是实体/状态变更分析的唯一输入。

## 注册与解码通道

`RunRegisterLoopWithOptions(decodeFuncV2, opts)` **以隧道模式注册**（平台唯一的运行方式）：
插件不起本地监听，`Register` 后在同一条连接上打开 `Connect` 双向流，DecodeV2 经隧道帧完成，
宿主不回拨。建流时插件把 Register 返回的 `instance_id` 放入 metadata
（`sdk.TunnelInstanceIDKey`），宿主据此精确绑定，不按到达顺序猜测配对；**隧道期间心跳照发**
——TCP 半开时流的 `Recv` 不会报错，心跳是唯一的应用层探测。设置 `opts.AuthToken` 后所有 RPC
附带 `authorization: Bearer <token>` metadata。

注册端点发现顺序：`GT_REGISTRY_ADDR` 环境变量 > `--registry=` 参数 > 默认 `:9091`。
注册端点支持 TCP、Unix socket（`unix:` 或裸路径）与 Windows 命名管道（`npipe:`）。
心跳默认 10s，注册/心跳失败按指数退避重试（首次 1s，上限 30s）；SDK 侧对 registry 连接
启用 gRPC keepalive（30s / 10s，允许无流时探测），用于发现半开连接。

## 环境变量速查

插件能否接入只取决于两个环境变量——漏设 `GT_REGISTRY_ADDR` 是"注册不上 / 解码不出事件"最常见的原因：

| 环境变量 | 作用 | 典型值 |
|---|---|---|
| `GT_REGISTRY_ADDR` | 宿主 registry 的监听地址，插件据此注册（发现顺序最高） | `127.0.0.1:19091` |
| `GT_TUNNEL` | 隧道模式标记。由**运行方**注入（gt-agent / `activate_plugin` 都注入 `1`），插件代码只透传不判断 | `1` |
| `GT_AUTH_TOKEN` | 注册鉴权 Bearer token（平台未开鉴权时可留空） | `gt_tok_xxx` |

平台统一以隧道模式运行插件：宿主不回拨，插件不需要任何入站端口，**只需 `GT_REGISTRY_ADDR`
（+ `GT_AUTH_TOKEN`）**。原 `GT_DECODER_ADDR` / `GT_DECODER_PUBLIC_ADDR`（本地监听地址与回连
地址）已随非隧道回退路径整体删除——如果发现自己在配这两个变量，方向已经错了。

本地/同机开发的最简完整配置：

```bash
export GT_REGISTRY_ADDR=127.0.0.1:19091           # 宿主 registry 端口
export GT_AUTH_TOKEN=gt_tok_xxx                   # 平台开启鉴权时必填
```

## 文档

- **[Agents.md](Agents.md)** — AI/Agent 开发操作契约（写插件前必读）
- [docs/decoder-development.md](docs/decoder-development.md) — 解码器开发指南（含 framing 权威说明）
- [docs/plugin-semantic-rules.md](docs/plugin-semantic-rules.md) — Protocol Semantic Rule 规范
- [docs/stream-reassembly.md](docs/stream-reassembly.md) / [docs/link-type-reference.md](docs/link-type-reference.md) — TCP 重组与链路类型参考
- [docs/runtime-connection.md](docs/runtime-connection.md) — 运行时连接（注册 / 隧道）说明
- [docs/troubleshooting.md](docs/troubleshooting.md) — 常见问题（0 事件排查等）
- [docs/case-study-godot-tiny-mmo.md](docs/case-study-godot-tiny-mmo.md) — Godot 小游戏解码案例
- [examples/http-stream-decoder/README.md](examples/http-stream-decoder/README.md) — 参考插件说明

宿主（pipeline/MCP 工具链）侧开发指南见本仓库根目录的
[docs/gt-plugin-development.md](../docs/gt-plugin-development.md)（MCP `get_plugin_dev_guide` 可获取）。

## 版本

当前 **v0.10.0**。沿革：

- **v0.10.0** — **连接路径收敛为隧道唯一模式（破坏性）**：`Register` 请求删除 `socket_path` /
  `tunnel` 两字段（protobuf 保留号 1、3），宿主不再回拨插件端点；解码一律走插件主动拨出的
  `Connect` 双向流，建流时以 gRPC metadata `x-gt-instance-id` 携带 `Register` 返回的
  `instance_id`，宿主据此精确绑定实例，id 缺失或未知即拒绝建流。SDK 侧 `RegisterOptions`
  只剩 `AuthToken`；`GT_DECODER_ADDR` / `GT_DECODER_PUBLIC_ADDR` 已移除，插件只认
  `GT_REGISTRY_ADDR`。契约 `spec_version` 6 → 7。**旧 SDK 构建的插件会在建流阶段被拒**
  （注册成功但永不 online），必须用本版 SDK 重新构建。
- **v0.9.0** — **Protocol Semantic Rule 的 effect 闭集收敛为 `pair` / `annotate` / `name`**：
  原第四个效果 `extract`（声明 `source` 把数组/对象字段拆成子事件、宿主挂 `parent_id`）已整体删除。
  它拆出的子事件不过语义规则（无 `meta`）、不参与状态投影（`Analysis` 恒空）、`schema_id` 随
  schema 子系统移除后无处安放；而 `DecodeV2` 响应的 `r.Events` 本就是切片，解码器直接发多条
  事件能同时拿到这三样，是能力更完整的同一条路。连带移除 `events.parent_id` 列与前端父子视图。
  另：SDK 源码迁入 gametrace monorepo 的 `sdk/`（module 路径不变），发布 tag 为 `sdk/vX.Y.Z`。
- **v0.8.2** — Protocol Semantic Rule 新增 `name` 效果：消息名从 payload 经 GJSON 提取，
  写入 `meta.msg_name`；`name` 规则先于其余规则求值，其结果注入 `_meta.msg_name`
  供后续谓词判定，规则声明优先于解码器硬编码。
- **v0.8.1** — manifest 新增 `transports` 声明（tcp|udp），插件可声明支持的 L4 传输层。
- **v0.8.0** — **Payload/Meta/Analysis 三段分离模型**：`DecodeResponseV2` 新增
  `meta_msgpack` / `analysis_msgpack`；`event.Draft` 增加 `Meta` / `Analysis` 字段；
  `event.SplitReservedKeys` / `MergeReservedKeys` 兼容旧扁平 payload。业务字段与平台推导从模型层强制分离。
- **v0.7.2** — 注册上报地址：通配符监听地址自动替换为本机非回环 IPv4，适配平台侧 Docker 部署回连；
  新增 `GT_DECODER_PUBLIC_ADDR` 原样上报。
- **v0.7.1** — 回归 schema 声明期校验与 `indexable_fields` 归一化（升格为 queryable + alias）。
- **v0.7.0** — **恢复 schema/state 声明层**，与 Protocol Semantic Rule 并存。
- v0.6.x — 仅 Protocol Semantic Rule 的瘦身阶段；runtime/event/transport 各层校验
  （`CheckManifest`、`CheckDecodeResponse`、`CheckDecodeRequest` 等）已移除不再回归；
  `contract.yaml` 自 gametrace 迁入本仓库成为 SSOT。
- v0.1.0 — 仅 Runtime/Event 的初始快照。

## 许可

[MIT](LICENSE)
