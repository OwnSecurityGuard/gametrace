# 团队部署指南

本文面向**服务器管理员（owner）**：在一台 Linux 服务器上用 Docker Compose 部署共享的 GameTrace 服务端，供团队成员用 `gt-agent` 接入。成员本机的上手步骤见 [成员上手指南](member-onboarding.md)。

## 架构总览

```
成员 A 本机                          服务器（Docker Compose）                成员的 AI Agent / 浏览器
┌──────────────────────┐            ┌──────────────────────────────────┐   ┌──────────────────────┐
│ gt-agent            │            │ gt-pipeline 容器                 │   │ gt-mcp 客户端        │
│  ├─ 抓包推流 ─────────┼── :9092 ──►│  AgentIngest gRPC                │   │  Authorization:      │
│  └─ 托管插件 ─────────┼── :9091 ──►│  PluginRegistry gRPC（隧道注册）  │   │   Bearer gt_xxx ────┼──► :8781
└──────────────────────┘            │  CaptureControl gRPC :9888       │   └──────────────────────┘
                                    │  └─ control.sqlite / sessions/   │
                                    │ gt-mcp 容器 ── HTTP/SSE :8781 ──┼──► 前端 / MCP 工具
                                    └──────────────────────────────────┘
```

| 端口 | 用途 | 谁连进来 |
|------|------|----------|
| 9888 | CaptureControl gRPC | gt-mcp（compose 内已互连，一般不对外） |
| 9091 | PluginRegistry gRPC | 成员本机插件（经 gt-agent 隧道注册） |
| 9092 | AgentIngest gRPC | gt-agent 抓包推流 |
| 8781 | gt-mcp HTTP/SSE（`/sse`+`/message`、`/mcp`、`/events/plugins`） | 团队成员的 AI Agent / 浏览器 |

数据落在共享卷 `/data`（control.sqlite + sessions/，SQLite WAL 模式，pipeline 与 mcp 跨进程并发安全）。

## 1. 准备

服务器要求：Linux + Docker（含 compose 插件）。

```bash
git clone https://github.com/OwnSecurityGuard/gametrace
cd gametrace
cp .env.example .env
```

## 2. 配置 token（`.env`）

编辑 `.env`，必填 `GT_AUTH_TOKENS`，格式为逗号分隔的 `owner=token` 列表：

```dotenv
# 基本格式：GT_AUTH_TOKENS=alice=gt_tok_xxx,bob=gt_tok_yyy
# 需要 admin 权限（看到所有人的会话/插件、管理他人资源）时加 :admin 后缀：
GT_AUTH_TOKENS=alice=gt_tok_aaa:admin,bob=gt_tok_bbb
```

规则（实现见 `pkg/auth/token.go`）：

- 每个 owner 只能有一个 token；token 不能重复；缺 `=`、空 token 等格式错误会导致**启动失败**（不会静默降级为匿名模式）。
- `:admin` 后缀写在 token 之后（`gt_tok_aaa:admin`），实际 token 是 `gt_tok_aaa`。
- 变量留空 = 匿名模式（所有请求放行，owner 统一为 `local`）。**团队部署务必配置**。
- token 是敏感信息，只走环境变量，不进 gametrace.yaml。

生成随机 token：

```bash
openssl rand -hex 24   # 输出前加 gt_tok_ 前缀
```

其余 `.env` 可选项（端口映射 `GT_*_PORT`、容器内监听地址 `GT_*_ADDR`、CORS `GT_MCP_ALLOWED_ORIGINS`、版本注入 `GT_VERSION`/`GT_GIT_COMMIT`）见 `.env.example` 内注释。

### 2.2 自助注册：新成员获取身份

新成员通过自助注册获得个人独立身份（users 表 + 即时生效的 `gt_` token）：

1. **自助注册（默认开放）**：Web UI「设置」弹窗 →「没有令牌？快速开始」输入用户名即可创建身份，无需管理员介入。注册者可立即创建自己的项目、抓原始包；**别人项目的解码插件需要该项目把你加为成员（项目邀请）才能按名解析使用**。接口为 `POST /access/register {"name":"carol"}`；`GT_AUTH_REGISTER=off` 可显式关闭（禁用自助注册）；匿名模式下无意义（恒关闭）。保留名不可注册：env bootstrap 的 owner、匿名 owner `local`、已存在用户。

成员管理（撤销等）：global admin（`:admin` token）可用 `list_users` / `revoke_user` 查看与撤销自助注册用户；撤销即删 users 行，token **立即失效**。env bootstrap 身份（`GT_AUTH_TOKENS`）不在其列，天然不可被撤销。注意：env bootstrap token 默认**不是** global admin，需带 `:admin` 后缀才能使用成员管理工具。

**项目插件共享**：项目 admin 在项目页设置的解码插件条目会记录设置者身份；项目成员（member/admin/owner）开始抓包（`start_capture`、租约抓包、`set_session_plugin` 热切换）时，服务端自动把"所属项目插件的归属 owner"加入解析白名单——成员可以用项目插件，但看不到、也不能用项目之外的其他用户插件。

### 2.3 对外通告地址（远端探针回连，Docker / 公网部署必配）

远端探针（成员机 / 启动码接入的机器）回连的是**服务端对外可达地址**，而非容器内监听地址。Docker / NAT 下这两者不同：容器内是 `:9091`（映射到宿主机 `19091`），容器内网卡还可能拿到 `172.x` 这类对端不可达网段。

下载探针 / 启动码接入时，回连地址（registry + ingest）由服务端 `advertisedAddrs` 解析，**优先级**：

1. **`GT_PUBLIC_HOST`（+ `GT_PUBLIC_REGISTRY_PORT` / `GT_PUBLIC_INGEST_PORT`）显式配置** —— 部署方承诺的地址，最可信，**强烈建议 Docker/公网部署配置**。端口用映射后的宿主端口（`19091` / `19092`）。
2. 未配置时按"浏览器怎么访问到我们"回推（用 MCP 对外 host + 映射端口近似）——同网段/直连能用，跨 NAT/容器会连不上。
3. 都拿不到才退回服务端网卡地址（容器内通常不可达，仅最后兜底）。

配置位置：`.env` 里设 `GT_PUBLIC_HOST` / `GT_PUBLIC_REGISTRY_PORT` / `GT_PUBLIC_INGEST_PORT`，`docker-compose.yml` 的 `mcp` 服务已透传这三个变量（默认 `19091` / `19092`）。未配置 `GT_PUBLIC_HOST` 时，前端「接入设备」会给出黄条提醒，提示运维去配。

### 2.4 代理抓包手机连接地址（同 LAN 容器部署必配）

代理抓包（移动端扫码接入）的二维码里，手机要填的 HTTP CONNECT 代理地址 = `host:agent_port`，其中 `host` 由服务端 `lanIP()` 解析、**端口由租约分配**（agent 绑定 `0.0.0.0`，全接口可达）。

`lanIP()` 解析优先级：

1. **`GT_LAN_IP` 显式配置** —— 宿主机在手机所在 LAN 内的可达 IPv4（如 `192.168.1.10`），最可信，**容器 / Hyper-V 部署强烈建议配置**。
2. 未配置走启发式（UDP 拨号出口 IP → 过滤 docker/WSL 虚拟网段后的首个私有 IPv4）——同 LAN 裸机可用，容器内会拿到 `172.x` 不可达网段。

配置位置：`.env` 里设 `GT_LAN_IP`，`docker-compose.yml` 的 `mcp` 服务已透传（默认空 = 走启发式）。注意它和 `GT_PUBLIC_HOST` 是两套不同场景——`GT_PUBLIC_HOST` 是远端探针跨 NAT 回连的公网/映射地址，`GT_LAN_IP` 是手机同 LAN 连代理的地址，一般填宿主机内网 IP 即可。

> 端口侧提醒：gt-singbox-agent 的 CONNECT 端口段已映射——`docker-compose.yml` 的 `pipeline` 服务新增 `- "${GT_PROXY_PORTS:-12100-12199}:12100-12199"`（默认 `12100-12199`，可用 `.env` 的 `GT_PROXY_PORTS` 覆盖）；手机从宿主机 LAN IP:该端口即可连入。若改用 host 网络则无需此映射。

> 关键变更：下载探针时**不再需要指定抓包端口与解码插件**——这些参数不在下载/接入阶段确定，而是探针在线后由「开始抓包」在 Web 上下发。因此回连地址是唯一必须在下载阶段确保正确的项，集中由 `GT_PUBLIC_*` 解决。

## 3. 启动与验证

```bash
# 默认完全重建：每次都从源码重新构建镜像（不复用旧镜像、不走层缓存）
docker compose up -d
# 连容器一起强制重建（镜像 ID 恰好没变时也要重来）
docker compose up -d --force-recreate
# 本机反复迭代嫌慢：临时走 BuildKit 层缓存
GT_BUILD_NO_CACHE=false docker compose up -d

docker compose ps          # pipeline、mcp 两个服务应为 running
docker compose logs pipeline | tail
```

> 构建策略说明：`docker-compose.yml` 给两个服务都加了 `pull_policy: build`，构建段加了
> `no_cache: true` / `pull: true`，所以 `up` 本身就会重建——不再需要记 `--build`，
> 也不会出现"改了源码却还在跑旧镜像"。要退回缓存构建见上（或 `.env.example` 的 `GT_BUILD_*`）。

验证（假设宿主机 IP 为 `10.0.0.5`）：

```bash
# 无 token → 401 unauthorized（鉴权已生效）
curl -s -o /dev/null -w '%{http_code}\n' http://10.0.0.5:8781/mcp
# 正确 token → 非 401（405/400 等取决于 HTTP 方法，不再是 unauthorized）
curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer gt_tok_bbb' http://10.0.0.5:8781/mcp
```

HTTP 路由：`/sse`、`/message`、`/mcp`、`/events/plugins` 均在鉴权链内（Bearer 校验，未带 token 返回 401）；`/singbox/profile` 是唯一豁免端点（手机客户端无法自定义请求头，且只输出代理地址信息）。

**owner 隔离**：每个 token 对应一个 owner，会话与插件按 owner 作用域——bob 看不到/管不了 alice 的会话与插件；带 `:admin` 的 owner 可以跨 owner 操作。MCP 工具（如 `start_capture`）的调用身份取自 HTTP 请求的 `Authorization: Bearer`，无需额外配置。

### 2.1 项目（Project）组织单元

「项目 Project」是一等组织单元，在 owner 之上提供一层**团队级归组**：项目持有 Game / 成员（Members）/ 解码插件（Decoder Plugins）/ 规则（Rules）/ 会话（Sessions），会话可归属到某个项目，避免多用户各自裸奔一堆 `session_xxx`。

项目相关接口：

| MCP 工具 | 作用 |
|---|---|
| `create_project` / `list_projects` / `get_project` / `update_project` / `delete_project` | 项目 CRUD，跨 owner 可见（见下） |
| `add_project_member` / `remove_project_member` | 项目成员增删；角色仅 `admin`/`member` 两档，不做复杂 RBAC |
| `transfer_project_owner` | 转移项目 Owner（独立的敏感安全操作；新 Owner 必须已是项目内成员） |
| `set_project_plugins` / `set_project_rules` | 项目持有的插件/规则条目（关联数据，非独立管理后台） |
| `move_session_to_project` | 把既有会话移入项目（或空串解除）；`set_session_project` 为其兼容别名 |

角色与权限（2026-09-05 钉死，角色层级：global admin > Project Owner > Project Admin > Project Member）：

| 动作 | Owner | Admin | Member |
|---|:--:|:--:|:--:|
| 查看项目 / 项目内会话 / 开始抓包 | ✓ | ✓ | ✓ |
| 管理成员 / 插件 / 规则 | ✓ | ✓ | ✗ |
| 修改 / 删除项目 | ✓ | ✗ | ✗ |
| 转移 Owner | ✓ | ✗ | ✗ |

- **Owner**：`projects.owner` 字段（创建者即首任 Owner）。Owner 不在成员表内；`created_by` 仅为审计字段。
- **可见性**：项目对 Owner、成员、全局 `:admin` 可见；**项目是协作边界**——成员可见项目内全部会话（含他人创建的），个人会话（未归属项目）仅创建者可见。
- **会话移动**：`move_session_to_project` 需要调用者对源会话有管理权、对目标项目有成员身份，且租户一致；不是任意可调的裸更新。
- **一手体验**：Web 首页「我的项目」展示项目在线/离线状态与最近会话；从项目发起抓包自动携带 `project_id`，会话持久化到 `sessions.project_id`。

```bash
# 通过 MCP 创建项目并加成员
PROJECT_ID=$(mcp call create_project '{"name":"王者荣耀测试服","game":"王者荣耀"}')
mcp call add_project_member "{\"project_id\":\"$PROJECT_ID\",\"user\":\"bob\",\"role\":\"member\"}"
mcp call start_capture '{"source":"agent","project_id":"'$PROJECT_ID'"}'   # 自动归属
# 校验归属：select session_id, project_id from sessions; （example）
```

**断线重连**：gt-agent 推流断线后按指数退避自动重连；插件进程崩溃由 gt-agent 按退避策略重启。服务器重启后 `restart: unless-stopped` 会自动拉起容器。

**监听地址回写**：各 gRPC/HTTP 服务实际监听地址会回写到 `<workdir>/addr.<name>.json`（如 `addr.mcp.json`、`addr.control.json`），方便多实例/动态端口场景读取。

### 启动排障

| 现象 | 原因 | 处理 |
|---|---|---|
| 构建卡在 `go mod download`，报 `proxy.golang.org ... connection refused` | builder 镜像默认 `GOPROXY` 是 proxy.golang.org，国内网络不可达 | 镜像已默认改用 `goproxy.cn`；如仍失败显式换源：`docker compose build --build-arg GOPROXY=https://goproxy.cn,direct` |
| `mcp` 容器反复重启，日志 `flag provided but not defined: -spawn-agent` | compose 只覆盖了 `entrypoint` 未写 `command`，镜像 `CMD ["-spawn-agent=false"]` 被追加给了 `gt-mcp`（该 flag 属于 pipeline） | mcp 服务必须显式写 `command`（见 `docker-compose.yml`），不要留空 |
| `mcp` 报 `open control store: unable to open database file (14)`，路径是 `/control.sqlite` | 工作目录没落到 `/data`，锚到了容器根目录，而进程以非 root 的 `gametrace` 用户运行 | 确认 `GT_HOME=/data` 已传入且镜像 `WORKDIR /data` 生效；数据目录应是 `/data/control.sqlite` |

排查第一步建议先分清是**构建失败**还是**容器 restart loop**：前者看 build 输出，后者直接
`docker compose logs <服务名>`；`docker inspect <容器> --format '{{.Config.Cmd}}'` 能确认
entrypoint 被覆盖后镜像 CMD 是否被意外继承。

## 4. 日志（可选）

两个容器默认把日志同时写文件（`/data/logs/`，按大小轮转）和 stderr（docker logs）。容器内通常只留一路即可，用环境变量关闭另一路（T17）：

| 环境变量 | 效果 |
|---|---|
| `GT_LOG_FILE_DISABLED=1` | 不落盘，只写 stderr（docker logs 可见） |
| `GT_LOG_STDERR_DISABLED=1` | 只写文件，不写 stderr |

在 `docker-compose.yml` 对应服务的 `environment:` 段加一行即可，如：

```yaml
    environment:
      GT_LOG_FILE_DISABLED: "1"
```

## 5. 成员接入

把服务器地址发给成员，成员按 [成员上手指南](member-onboarding.md) 操作。**推荐走前端「接入设备」生成启动码**，在目标机执行一条命令即自动领配置回连，无需手填 token / server / session：

```bash
# 启动码接入（推荐，免参数）：<code> 来自前端「我的接入」面板
irm "http://10.0.0.5:8781/setup.ps1?code=GT-XXXX-XXXX&platform=windows/amd64" | iex   # Windows
curl -fsSL "http://10.0.0.5:8781/setup.sh?code=GT-XXXX-XXXX&platform=linux/amd64" | bash # Linux/macOS
```

也可下载预置探针 zip 自行解压运行（同样免参数，回连地址已烧进 `config.embedded.json`）。registry 用映射后的宿主端口 `19091`，ingest 自动取 `19092`。

> 抓包端口与解码插件**不在接入时决定**——探针在线后在「开始抓包」里选这台机器并指定，由服务端下发。改端口/换解析器都不必重下探针。

旧版手动命令（需 owner 先 `start_capture(source="agent")` 拿到 session_id，已不推荐）：

```bash
gt-agent --token gt_tok_bbb --server 10.0.0.5:19091 --session <session_id> --iface 以太网 --filter "port 8984"
```

## 6. 验收清单（新机器演练）

- [ ] 全新 Linux 机器 `docker compose up -d` 一次成功（默认完全重建，需 GitHub 可达，见临时依赖）；
- [ ] 正确 token 的 MCP 请求 200；伪造 token 被拒绝（401/403）；
- [ ] bob 的 MCP 客户端看不到 alice 的会话与插件（owner 隔离）；admin 可以；
- [ ] 成员 `gt-agent` 推流后，owner 侧 `get_session_status` 的 `packets_in` 增长；
- [ ] 拔掉成员网线/杀掉 agent 进程再恢复，推流自动重连继续入库；
- [ ] 服务器重启后容器自愈（`restart: unless-stopped`），历史会话仍可查询。

## 7. 非 Docker 部署（简述）

裸机/裸容器自建时直接跑二进制即可，注意：

- 服务端实时网卡抓包源需要 libpcap（cgo，`-tags pcap` 构建）；
- 两个进程共用同一 `GT_HOME` 工作目录（与 compose 的 /data 卷等价）；
- `gt-mcp` 用 `GT_CONTROL_ADDR` 指向 pipeline 的 9888；
- 其余环境变量与上文一致（见 `pkg/config/app.go`）。

更多配置细节：`examples/gametrace.yaml`（统一配置文件示例）、`.env.example`。
