# 成员上手指南

一页速览：如何把本机接进团队 GameTrace。接入方式只有一种：**下载 gt-agent 探针**，解压运行即自动回连（无需手填 token / 回连地址 / 会话）。服务器侧部署由管理员完成（见 [团队部署指南](team-deployment.md)）。

## 1. 下载探针接入（唯一接入方式）

在网页顶部工具栏点「接入设备」（下载图标）→ 选**目标操作系统**（要在哪台电脑上抓包就选哪台）→「下载探针」，得到 `gt-agent-<os>-<arch>.zip`：

| 平台 | 操作 |
|---|---|
| **Windows** | 解压 zip，双击运行 `gt-agent.exe`（需先装 [Npcap](https://npcap.com/)，勾选 "WinPcap API-compatible Mode"） |
| **Linux / macOS** | 解压 zip，终端运行 `./gt-agent`（Linux 需 `sudo apt install libpcap-dev` 或发行版等价包） |

zip 已内置回连地址与凭证（`config.embedded.json` 与探针在同一目录即可），探针启动后自动注册到团队，出现在网页「我的设备」。抓包前本机需装好抓包驱动（上表）。

> 抓包端口与解码插件**不在接入时决定**——探针在线后，在「开始抓包」里选这台机器并指定端口与解析器，由服务端下发；改端口/换插件都不必重下探针。

## 2. 只托管本机插件（不抓包）

把编译好的插件二进制放进 gt-agent 同目录的 `plugins/` 下，然后：

```bash
gt-agent --token gt_tok_xxxx --server <服务器IP>:9091
```

gt-agent 会自动发现 `plugins/` 下的插件进程并拉起，以**隧道模式**注册到服务器。插件（官方脚手架 `create_plugin` 生成的模板原生支持）经 gRPC 连到 `:9091`，崩溃会被自动按退避重启。此后服务器侧 `list_registered_plugins` 就能看到你的插件。

## 3. 手动抓包（高级）

按需手动开会话并推流：

```bash
start_capture(source="agent", plugin=<可选>)   # 记下 session_id
gt-agent --token gt_tok_xxxx --server <服务器IP>:9091 \
  --session <session_id> --iface <网卡> --filter "port 8984"
```

- `--iface`：抓包网卡名（Windows 如 `以太网`，Linux 如 `eth0`）；
- `--filter`：BPF 过滤表达式（建议加，控制上行带宽）；
- `--session` 留空 = 只托管插件、不抓包。

开发者想写/构建/验证插件，可在网页「更多 → 开发者工具」中完成（脚手架、编译、归因）。

## 4. 常见问题

| 现象 | 排查 |
|---|---|
| 探针没出现在「我的设备」 | 确认 zip 里的 `config.embedded.json` 与探针同一目录；确认目标电脑能访问回连地址（见 2.3 节） |
| 请求返回 401 unauthorized | 高级手填场景：token 错了或没带（`Authorization: Bearer gt_tok_xxxx`） |
| 能连上但看不到数据 | 确认抓包端口来源的会话与「开始抓包」时一致；确认防火墙放行 9091/9092 |
| 抓包启动失败 "capture unavailable" | 未装 Npcap/libpcap，或二进制没用 `-tags pcap` 构建 |
| 插件没注册上 | 插件需支持隧道模式（官方脚手架模板原生支持）；看 `plugins/` 下 agent 的插件日志 |
| 想换身份 | 换 token 即换 owner；别人的会话/插件你看不到也管不了（admin 除外） |