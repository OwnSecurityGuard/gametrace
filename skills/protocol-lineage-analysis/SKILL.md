---
name: "protocol-lineage-analysis"
description: "分析 GameTrace 抓取会话的协议血缘（请求-响应配对、消息命名与语义标签）：先查会话绑定的解码插件启用了哪些 semantic_rules，再依据启用的规则选择可用的血缘查询（correlation_id / causation_id / meta.msg_name / meta.semantic），按用户需求给出对话式分析结论。当用户想分析抓包事件之间的因果关系、请求响应配对、追踪某条消息的来龙去脉，或问『这个响应是哪个请求触发的』时调用。"
---

# GameTrace 协议血缘分析

## 目的

对已抓取的会话做**协议血缘分析**：搞清楚事件之间"谁引发谁、谁和谁是一组"。血缘字段全部由平台按解码插件声明的 semantic_rules 生成，因此分析的第一步永远是**确认插件启用了什么规则**——有什么规则，才有什么血缘；没启用的规则，对应字段一律为空，不可臆测。

## 何时使用

- 用户问"这个响应/事件是由哪条消息触发的"
- 用户要梳理某次交互的完整请求-响应链路
- 用户要按消息名或语义标签筛选、分组事件流
- 用户描述"事件之间的因果关系 / 调用链 / 血缘"类需求

不适用：纯业务字段统计（直接用 `list_decoded_data` + filter 即可，无需本 skill 的血缘判定步骤）。

## 前置：MCP 鉴权（无 token 时引导浏览器授权）

调用平台 MCP 工具需要 Bearer token。**收到 401 时不要让用户去找 token 配置文件**，走平台内置的 OAuth 浏览器授权（与飞书授权同体验）：

1. MCP 规范客户端（Trae/Claude/Cursor 等）会在 401 后自动发现授权端点并拉起浏览器；用户只需在授权页**登录并点击同意**，agent 自动经 PKCE 换回用户 token。
2. 客户端不支持自动发现时，手动走同一流程（平台地址记为 `<base>`，如 `http://127.0.0.1:8080`）：
   - `GET <base>/.well-known/oauth-authorization-server` → 取 `authorization_endpoint` / `token_endpoint`
   - `POST <base>/oauth/register`（RFC 7591 动态注册）→ 得 `client_id`
   - 生成 `code_verifier`，算 `code_challenge = BASE64URL(SHA256(verifier))`，打开浏览器访问
     `<base>/oauth/authorize?client_id=…&redirect_uri=…&response_type=code&code_challenge=…&code_challenge_method=S256`
   - 用户在浏览器**手动登录并同意**后，用回调的 `code` `POST <base>/oauth/token`（带 `code_verifier`）换 token
3. 拿到 token 后所有 MCP 调用带 `Authorization: Bearer <token>` 头。

注意：匿名模式（平台未配置任何 token）下不会出现 401，也无需授权，跳过本节。

## 核心概念：规则 → 血缘能力映射

semantic_rules 的 3 种 effect 各自点亮一种血缘能力。**插件启用了该规则，就默认规则有效、字段可信**（规则语义正确性由插件开发流程保证，分析时不复查）：

| manifest 中的 effect | 点亮的血缘能力 | 查询字段 | 语义 |
|---|---|---|---|
| `pair` | 请求-响应配对 | `correlation_id`、`causation_id` | 同一配对组两事件 `correlation_id` 相同（= 请求方事件 id）；响应方 `causation_id` 指向请求方事件 id |
| `name` | 消息命名 | `meta.msg_name` | 从 payload 提取的消息名，按名分组/过滤的基础 |
| `annotate` | 语义标签 | `meta.semantic` | 命中标签数组，如 `["login", "auth"]` |

规则未启用 ⇒ 对应字段恒为空值 ⇒ 该维度血缘**不存在**，明确告知用户并止步，不得用时间相邻、字段相似等启发式去"猜"血缘。

## 工作流程

### 1. 定位会话与解码插件

调 `list_all_sessions` 找到目标会话，取其 `plugin` 字段（会话绑定的解码插件名）。用户没给 session_id 时，按时间/协议/状态列出候选会话让用户选，不要默认取第一个。

### 2. 读取 manifest，判断启用了哪些规则

调 `get_plugin_manifest`（参数 `name` = 上一步插件名）拿到 plugin.yaml 原文，解析 `semantic_rules:` 段，逐条列出：

- 规则 `id`
- `effect.type`（pair / annotate / name）
- `effect` 关键参数：pair 看 `sides`（Side 0 = 请求方、Side 1 = 响应方）与各侧 `key`；name 看 `key`；annotate 看 `semantic`

把启用的规则整理成上面的"能力映射"表告诉用户：本会话能做什么维度的血缘分析。**没有 semantic_rules 段或段为空**时，说明该插件未启用任何规则，只能做无血缘的原始事件浏览。

### 3. 与用户对齐分析需求

依据可用能力，向用户确认分析目标。有疑问必须问清，典型需要确认的点：

- 用户要的"关系"具体是哪种：请求-响应（pair）？还是只是同类消息归类（name/annotate）？
- 用户要的维度插件不支持时，说明缺什么规则、影响什么结论，让用户决定是否降级分析（如只按时间线叙述）
- 用户口中的业务术语（"一次登录"、"一轮同步"）与规则的对应关系，不确定就问

### 4. 血缘查询

用 `list_decoded_data` + `filter`（expr 语法）执行查询。常用模式速查：

| 目的 | filter 示例 |
|---|---|
| 拉取一个完整请求-响应组 | `correlation_id == "<请求方事件id>"` |
| 谁响应了某条请求 | `causation_id == "<请求方事件id>"` |
| 某响应由哪条请求引发 | 取该响应行的 `causation_id`，再 `id == "<该值>"` 查请求 |
| 按消息名过滤 | `meta.msg_name == "UpgradeReq"` |
| 按语义标签过滤 | `"login" in meta.semantic` |
| 血缘 + 业务字段组合 | `correlation_id == "req-1" && data.hp < 10` |

查询要点：

- 事件行本身就带 `correlation_id` / `causation_id`，先无过滤（或按业务条件）拉一批事件，从行内字段出发做二次过滤往往比盲猜 filter 更快
- `limit` 默认 100；事件多时分页或先收窄业务条件
- 方向看 `meta.direction`（`client_to_server` / `server_to_client`，前端显示为 C→S / S→C）

### 5. 对话式结论

分析结果**以对话形式给出结论**，不生成报告文件。结构建议：

1. **一句话结论**：直接回答用户的问题（如"这轮升级失败是服务端在 UpgradeResp 里明确拒绝了"）
2. **证据链**：按时间序列出参与事件（消息名 + 方向 + 关键业务字段 + 血缘关系），血缘关系显式标注（"↑ 由 UpgradeReq(id=…) 触发"）
3. **边界说明**：哪些结论基于启用的规则（可信），哪些是合理推断（标注"推断"）；插件未启用的规则维度导致的留白，明确说出来

## 红线

- 血缘判定**只依据插件启用的规则**：规则在 manifest 里存在 ⇒ 字段可信；不存在 ⇒ 该维度血缘不存在。禁止用时间相邻、负载相似等启发式补血缘。
- 不确定用户意图、规则语义有歧义时，先问再做，不猜。
- 结论只陈述数据支持的事实；推断必须显式标注，与事实区分。
- 修改类操作（delete_session 等）与本 skill 无关，不要顺手调用。

## 历史

本 skill 曾覆盖 `extract` 效果产出的父子血缘（`parent_id`：一条快照消息拆出的多个子事件）。该效果已于 2026-09-18 随平台整体删除——"一个包产出多条事件"是 DecodeV2 响应切片（`r.Events`）的原生能力，走解码器能同时拿到子事件的 `meta` 与状态投影，比走规则声明更完整。因此现在没有"父子层级"这个血缘维度：若用户问"某条大消息拆出了哪些子事件"，正确答法是**查该时刻由同一连接产生的相邻事件**（属推断，须标注），或让插件作者在解码器里直接发多条事件。
