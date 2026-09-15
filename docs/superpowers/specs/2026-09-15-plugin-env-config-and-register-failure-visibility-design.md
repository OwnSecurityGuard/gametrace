# 插件 .env 配置工具 + 解码器注册失败可见性 设计

- 日期：2026-09-15
- 状态：已确认（用户批准方案 1-A + 2-A，及三项关键决策）

## 1. 背景与问题

用户用 AI skill（`skills/decoder-plugin-guide`）编写解码插件时存在两个问题：

1. **.env 配置不会填**：生成的 `.env` 中 4 项配置（`GT_REGISTRY_ADDR` / `GT_AUTH_TOKEN` / `GT_DECODER_ADDR` / `GT_DECODER_PUBLIC_ADDR`）用户不知如何取值，尤其 `GT_DECODER_ADDR` 与 `GT_DECODER_PUBLIC_ADDR` 频繁填错。现状是 SKILL 指引 AI 调 `get_registry_addr` 拿地址、token 仍需用户手动从平台 `GT_AUTH_TOKENS` 复制。
2. **注册失败无前端提示**：`GT_DECODER_PUBLIC_ADDR` 填错时，平台在 `Register` RPC 中拨号验证解码器地址失败，仅向 SDK 返回错误（插件控制台日志可见），前端事件流只有 register/deregister/online/offline 四种事件且 hook 不读事件内容，插件面板数据来自 `list_registered_plugins`（失败插件不在列表中）——用户在前端完全看不到失败。

## 2. 已确认决策

| 决策点 | 结论 |
| --- | --- |
| 新工具是否返回调用者自己的 token | **返回**（信任级别与现有 OAuth 兑换一致：调用方已通过 Bearer/OAuth 认证为该 owner，拿的是自己的 token） |
| 注册失败提示范围 | **toast 即时告警 + 插件面板持久栏 + AI 可查**（status_plugin 并入失败原因，AI 可自愈修 .env） |
| 拨号失败处理策略 | **仅诊断建议，不自动纠正**（报错文本携带来源 IP 建议值，符合证据驱动原则，不掩盖配置错误） |

## 3. 功能 1：`get_plugin_env` MCP 工具

### 3.1 工具定义

- 注册位置：`cmd/gt-mcp/main.go` 工具表（`get_registry_addr` 旁，约 L2513），handler 放 `cmd/gt-mcp/dev_tools.go`（紧邻 `handleGetRegistryAddr`），`capabilities.go` 工具清单同步加入。
- 入参：
  - `host`（可选，string）：显式外部可达主机，语义同 `get_registry_addr` 的 `host`（跨机部署时插件与调用方不在同一视角时覆盖）。
  - `decoder_port`（可选，number，默认 61887）：与 SKILL.md 现行约定端口一致。

### 3.2 返回结构

```jsonc
{
  "registry_addr": "192.168.31.87:19091",       // → GT_REGISTRY_ADDR
  "auth_token": "gt_tok_xxx",                   // → GT_AUTH_TOKEN（调用者自己的）
  "token_source": "env",                        // env | users | anonymous
  "decoder_addr": "0.0.0.0:61887",              // → GT_DECODER_ADDR（推荐监听）
  "decoder_public_addr": "192.168.31.87:61887", // → GT_DECODER_PUBLIC_ADDR
  "env_file": "<完整 .env 文本，含逐行注释，可直接写入>",
  "notes": "<每字段一句填错后果说明>"
}
```

### 3.3 取值来源（全部复用现有逻辑）

- `registry_addr`：复用 `advertisedAddrs`（`GT_PUBLIC_HOST` 显式通告优先，否则按请求 Host 回推）。
- `auth_token`：复用 `cmd/gt-mcp/oauth.go` `ownerToken` 反查（`tokensByOwner` env 优先、users 表兜底）；匿名模式（无鉴权部署）返回空串并在 `token_source=anonymous` 说明。
- `decoder_public_addr` host 段 = `registry_addr` 的 host 段（即平台外部可达主机；插件与调用方同机时正确，跨机时用 `host` 参数覆盖）。
- `env_file` 示例（由 handler 拼装）：

```env
# 由 gametrace get_plugin_env 生成 —— 复制为 .env，无需手填
GT_REGISTRY_ADDR=192.168.31.87:19091
GT_AUTH_TOKEN=gt_tok_xxx
GT_DECODER_ADDR=0.0.0.0:61887
GT_DECODER_PUBLIC_ADDR=192.168.31.87:61887
```

### 3.4 SKILL.md 同步

`skills/decoder-plugin-guide/SKILL.md` 步骤 0 改为「调 `get_plugin_env` 一次，把 `env_file` 原样写入 `.env`」，删除「GT_AUTH_TOKEN 是唯一手填项」表述及 `get_registry_addr` 回填流程（保留 `get_registry_addr` 工具本身，capabilities 工作流仍在用）。

## 4. 功能 2：解码器注册失败可见性

### 4.1 后端捕获（`pkg/plugin/manager.go`）

`Register` 中 `dialDecoder` 失败（`!tunnel` 分支）时：

1. **错误文本增强**（随 Register RPC 返回给 SDK，自然出现在插件控制台 / AI 激活日志）：
   - 基础格式：`dial plugin socket <addr>: <err>; 注册连接来源 IP <srcIP>，可尝试 GT_DECODER_PUBLIC_ADDR=<srcIP>:<port>`
   - host 为 `127.0.0.1` / `localhost` 时追加 Docker 场景提示（平台在容器内回拨的是容器自身）。
   - 来源 IP 取 `grpc peer.FromContext(ctx)`，仅作建议文本，不做自动纠正。
2. **emit 新事件** `PluginEventRegisterFailed`（`PluginEvent` 结构新增 `SocketPath`、`Error`、`Owner` 字段；其余事件这些字段为零值）。
3. **内存 ring buffer**（容量 20，条目 TTL 15 分钟）：
   - 按 `name+socket_path+error` 去重，同 key 5 分钟内不重复记录/发事件（SDK 以 1→30s 退避无限重试，不去重会刷屏）。
   - key 变化（用户改了 .env 重启）立即记录。
   - 存放于 `RegistryServer`，`Manager` 暴露查询方法（同 `Subscribe` 的委托模式）。

### 4.2 proto 变更（`pkg/internalipc/proto/internal.proto` + protoc 重新生成）

生成文件已入库（protoc-gen-go v1.36.11 / protoc v3.21.4），改后按现有工具链重新生成：

- `PluginEvent` 增加 `socket_path = 6`、`error = 7`、`owner = 8`。
- 新 message：

```proto
message PluginFailure {
  string name = 1;
  string socket_path = 2;
  string error = 3;
  string owner = 4;       // 注册方属主（空串 = 匿名）
  int64 timestamp_unix = 5;
}
```

- `ListPluginsResponse` 增加 `repeated PluginFailure recent_failures = 2`。

### 4.3 通路改点（pipeline → gt-mcp → 前端）

- `cmd/gt-pipeline/pipeline_service.go`：
  - `SubscribePlugins` 适配器透传新字段（SocketPath/Error/Owner）。
  - Engine 接口（`pkg/internalipc/capturecontrol/server.go`）新增 `ListRegisterFailures(ctx) ([]RegisterFailure, error)`（owner 作用域过滤规则同 `ListPlugins`：非 admin 只见自己的+匿名，admin 见全部）；`pipelineService` 实现委托 registry ring buffer。
- `pkg/internalipc/capturecontrol/server.go`：
  - `WatchPlugins` 转发新字段。
  - `ListPlugins` RPC handler 追加 `recent_failures`（调 engine 新方法）。
  - `capturecontrol.PluginEvent` / 新 `RegisterFailure` 结构体同步。
- `cmd/gt-mcp/main.go`：
  - `pluginEventJSON` 增加 `socket_path`、`error`、`owner` 字段；`startPluginEventWatcher` 透传。
  - `handleListRegisteredPlugins` 输出增加 `recent_register_failures`。
  - **SSE owner 过滤（仅新事件）**：`handleEventsSSE` 按订阅者身份过滤——`register_failed` 只投递给 owner 匹配的订阅者（admin 收全部）；现有 4 种事件保持全员广播不变（避免 A 的插件失败打扰 B 弹 toast）。

### 4.4 前端展示（`web/src`）

1. **toast**：`hooks/use-mcp.ts` `usePluginEventStream` 改为解析 `event.data`；`type=register_failed` → `toast.error`（插件名 + 尝试地址 + 错误 + 建议），同时保留原有缓存失效逻辑。
2. **插件面板持久栏**：`components/plugin-panel.tsx` 新增「最近注册失败」区块，数据来自 `list_registered_plugins` 的 `recent_register_failures`（5s 轮询 + SSE 失效刷新双保障，后端 TTL 自动消失），显示 name / 尝试地址 / 错误 / 时间。
3. **类型**：`useRegisteredPlugins` 结果类型扩展。
4. toast 基建复用现有 `components/ui/toast.tsx`。

### 4.5 AI 可查（`cmd/gt-mcp/dev_tools.go`）

`status_plugin` 命中失败记录时并入 runtime 视图，`suggested next_action` 给出「修正 .env 的 `GT_DECODER_PUBLIC_ADDR` 为 <建议值> 后重新 activate」。

## 5. 不改动范围

- SDK 零改动（增强后的错误文本经 Register RPC 返回值自然到达插件控制台）。
- `get_registry_addr` 工具契约不变。
- 隧道模式（`GT_TUNNEL=1`，agent 托管）不受影响——本来就无回拨。

## 6. 测试与验证

- `pkg/plugin`：拨号失败 → 事件 + 失败记录 + 去重 / TTL / owner 过滤（复用现有 `unix:/nonexistent/decoder.sock` 测试素材）。
- `cmd/gt-pipeline`：Engine 新方法的 owner 作用域。
- `cmd/gt-mcp`：`get_plugin_env` 单测（token 反查、匿名模式、.env 文本、host 覆盖）；SSE owner 过滤单测；`list_registered_plugins` 失败列表透传。
- 端到端：前端改动需重建镜像（`//go:embed`），`docker compose build && docker compose up` 后用错误 `GT_DECODER_PUBLIC_ADDR` 起插件，验证 toast / 面板栏 / AI `status_plugin` 三处。
