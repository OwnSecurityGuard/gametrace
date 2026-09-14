# GameTrace 代码审查流程（Code Review Process）

> 本标准与 `STANDARD.md` 配套。目标：在不拖慢交付的前提下，拦住 🔴 Blocker 和项目雷区，让代码质量从"参差不齐"收敛到"可预期"。

---

## 1. 适用范围与门槛

- **所有合入 `main` / `master` 的 PR 必须审查**（含文档、配置）。
- 高危模块（以下任一带 `*`，建议 reviewer 含高工二审）：
  - `pkg/probe`、`pkg/spool`、`pkg/plugin`、`cmd/gt-agent`、`cmd/gt-mcp/agent_download.go`
  - 任何 SDK 接线 / 协议版本 / 注册逻辑变更
- 个人分支上的 WIP、纯本地实验可不审查，但合入主干前必须补齐。

---

## 2. 角色与职责

| 角色 | 职责 |
|---|---|
| **Author（作者）** | 自审、写清 PR 描述、回复评论、确保 CI 绿、按结论修改 |
| **Reviewer（审查者）** | 按 `CHECKLIST.md` + `STANDARD.md` 审查，给分级评论，决定是否 approve |
| **Maintainer / 高工** | 高危模块二审；仲裁争议；维护本标准本身 |

- 普通 PR：**≥1 名 Reviewer approve** 即可合入。
- 高危 PR：**≥1 名 Reviewer + 高工（或指定 Maintainer）approve**。
- 作者不得自己 approve 自己的 PR（除非紧急 hotfix 且高工授权）。

---

## 3. 标准流程

```
① 作者自审  →  ② 提 PR（填模板）  →  ③ CI 门禁  →  ④ 人工审查  →  ⑤ 修改/再审  →  ⑥ 合入
```

1. **自审**：作者按 `CHECKLIST.md` 过一遍，本地 `golangci-lint run` + `go test ./...` 已过。
2. **提 PR**：用 `PULL_REQUEST_TEMPLATE.md`，标注是否命中"高危模块"。
3. **CI 门禁**（见 §4）：任一红即打回，不进入人工审查。
4. **人工审查**：Reviewer 在 1 个工作日内给首轮结论（见 §7 SLA）。按 `STANDARD.md` 给 🔴/🟡/💭。
5. **修改/再审**：作者改完推新 commit，Reviewer 复核已解决项；🔴 必须全清，🟡 需显式 waiver。
6. **合入**：squash merge 保持主线干净（或由高工指定的合并策略）；合入后删除特性分支（可选）。

---

## 4. 质量门禁（CI 必过）

以下任一不过，PR 不得合入：

- **`golangci-lint run` 全绿**（配置见附录 A）。
- **`go build ./...` 通过**（含 `pcap` tag 的链路：`go build -tags pcap ./cmd/...`）。
- **`go test ./...` 通过**，且覆盖率不降（关键包见 `STANDARD.md` §3.9 护栏）。
- **前端**：`cmd/gta-mcp` 的 `tsc --noEmit` + `vite build` 通过。
- **契约检查**：插件 `manifest` / `contract.yaml` 通过 SDK `contract.Check`（含 §3.5/§3.6 约束）。

> 门禁配置进 `.github/workflows/ci.yml`（或你们现有的 CI）。本标准附录提供可直接落地的 `.golangci.yml`。

---

## 5. 审查评论规范（怎么写）

每条评论必须包含三要素：

```
🔴 **并发：Mutex 不可重入**
位置：pkg/probe/manager.go:142 的 syncLocked 内调用 nextCmdID()

为什么：该函数要求调用方已持 m.mu，再调会加同把锁的 nextCmdID() 会自锁死
       CaptureControl handler（历史事故：点抓包无反应、服务端不报错）。

建议：改为 nextCmdIDLocked()（已在锁内，不加锁）。
```

- 用 🔴/🟡/💭 前缀，一眼分级。
- 说"为什么"，不只用"不好"。
- 给可执行的"建议"，不是命令（"考虑…因为…"）。
- 夸好的写法：命中雷区却正确处理、补了回归测试、拆得好——明说，建立正向反馈。
- 一次给完整反馈，不一轮只丢一条吊胃口。
- 意图不明时先**问**而非判错（"这里是要支持多卡并发吗？"）。

---

## 6. 争议与升级

- Reviewer 与 Author 对 🟡 是否 waiver 有分歧 → 请 Maintainer（高工）裁定。
- 涉及架构/契约/权限模型的改动，无论大小，先在对齐设计阶段拉高工，不要在 PR 里"先合再改"。
- 发现 🔴 但作者坚持合入 → 拒绝合并并记录原因；必要时升级。

---

## 7. SLA（建议，可按团队密度调整）

| 环节 | 目标 |
|---|---|
| Reviewer 首轮反馈 | ≤ 1 个工作日 |
| 作者响应评论 | ≤ 1 个工作日 |
| 紧急 hotfix | 可即时，但事后补 PR 审查记录 |

---

## 附录 A：推荐 `.golangci.yml`

直接落地到仓库根目录即可启用门禁。按你们现状（Go 1.26.8，含 pcap tag）调过：

```yaml
run:
  timeout: 5m
  go: "1.26"
  build-tags:
    - pcap

linters:
  enable:
    - errcheck        # 漏处理的 error
    - govet           # 包括 printf、lock 复制、loopvar
    - ineffassign
    - staticcheck
    - unused
    - gosimple
    - misspell
    - revive          # 可配置命名/复杂度
    - gocyclo         # 圈复杂度上限
    - bodyclose       # resp.Body 未关
    - nilerr          # 返回 nil error 但已设 err
    - prealloc        # 大 slice 预分配
  disable-all: true

linters-settings:
  gocyclo:
    min-complexity: 25
  revive:
    rules:
      - name: exported
        disabled: false   # 导出符号需注释（文档门槛）

issues:
  exclude-rules:
    # 生成代码 / SDK 第三方跳过
    - path: _test\.go
      linters:
        - gocyclo
    - path: (gt-plugin-sdk|gta-plugin-sdk)/
      linters:
        - all
  max-issues-per-linter: 0
  max-same-issues: 0
```

> 注意：`govet` 的 `loopvar` 检查对 Go <1.22 的循环变量捕获很关键；你们已是 1.26，但仍建议保留以约束遗留写法。

---

## 附录 B：拉取新 SDK tag（防 sumdb 404）

升级 `gt-plugin-sdk` 到刚发布的新 tag 时，`go mod tidy` 可能报
`goproxy.cn/sumdb/...lookup ...404 temporarily unavailable`（sumdb 尚未收录）。解法：

```bash
GONOSUMDB='github.com/OwnSecurityGuard/*' \
GOPROXY='https://goproxy.cn,direct' \
go mod tidy
```

**勿用 `GOSUMDB=off`**（会连带 toolchain 校验失败）。`GONOSUMDB` 是真实变量，默认跟随 `GOPRIVATE`。
