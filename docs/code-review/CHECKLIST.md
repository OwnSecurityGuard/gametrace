# GameTrace 代码审查勾选单（Reviewer Checklist）

> 复制本单到 PR 评论，逐项打 ✅/❌。命中 ❌ 按 `STANDARD.md` 级别给结论。

## 一、通用维度
- [ ] **正确性**：空指针/nil map/越界/溢出/时区零值均无遗漏
- [ ] **错误处理**：无吞错（`_ =` 需注明）；error 用 `%w` 保留链；无业务路径 `panic`/`log.Fatal`
- [ ] **并发安全**：共享状态有锁；无重复加同一把锁；goroutine 有退出机制；循环变量捕获正确
- [ ] **安全**：认证(`GT_AUTH_TOKENS`/`X-GT-*`)齐全；输入校验；路径无穿越；凭证不落日志；SQL 参数化
- [ ] **性能**：无 N+1；热路径(解码/ingestion)无多余分配
- [ ] **测试**：关键路径有测试；修 bug 带回归测试；测试可重复无外部依赖
- [ ] **可维护性**：命名/单一职责/无"为将来"预留抽象

## 二、项目专属雷区（逐项必查）
- [ ] **3.1 Mutex 不可重入**：`pkg/probe` 锁内只用 `nextCmdIDLocked()`
- [ ] **3.2 探针身份持久化**：本次下发覆盖 `probe.json` 旧值并作废旧凭证
- [ ] **3.3 跨 NAT 回连**：`GT_PUBLIC_*` 可外部配置覆盖，不硬推请求 Host
- [ ] **3.4 compose 注册端口 19091**：宿主跑插件显式 `GT_REGISTRY_ADDR=127.0.0.1:19091`
- [ ] **3.5 SDK 单真源**：replace 未指向已退役的 `gt-plugin-sdk` / `gta-plugin-sdk` checkout（否则引入第二份 SDK，编译报 `sdk.DecodeFuncV2` 类型不匹配）
- [ ] **3.6 协议 major 匹配**：manifest `api_version` major 与 manager 一致
- [ ] **3.7 权限 creator-only**：新资源默认仅创建者可见，无多余 project_id/角色表
- [ ] **3.8 DecodeV2 契约**：先 `Send(Done:false 载荷)` 再 `Send(Done:true 终止)`
- [ ] **3.9 pkg/spool**：改动未破坏 `queue_test.go` 不丢包保证
- [ ] **3.10 状态维度**：connection/capture/data 未压回单一 status
- [ ] **3.11 schema ID 命名**：仅 `_` 和 `.`，无连字符
- [ ] **3.12 用户任务为中心**：入口面向"抓这台服务器"，基础设施藏「管理>」

## 三、前端（cmd/gta-mcp，如改动）
- [ ] 类型安全无 `any` 兜底业务数据；`queryKey` 稳定；写走 `useMutation` 失效缓存
- [ ] 错误边界不白屏；`gt_auth_token` 未硬编码/打印；受保护请求带 `X-GT-*`
- [ ] 状态用独立 chip 呈现（3.10）

## 四、提交与 PR
- [ ] commit message 说明 why；PR 按模板填（改了啥/为什么/怎么验证/风险）
- [ ] 单 PR 单关注点；lint/format 修整未混入功能改动

## 五、门禁（CI 必绿，作者自证）
- [ ] `golangci-lint run` 通过（`.golangci.yml`）
- [ ] `go build -tags pcap ./...` 通过
- [ ] `go test ./...` 通过，覆盖率不降
- [ ] 前端 `tsc --noEmit` + `vite build` 通过（如改动）

---
**结论**：✅ Approve  /  🟡 Approve with minor  /  🔴 Changes requested
