# Pull Request 模板

> 复制以下内容到 PR 描述。命中"高危模块"请在标题加 `[高危]` 前缀。

## 改了什么（What）
<!-- 一句话概括 + 关键文件清单 -->

## 为什么（Why）
<!-- 背景、动机、关联的 issue/设计决策。不止 what，要 why。 -->

## 怎么验证（How to verify）
<!-- 本地命令、手测步骤、涉及端口/环境。例：GT_REGISTRY_ADDR=127.0.0.1:19091 起插件 -->

## 风险与影响面
<!-- 是否动到：pkg/probe / pkg/spool / pkg/plugin / cmd/gt-agent / SDK 接线 / 协议版本 -->
<!-- 是否影响已部署探针、历史数据、前端契约 -->

## 高危模块自检（命中打 ✅）
- [ ] 改动了 `pkg/probe` / `pkg/spool` / `pkg/plugin` / `cmd/gt-agent` / `agent_download.go`
- [ ] 涉及 SDK 接线 / 协议版本 / 插件注册
- [ ] 如命中：已按 `docs/code-review/STANDARD.md` §3 雷区逐条核对，并说明处理

## 自审结果
- [ ] 已跑 `golangci-lint run` + `go test ./...`（+ `go build -tags pcap ./...`）
- [ ] 已按 `docs/code-review/CHECKLIST.md` 自审
- [ ] 修复类 PR 已带回归测试

## 审查结论（Reviewer 填）
- 级别：✅ Approve / 🟡 Approve with minor / 🔴 Changes requested
- 关键评论归档于 PR conversation，雷区命中见 `STANDARD.md` §3 对应条目
