# http-stream-decoder — 参考插件

一个完整可运行的 HTTP 解码插件，演示 SDK 的完整使用链路：

```
capture frame → framing.ExtractL7 → framing.Reassembler → HTTP 消息解析 → event
```

## 文件

| 文件 | 职责 |
|---|---|
| `main.go` | 入口：`sdk.RunRegisterLoop` + 跨 Decode 调用持久化的 Reassembler / 流计数器 |
| `http.go` | 用标准库 `net/http` 从重组后的流字节解析单条 HTTP 消息（请求/响应），精确计算消费字节数 |
| `emit.go` | 把消息组装成事件（含 `_state_changes` 与 Meta 通道的 direction）并发送 |
| `plugin.yaml` | semantic_rules 语义契约声明（pair / annotate） |

## 解码行为

- `DecodeRequest.payload` 是**完整链路层帧**；`framing.ExtractL7(payload, link_type)` 负责剥离。
- TCP 流通过 `framing.Reassembler` 跨包重组；解析不完整时保留字节等待后续包。
- 请求/响应按 `FlowKey.Canonical()`（方向无关的流标识）作为 `CorrelationKey` 关联。
- 每个 `input_id` 最终必须回 `done=true`（本例在 `decode` 尾部统一发送）。
- 超过 4MB 仍无法解析的流直接丢弃，防止内存无限增长。

## 构建

本示例位于 SDK 模块内部，直接构建：

```bash
cd gt-plugin-sdk
go build -o http-stream-decoder.exe ./examples/http-stream-decoder
```

## 运行（接入 GameTrace 宿主）

方式一：独立进程注册（pipeline 在 `:9091` 监听插件注册时）：

```bash
GT_REGISTRY_ADDR=127.0.0.1:9091 ./http-stream-decoder
```

方式二：通过 gametrace-mcp 开发工具链（推荐，见主项目 `get_plugin_dev_guide`）：
把本目录连同 `plugin.yaml` 复制为独立模块（参考主项目 `plugins/go.mod.template`），
然后走 `build_plugin` → `activate_plugin` → `verify_plugin` 闭环。

## 契约要点（v0.9.0）

- 事件类型 `http.request` / `http.response`。
- 协议语义全部声明为 `semantic_rules`，由平台执行：
  - `http.pair_request_response`：同 flow 内按 `flow_id` 配对请求与响应，
    方向角色经 `_meta.direction` 判别（插件把 direction 写入 Meta 通道，
    平台将 Meta 并入规则求值视图的 `_meta` 键）。
  - `http.mark_error`：状态码 4xx/5xx 标注为 `error`。
- 每流消息计数（`requests` / `responses`）随事件上报，并通过 `_state_changes`
  描述状态变更，供平台实体基线投影。
