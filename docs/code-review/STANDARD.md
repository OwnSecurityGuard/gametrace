# GameTrace 代码审查标准（Code Review Standard）

> 适用范围：`gametrace` 仓库全部 Go 代码、`cmd/gta-mcp` 前端（React 19 + TanStack Query）、插件（`plugins/*`）、SDK 接线层。
> 配套文档：`PROCESS.md`（流程）、`CHECKLIST.md`（审查勾选单）、`PULL_REQUEST_TEMPLATE.md`（PR 模板）。

---

## 0. 如何使用本标准

- 本标准是**最低门槛**，不是天花板。满足本标准不代表代码优秀，只代表"可以合入"。
- 审查时按 `CHECKLIST.md` 逐项过，命中任一项即按级别给结论。
- 级别定义见 §1。所有评论必须**具体 + 说明为什么 + 给建议**（见 `PROCESS.md` §5），禁止"这能不能改改"式空评。
- 项目专属雷区（§3）是 GameTrace 历史事故沉淀，**命中即至少 🟡 Major**，因为它们曾导致"服务端不报错但功能全死""探针归错 owner""跨 NAT 连不上"等隐蔽故障。

---

## 1. 严重级别定义

| 级别 | 标记 | 含义 | 合入门槛 |
|---|---|---|---|
| Blocker | 🔴 | 正确性/安全/数据风险，或破坏契约 | **必须修**，否则拒绝合并 |
| Major | 🟡 | 可维护/性能/并发隐患，或项目雷区 | 应修；要 waiver 需 reviewer + 作者共同说明理由 |
| Minor | 💭 | 风格、命名、文档、可选优化 | 鼓励修，不阻塞 |

---

## 2. 通用审查维度

### 2.1 正确性与边界 🔴
- 空指针 / nil map / 未初始化 slice、数组越界、off-by-one。
- 数值溢出、除零、负数当无符号、time 时区与零值（`time.Time{}` 非 nil）。
- 边界条件：空输入、超大输入、重复调用、并发首次调用竞态。
- 协议/序列化字段：新增字段是否破坏旧客户端（前向/后向兼容）。

### 2.2 错误处理 🔴
- 返回 `error` 的函数**不得吞掉错误**（`_ = fn()` 需注明为何安全）。
- `if err != nil` 后必须处理或显式 `return`/wrap；不要 `panic` 应付业务错误。
- 用 `fmt.Errorf("...: %w", err)` 保留错误链；不要丢失原始错误再包一层字符串。
- 不要对可达路径 `log.Fatal` / `os.Exit`（会杀掉整个进程，影响其他会话）。
- gRPC/HTTP 错误码语义正确（4xx 客户端错 vs 5xx 服务端错）。

### 2.3 并发安全（Go 专项）🔴
- 共享状态是否有锁保护；锁的粒度与顺序是否一致（防死锁）。
- **禁止在已持锁的函数内再次调用会加同一把锁的函数**（Go Mutex 不可重入，见 §3.1）。
- goroutine 泄漏：是否有退出通道 / `context` 取消传播；`defer wg.Done()` 配对。
- `for` 循环里 `go func` 捕获循环变量（Go <1.22 尤其要 `v := v`）。
- channel 使用：是否可能永久阻塞（无 buffer 且无接收方）、是否重复 close。

### 2.4 安全 🔴
- 认证：受保护接口是否校验 `GT_AUTH_TOKENS` / `X-GT-Owner` / `X-GT-Admin`（缺失即越权）。
- 授权：新资源默认 **creator-only**，不得默认全员可见（见 §3.7）。
- 输入校验：外部输入（探针上报、URL 参数、插件 manifest）必须校验，防注入/越界。
- 路径穿越：涉及文件路径（探针下发、录制文件、上传）必须做 `filepath.Clean` + 目录约束。
- 凭证：token / 私钥**绝不**进日志、不落明文配置；核对 §3.2（probe.json 覆盖旧凭证）。
- 数据层：SQLite 拼接 SQL 必须用参数化（`?` 占位），禁止字符串拼接。

### 2.5 性能 🟡
- N+1：循环内查库 / 循环内网络调用 → 批量化。
- 不必要的分配与拷贝（大 slice 反复 append 无 `make(..., 0, n)`、字符串拼接用 `+`）。
- 热路径上的锁竞争、重复解析（如每包都 `json.Unmarshal` 固定 schema）。
- 抓包/解码链路：避免每事件新建大对象，关注 GC 压力（这是 ingestion 核心路径）。

### 2.6 测试 🟡
- 关键路径（协议解析、状态机、权限判定、不丢包队列）必须有测试。
- 修复 bug 的 PR **必须带回归测试**。
- 表驱动测试 + 边界用例；不要只测 happy path。
- 测试可重复、无外部依赖（见 §3 中"非封闭测试"警告）——mock 外部临时文件/网络。

### 2.7 可维护性 💭
- 命名自解释；函数不超过一屏（~80 行），单一职责。
- 魔法数字 / 硬编码端口 / 字符串提取为常量或配置。
- 注释解释**为什么**（intent），而非复述代码做什么。
- 不引入未使用的抽象层、不"为将来"预留 project_id/角色表等（见 §3.7）。

---

## 3. 项目专属雷区（必查清单）

> 每条都来自真实事故。审查插件、探针、解码链路、部署配置时逐条对照。

| # | 雷区 | 触发场景 | 怎么查 | 级别 |
|---|---|---|---|---|
| 3.1 | **Mutex 不可重入** | 改 `pkg/probe` 锁内逻辑 | 锁内是否调 `nextCmdID()` 而非 `nextCmdIDLocked()`；是否调了会重新加 `m.mu` 的函数 | 🔴 |
| 3.2 | **探针身份持久化** | 改 `cmd/gt-agent/config.go`、`suppliedConfig.adopt` | 本次下发的身份/回连目标是否覆盖 `probe.json` 旧值并作废旧凭证；`%AppData%\gt-agent\probe.json` 不被重下清除 | 🔴 |
| 3.3 | **跨 NAT 回连地址** | 改下发/回连逻辑 | 是否仍按请求 Host 回推；`GT_PUBLIC_HOST/REGISTRY_PORT/INGEST_PORT` 是否可被外部配置覆盖 | 🟡 |
| 3.4 | **docker compose 注册端口 19091** | 改插件启动/SDK 接线 | 宿主手动跑插件是否显式 `GT_REGISTRY_ADDR=127.0.0.1:19091`；容器内是否用 `pipeline:9091` | 🟡 |
| 3.5 | **SDK 单真源** | 引入/升级 SDK、改 go.mod replace | 是否只依赖仓库内 `./sdk`（module `github.com/OwnSecurityGuard/gametrace/sdk`）；replace 是否误指向已退役的 `gt-plugin-sdk` / `gta-plugin-sdk` 本地 checkout —— 那会引入第二份 SDK 定义，症状是 `cannot use decodePacket as sdk.DecodeFuncV2` 型编译失败（旧名时代是 proto 撞名 panic） | 🔴 |
| 3.6 | **协议 major 版本匹配** | 改 manifest / 注册 | plugin manifest `api_version` major 与 manager 要求一致，否则注册被拒 | 🔴 |
| 3.7 | **资源权限默认 creator-only** | 新增资源/接口 | 未显式共享时，新资源是否仅创建者可见；是否提前建了不必要的 project_id/角色表 | 🟡 |
| 3.8 | **DecodeV2 响应契约** | 写/改解码插件 | 是否先 `Send(Done:false 载荷)` 再 `Send(Done:true 终止)`；把 payload 塞进 `Done:true` 会导致 events 恒为 0 | 🔴 |
| 3.9 | **pkg/spool 改动** | 改队列/落盘 | 是否先读过 `queue_test.go`（381 行护栏，不丢包核心）；改动是否破坏"不丢包"保证 | 🔴 |
| 3.10 | **状态维度分裂** | 改探针/会话状态 | 是否把 connection/capture/data 压回单一 `status`；UI 是否用独立 chip | 🟡 |
| 3.11 | **schema ID 命名** | 改插件 manifest | schema/contract ID 是否含连字符（只允许 `_` 和 `.`，如 `godot_gateway.request.v1`） | 💭 |
| 3.12 | **以用户任务为中心** | 改前端/API 设计 | 一级入口是否面向"抓这台服务器"而非"操作 probe"；基础设施是否藏进「管理>」 | 💭 |

---

## 4. Go 专项速查

- `context.Context` 作为第一个参数贯穿 IO/超时调用；不要存进 struct。
- `defer` 在循环/大函数里注意执行时机与资源释放（文件、事务）。
- 错误比较用 `errors.Is` / `errors.As`，不用字符串相等。
- `sync.Pool` 复用大对象需重置，避免跨 pool 泄漏状态。
- 不要 `panic` 做流程控制；`recover` 仅限顶层 goroutine 边界。
- 接口最小化（accept interfaces, return concrete）。
- `go.mod` 升级 SDK 遵守 §3.5 / §3.6；新 tag 拉取见 `PROCESS.md` 附录。

---

## 5. 前端专项（cmd/gta-mcp：React 19 + Vite + TanStack Query v5）

- 类型安全：API 响应有 TS 类型，禁止 `any` 兜底业务数据；后端契约变更同步前端类型。
- Query 管理：用稳定 `queryKey`；写操作走 `useMutation` + 失效缓存，不要手动刷新整个树。
- 错误边界：网络/解码失败有 UI 兜底，不白屏；错误上报带上下文。
- 状态呈现：按 §3.10 用独立 chip 显示 connection/capture/data，不合并成单灯。
- 凭证：`gt_auth_token` 存 localStorage，不得在代码里硬编码或打印。
- 安全：所有受保护请求带 `X-GT-*` 头；前端不做权限决策（后端为准）。

---

## 6. 提交与 PR 规范

- Commit message：中文或英文均可，但需说明**为什么**（intent），不止 what。多行用 `Write` 落 `.git/_cmsg.txt` + `git commit -F`（避免编码问题）。
- 一个 PR 一个关注点；超大 PR（>800 行）建议拆分或先对齐设计。
- PR 描述按 `PULL_REQUEST_TEMPLATE.md` 填：改了什么、为什么、怎么验证、风险点。
- 禁止把 `fmt`/`lint` 修整混入功能 PR（单独 PR 或注明）。
