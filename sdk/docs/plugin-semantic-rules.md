# Protocol Semantic Rule（协议语义规则）

> spec_version 5 新增（contract.components.semantic = 1）；v0.8.2 增补 `name` 效果。
> 设计定调：**插件定义协议语义，平台执行语义规则。**

## 1. 定位与边界

插件告诉 GameTrace："对于我解析出来的 Event JSON，应该怎样判断、提取和解释它的语义。"

```
插件负责                          GameTrace 负责
─────────────                    ─────────────
协议知识                          读取规则
    ↓                            执行规则
 Schema                          产生平台侧的派生语义
 + Semantic Rules                （配对、子事件挂接、标签落库）
 + State
```

插件**定义**规则，不自己生成最终关系实例；GameTrace **执行**规则。
Event 仍是基础数据，规则可以直接访问**整个 Event JSON**（原则 2），不依赖 SDK 固定字段。

## 2. 三段流水线

```
Event JSON ──GJSON 取值──▶ Value ──Predicate 判断──▶ bool ──Effect──▶ 语义
```

- **GJSON** = JSON 数据选择/提取器（`seqId` / `data.targetId` / `headers.x-request-id`），
  不负责规则系统。
- **Predicate** = 最小判断能力（§3）。只做判断，不做业务逻辑；
  不引入 Expr、脚本、自定义函数，避免 SDK 演变成新的 DSL。
- **Effect** = 成立之后意味着什么（§4）。闭集四个：pair / extract / annotate / name
  （`name` 自 v0.8.2 起）。

求值分两遍：`name` 效果先跑并注入 `_meta.msg_name`，其余效果后跑。

## 3. Predicate

叶子节点（GJSON path 取值后判断一次）：

| op | 含义 | 需要 value |
|----|------|-----------|
| eq / neq | 相等 / 不等（kind 感知：数字、字符串、布尔、null 各自比较，不隐式转型） | 是 |
| exists / not_exists | path 是否存在 | 否 |
| gt / gte / lt / lte | 数字按数值序，字符串按字典序 | 是 |
| in / not_in | value 必须是数组，做成员判定 | 是（数组） |
| contains | 字符串包含子串；数组包含元素 | 是 |
| prefix / suffix | 前缀 / 后缀 | 是 |

**缺失语义**：除 exists / not_exists 外，path 取值不存在时判 false。
"缺失"检查交给 exists / not_exists 显式表达。

组合器：`all`（AND）/ `any`（OR），可嵌套。同一节点内组合器与叶子条件互斥，
`all` 与 `any` 互斥。manifest 中最常见的写法是序列简写，**隐式 all**：

```yaml
when: [ { path: seqId, op: neq, value: 0 }, { path: uri, op: prefix, value: "/battle" } ]
```

## 4. Effect

### 4.1 pair —— 事件配对（Request ↔ Response）

```yaml
- id: my_game.pair_request_response
  when:
    - { path: seqId, op: neq, value: 0 }
    - { path: direction, op: in, value: [client_to_server, server_to_client] }
  effect:
    type: pair
    sides:                          # 恰好 2 个：每侧一个 key + 角色判定
      - { path: direction, op: eq, value: client_to_server, key: seqId }
      - { path: direction, op: eq, value: server_to_client, key: meta.req_seq }
```

**key 是 per-side 的**：每个 side 的 `key` 是"该侧事件"中配对键的 GJSON path。
两侧把关联值放在结构或字段名不同的位置时，各自声明自己的 path —— 请求侧
`seqId`、响应侧 `meta.req_seq`，只要字符串化取值相等即可配对。不再有规则级
`effect.key`。

平台执行语义（`rule.MatchPair`）：两个事件**同规则**、各匹配一个**不同 side**，
且各自 `key` 提取值**字符串化相等** → 构成配对。

同一机制通吃 HTTP/2（`streamId`）、游戏自定义协议（`seqId`）：SDK 不需要知道
协议是什么。事件满足 when 但两侧角色判定都不匹配（或命中侧的 key 缺失）时，
该事件不产出 PairHit（不扮演角色）。

### 4.2 extract —— 子事件提取（SyncDbData 问题）

一个网络 Event 内可能包含多个逻辑协议事件。子事件保留与父事件的来源关系，
**不是伪装成三条新的网络包**。

```yaml
- id: my_game.extract_sync_db
  when: [ { path: data.SyncDbData, op: exists } ]
  effect:
    type: extract
    source: data.SyncDbData         # 指向数组（每元素一个子事件）或对象（单个）
    child:
      event_type: my_game.db.update
      schema_id: my_game.db_update.v1   # 子事件的线上 schema 标识
```

source 指向标量或 null 时静默产出 0 个子事件（`SyncDbData: null` 是合法形态）。

### 4.3 annotate —— 语义标注

```yaml
- id: my_game.mark_error
  when: [ { path: error, op: exists } ]
  effect:
    type: annotate
    semantic: error
```

semantic 闭集：`request` / `response` / `notification` / `error`。
**Error 与 Push/Notification 是 Event 的语义属性，不是 Relation**（§8）。

### 4.4 name —— 消息命名（v0.8.2+）

```yaml
- id: my_game.name_message
  when: [ { path: type, op: exists } ]
  effect:
    type: name
    key: type                       # 消息名称的 GJSON path
```

把"这条消息叫什么"从解码器代码里挪到声明里：宿主把首个命中写入 `meta.msg_name`，
**规则声明优先于解码器硬编码**。

求值顺序是两遍（见 §7）：`name` 规则先执行，提取到的值被注入求值视图的
`_meta.msg_name`，然后才评估其余效果。因此后续规则的 `when` 可以直接按
`_meta.msg_name` 分支：

```yaml
- id: my_game.mark_login_error
  when: [ { path: _meta.msg_name, op: eq, value: LoginRsp } ]
  effect: { type: annotate, semantic: error }
```

key 取值不存在或字符串化为空时该规则静默不命中。

## 5. 语义模型全景

```
Event
├── Schema
├── Semantic（annotate：request / response / notification / error）
├── Relations（pair）
├── Extraction（child events）
└── Name（meta.msg_name，v0.8.2+）
```

> 注：`_state_changes` 走 **Analysis 通道**（v0.8.0 三段分离），宿主投影到
> `state_changes` 表，是实体/状态变更分析的唯一输入；`states` 声明与 `state` 包已移除。

## 6. 明确不做（第一版）

- **Group / Behavior Group / Impact Window**："请求之后 N 秒内的事件"不可靠；
  等出现 battleId / transactionId 等稳定协议事实再加。
- **高级因果关系**：caused-by / retry-of / timeout-of / cancel-of / depends-on /
  supersedes 等，真实需求未证明第一阶段必需。
- **Expr / 脚本 / 自定义函数**：防止规则层 DSL 化。

## 7. SDK API 速览

```go
// 声明期（宿主注册期 / plugin.verify）
issues := rule.RulesReport(m.SemanticRules)      // 全量声明校验
report := contract.NewPluginChecker().Check(m)   // 已并入 semantic 层

// 运行期
res, err := rule.Evaluate(m.SemanticRules, payloadValue)
// res.Semantics  → annotate 命中
// res.Pairs      → pair 命中（rule.MatchPair 判定两个 hit 可否配对）
// res.Children   → extract 子事件（宿主挂接父事件来源关系）
// res.Names      → name 命中（v0.8.2+，宿主取首个写入 meta.msg_name）

// plugin.verify 的 CheckEvent 已内置 semantic 层：评估失败报
// gt.semantic.evaluate-failed。
```

规则 ID 约束：点分小写段（段内 `[a-z][a-z0-9_]*`），不带版本后缀。机器可读规则清单见 `contract/contract.yaml` 的 `gt.semantic.*` 段。

## 8. 完整示例

四条规则覆盖当前真实协议形态：

```yaml
semantic_rules:
  - id: my_game.pair_request_response
    when:
      - { path: seqId, op: neq, value: 0 }
      - { path: direction, op: in, value: [client_to_server, server_to_client] }
    effect: { type: pair, sides: [ { key: seqId, ... }, { key: meta.req_seq, ... } ] }   # §4.1

  - id: my_game.mark_push
    when: [ { path: seqId, op: eq, value: 0 } ]
    effect: { type: annotate, semantic: notification }

  - id: my_game.extract_sync_db
    when: [ { path: data.SyncDbData, op: exists } ]
    effect: { type: extract, source: data.SyncDbData, child: { ... } }  # §4.2

  - id: my_game.name_message
    when: [ { path: type, op: exists } ]
    effect: { type: name, key: type }                                    # §4.4

  - id: my_game.mark_error
    when: [ { path: error, op: exists } ]
    effect: { type: annotate, semantic: error }
```

平台最终得到：

```
Request
   ↕ (pair)
Response
   ├── semantic: error
   ├── msg_name: LoginRsp (name)
   ├── Child Event A (extract)
   ├── Child Event B
   └── Child Event C
Notification (annotate)
```

完整可运行示例见 `contract/checker_semantic_test.go` 与 `rule/rule_test.go`。
