.PHONY: proto test build build-mcp build-pipeline build-agent build-agents build-examples run-mcp run-pipeline deploy release-matrix web-build docs vet lint test-race verify-plugin coverage-gate bench bench-compare bench-save

TAGS := pcap

# ============================================================================
# 版本信息注入（T14）
#
# VERSION 优先取 git tag（tag 推送触发 CI 时），否则取 `git describe` 的
# 可读近似值，再退回 dev。GIT_COMMIT 是短哈希。CI 的 release job 通过
# make 的命令行变量覆盖，例如：
#   make release-matrix VERSION=v0.5.0 GIT_COMMIT=abc1234
# ============================================================================
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# 构建时间戳：默认留空（-version 会省略该字段）。Windows 下的 make/date 对
# `date` 与 git log 的 `%cI` 处理均不可靠（% 会被 make 吞掉、date 是交互版），
# 因此不在 make 内自动推导；需要时手动传：
#   make deploy BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)   （MSYS/linux）
#   docker compose up 前设 $env:GT_BUILD_TIME=...             （PowerShell）
BUILD_TIME ?=

# 注入 pkg/version 的包级变量（见 pkg/version/version.go）。
LDFLAGS := -s -w \
	-X gametrace/pkg/version.Version=$(VERSION) \
	-X gametrace/pkg/version.Commit=$(GIT_COMMIT) \
	-X gametrace/pkg/version.BuildTime=$(BUILD_TIME)

# ============================================================================
# 交叉编译矩阵（T14 release 产物）
#
# 说明：pcap 采集层是 cgo 依赖（github.com/gopacket/gopacket/pcap），交叉编译
# 无法携带目标平台的 libpcap，因此 release 矩阵统一 CGO_ENABLED=0，且**不带**
# -tags pcap：
#   - 仅 cmd/gt-agent（探针）依赖 pcap：其网卡实时抓包按 pcap / !pcap 构建标签
#     门控，无标签构建可编译，运行时给出明确错误；
#   - pcap 文件源（pcapgo，纯 Go）、gt-pipeline 抓包源（agent 推流 / 移动代理）
#     不受影响；gt-pipeline 自身已无本机网卡抓包（pcaplive 已移除）；
#   - 需要探针网卡抓包的产物用 Docker 镜像（见 Dockerfile，带 libpcap）。
# windows/amd64 产物带 .exe 后缀，其余不带。
# 本 target 只用 POSIX sh 语法（$$ 转义 + for/if），无 GNU make 扩展。
# ============================================================================
RELEASE_PLATFORMS := windows/amd64 linux/amd64 linux/arm64 darwin/arm64
RELEASE_CMDS := gt-pipeline gt-mcp gt-agent


# 只生成 gametrace 自有的进程间控制面 proto。
# 插件线上契约（plugin.proto）已并入仓库内 SDK 子模块 ./sdk，在那边 make proto 生成
# （sdk/Makefile）。gametrace 与 SDK 各生成一份会在 protobuf 全局注册表里撞同一个
# 文件路径并 panic，因此契约代码只由 ./sdk 产出一份。
# 这里只覆盖 gametrace 自有协议中的 internalipc（抓包控制面）。
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/internalipc/proto/internal.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/capture/mobile/proto/mobile.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/capture/agent/proto/agent.proto

test:
	go test -tags $(TAGS) ./...

build-mcp:
	go build -tags $(TAGS) -ldflags '$(LDFLAGS)' -o bin/gt-mcp.exe ./cmd/gt-mcp

build-pipeline:
	go build -tags $(TAGS) -ldflags '$(LDFLAGS)' -o bin/gt-pipeline.exe ./cmd/gt-pipeline

# 移动端流量入口（sing-box 侧 → GameTrace）：TCP 中继 + gRPC 推送连接级数据。
build-agent:
	go build -tags $(TAGS) -o bin/gt-singbox-agent.exe ./cmd/gt-singbox-agent

# ============================================================================
# 多平台下载 agent 预置矩阵（T-Web First） -> build/agents/
#
# 远程 agent 需要在"用户本机"做实时抓包。Docker 镜像已内建 linux/windows/darwin
# 三平台 × amd64/arm64 双架构共 6 份可抓包探针（其中 linux/arm64 受 BUILD_ARM_AGENT
# 控制，见 Dockerfile builder），本 target 供裸机部署
# 或补充平台时预置（产物同样会进 GT_AGENT_BIN_DIR 扫描）：
#   - windows/amd64、windows/arm64：gopacket/pcap 在 Windows 是纯 Go（运行时加载
#     wpcap.dll），CGO_ENABLED=0 即可交叉编译，无需 mingw；
#   - linux/amd64：cgo libpcap，需在 linux(amd64) 宿主构建；
#   - linux/arm64：cgo libpcap，需 aarch64 Linux 宿主构建（含 libpcap-dev）；
#   - darwin/amd64、darwin/arm64：cgo libpcap，只能在 macOS 宿主构建——非 mac
#     宿主会跳过并在结尾打印 WARN（对应下载页平台如实标为不可用）。
# 产物是「通用」gt-agent（不带 embedded 标签），下载时由服务端把
# config.embedded.json 作为 sidecar 打进 zip，运行时从 exe 同目录读取；
# 缺平台的产物未提供时，后端 get_agent_download_options 会如实标记
# 该平台不可下载（不会像旧方案那样回落到服务端本机平台）。
AGENT_PLATFORMS := windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
build-agents:
	set -e; \
	mkdir -p build/agents; \
	failed=""; \
	for platform in $(AGENT_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		cgo="1"; if [ "$$os" = "windows" ]; then cgo="0"; fi; \
		echo "==> CGO_ENABLED=$$cgo GOOS=$$os GOARCH=$$arch -tags pcap go build ./cmd/gt-agent"; \
		if ! CGO_ENABLED=$$cgo GOOS=$$os GOARCH=$$arch go build -tags pcap \
			-o build/agents/gt-agent-$$os-$$arch$$ext ./cmd/gt-agent; then \
			failed="$$failed $$platform"; \
		fi; \
	done; \
	if [ -n "$$failed" ]; then echo "WARN: skipped platforms:$$failed (darwin requires a macOS host)"; fi; \
	ls -la build/agents/

build: build-mcp build-pipeline build-agent

# ============================================================================
# SDK 维护与对外发布（SDK 已并入本 monorepo 的 ./sdk）
#
# ./sdk 是 SDK 源码的唯一真源（module：github.com/OwnSecurityGuard/gametrace/sdk，
# 保留独立 go.mod；根 go.mod 以 replace => ./sdk 消费）。
#
# 对外发布靠【本仓库】的 sdk/vX.Y.Z tag：module 位于子目录，Go 解析
# github.com/OwnSecurityGuard/gametrace/sdk@vX.Y.Z 时查的是本仓库里名为
# sdk/vX.Y.Z 的 tag。裸 vX.Y.Z（无 sdk/ 前缀）无法被 go get 解析。
#
# 历史坑：旧版 sdk-publish 打的是裸 tag（git tag -f v0.9.0）并推给镜像仓库，
# 于是 SDK 从未真正可 go get —— 远程长期只有 sdk/v0.1.1，而文档一直声称
# 外部插件 go get ...@v0.9.0 可用。
# ============================================================================
.PHONY: sdk-test sdk-publish
SDK_VERSION ?= v0.10.0
SDK_UPSTREAM ?= git@github.com:OwnSecurityGuard/gt-plugin-sdk.git

# 在 SDK 子模块内单独跑它自己的测试/构建。
sdk-test:
	cd sdk && go test ./... && go build ./...

# 对外发布 SDK：
#   1. 在本仓库打 sdk/$(SDK_VERSION) 并推 origin —— 只有这一步才让
#      go get github.com/OwnSecurityGuard/gametrace/sdk@$(SDK_VERSION) 真正可用。
#   2. （历史遗留）把 ./sdk 子树推给已退役只读镜像 gt-plugin-sdk 作归档。
#      该镜像不能作为 go get 通道：镜像内 go.mod 声明的 module 是
#      gametrace/sdk，与镜像仓库路径 gt-plugin-sdk 不匹配，Go 不会查它。
#      外部获取一律走上一步的本仓库 tag。
# 用法：make sdk-publish SDK_VERSION=v0.10.0
sdk-publish: sdk-test
	git tag -f sdk/$(SDK_VERSION)
	git push origin sdk/$(SDK_VERSION)
	@test -n "$(SDK_UPSTREAM)" || { echo "SDK_UPSTREAM is empty"; exit 1; }
	git subtree push --prefix sdk $(SDK_UPSTREAM) main --squash || git subtree push --prefix sdk $(SDK_UPSTREAM) main

build-examples:
	go build -tags $(TAGS) -o bin/http-server.exe ./examples/http/server
	go build -tags $(TAGS) -o bin/http-client.exe ./examples/http/client

run-mcp:
	go run -tags $(TAGS) ./cmd/gt-mcp

run-pipeline:
	go run -tags $(TAGS) ./cmd/gt-pipeline

# deploy：Docker 部署与"确认跑的是刚构建的新版本"一键流程。
#
# compose 文件头"镜像策略"已保证每次 up 都走源码构建（pull_policy: build，
# BuildKit 层缓存按内容寻址——源码变化必然重编，详见 compose 文件头说明）。
# 本 target 补两块之前缺失的能力：
#   1) 把宿主 git 的 VERSION / GIT_COMMIT / BUILD_TIME 注入镜像（-version 才
#      有意义，不再是恒定的 dev (unknown)）；
#   2) 部署完自动 exec 各服务打印 -version，给出"确实跑到新代码"的直接证据。
# 用法：make deploy  （依赖 docker compose + git，均为项目既有约定）
deploy:
	@echo "==> build+deploy  gt-server: VERSION=$(VERSION) GIT_COMMIT=$(GIT_COMMIT) BUILD_TIME=$(BUILD_TIME)"
	GT_VERSION="$(VERSION)" GT_GIT_COMMIT="$(GIT_COMMIT)" GT_BUILD_TIME="$(BUILD_TIME)" \
		docker compose up -d --build --force-recreate
	@echo
	@echo "==> 运行版本核对（应与上面 VERSION/GIT_COMMIT/BUILD_TIME 一致才算部署成功）："
	@echo "--- gt-pipeline ---"; docker compose exec pipeline /usr/local/bin/gt-pipeline -version 2>&1 || true
	@echo "--- gt-mcp ---"; docker compose exec mcp /usr/local/bin/gt-mcp -version 2>&1 || true

# 重新生成 docs/mcp-tools.md 的 MCP 工具目录（与 cmd/gt-mcp 下所有工具注册对齐）。
docs:
	go run ./scripts/gen_tool_table

# web-build：构建前端并把产物同步进 gt-mcp 的 embed 目录（cmd/gt-mcp/webui/）。
# vite 产物照常出在 web/dist（vite.config.ts 不改，避免 outDir 清空误删 tracked
# 文件）；这里先清空 webui 旧产物再复制（保留 .gitkeep），重复构建不会积累
# 陈旧 hash 产物。此后 go build ./cmd/gt-mcp 即内嵌最新前端。
# 跑过一次后，未重新 web-build 也不会破坏构建：embed 里的旧产物照常可用。
web-build:
	cd web && npm ci && npm run build
	rm -rf cmd/gt-mcp/webui/assets
	rm -f cmd/gt-mcp/webui/index.html
	cp -r web/dist/. cmd/gt-mcp/webui/

# release-matrix：交叉编译全平台 release 产物到 bin/release/（T14）。
# CI release job 在 tag push（v*）时调用；本地可用 make release-matrix 验证。
# 版本注入见文件头部说明：make release-matrix VERSION=v0.5.0 GIT_COMMIT=abc
# 前置依赖 web-build：release 的 gt-mcp 产物内嵌最新前端（需要本机 node）。
release-matrix: web-build
	set -e; \
	mkdir -p bin/release; \
	for platform in $(RELEASE_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		for cmd in $(RELEASE_CMDS); do \
			echo "==> CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build ./cmd/$$cmd"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
				go build -ldflags '$(LDFLAGS)' \
				-o bin/release/$$cmd-$$os-$$arch$$ext ./cmd/$$cmd; \
		done; \
	done; \
	ls -la bin/release/

# ============================================================================
# 质量门（CI 与本地共用同一入口，避免两边口径漂移）
#
# 仓库不是单 go module：根 + ./sdk + 每个示例插件各自 go.mod（外部插件同款
# 消费方式）。以下 target 全部逐模块执行——只跑根模块会漏掉 SDK/插件单独坏掉
# 的情况，这正是 CI 矩阵按模块拆开的原因。
# ============================================================================

# 所有"像外部用户一样消费 SDK"的插件模块（含 SDK 自带模板 http-stream-decoder）。
PLUGIN_MODULES := examples/http-decoder examples/lp-decoder examples/ws-decoder sdk/examples/http-stream-decoder

# vet：带 pcap 标签跑（覆盖 capture_pcap.go 等门控文件；构建矩阵的不带标签
# 编译由 CI build job 单独兜底）。
vet:
	go vet -tags $(TAGS) ./...
	cd sdk && go vet -tags $(TAGS) ./...

# lint：golangci-lint v2，配置在仓库根 .golangci.yml（各子目录向上自动发现）。
# 需要本机安装：go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
# 存量策略：根模块有历史 errcheck/nilerr/staticcheck 欠账，CI 对 PR 只阻断
# **新增**问题（--new-from-merge-base）；sdk 与 examples 已清零、全量阻断。
lint:
	golangci-lint run ./...
	cd sdk && golangci-lint run ./...
	@for m in $(PLUGIN_MODULES); do \
		echo "==> lint $$m"; \
		(cd $$m && golangci-lint run ./...) || exit 1; \
	done

# test-race：本项目是长期运行的并发服务（capture goroutine → 解码插件 →
# event store → MCP 查询；registry heartbeat / tunnel / session 生命周期），
# 数据竞争只有 -race 能抓到，普通 test 通过不代表安全。
test-race:
	go test -race -tags $(TAGS) ./...
	cd sdk && go test -race ./...
	@for m in $(PLUGIN_MODULES); do \
		echo "==> race $$m"; \
		(cd $$m && go test -race ./...) || exit 1; \
	done

# ============================================================================
# verify-plugin：Plugin Contract CI（GameTrace 特有，普通项目没有这条）。
#
# 核心价值链路 plugin → manifest → contract → semantic rules → event 必须
# 零漂移：contract.yaml（SSOT，go:embed 进 sdk/contract）一改，所有消费者
# 必须当场被下面的测试打回：
#   1. sdk 的 contract/rule/event 自校验测试（规则语义与 checker 本身）；
#   2. 每个示例插件模块 go build + go test——其 main_test.go 固定包含
#      TestManifestContractCheck（声明期契约）与 emit→CheckEvent（输出期契约），
#      即"该插件的 plugin.yaml 与事件输出仍然满足当前 contract"。
# 运行期全链路（registry 注册 → pipeline 解码 → 落库）由根模块 TestSimulate
# （go test ./cmd/gt-pipeline/）覆盖，见 CI 的 test job。
# ============================================================================
verify-plugin:
	cd sdk && go test ./contract/ ./rule/ ./event/
	@for m in $(PLUGIN_MODULES); do \
		echo "==> verify plugin: $$m"; \
		(cd $$m && go build ./... && go test ./...) || exit 1; \
	done

# coverage-gate：核心包覆盖率地板（防回退，不是唯分数论）。
# 门槛表与当前实测解释都在 scripts/coverage_gate.sh 头部。
coverage-gate:
	sh ./scripts/coverage_gate.sh

# bench / bench-compare / bench-save：抓包解码性能基线。
# bench-compare 用 benchstat 对比 bench/baseline.txt，回归超阈值 exit 1。
# 注意：baseline 必须与对比方在同一类机器上采集（CI 用 ubuntu）；换环境先
# make bench-save 重录，否则数字没有可比性。
bench:
	sh ./scripts/bench.sh

bench-compare:
	sh ./scripts/bench_compare.sh

bench-save:
	sh ./scripts/bench.sh --save
