import { useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Bot, Check, Copy, Plug, Server, KeyRound, AlertTriangle, ShieldAlert } from "lucide-react";
import { useAgentDownloadOptions } from "@/hooks/use-mcp";
import { getToken } from "@/lib/auth";
import { toast } from "@/components/ui/toast";

interface McpAccessDialogProps {
  open: boolean;
  onClose: () => void;
}

/**
 * MCP 接入（顶栏设置入口）：
 * 展示本平台 MCP 服务器的连接信息（SSE 端点 / 消息端点 / 认证方式），
 * 并生成一段可直接复制给 AI（Claude / ChatGPT / Cursor …）的提示词，
 * 让该 AI 作为 MCP 客户端连上此平台并调用抓包/协议解析等工具。
 *
 * 地址复用后端既有 get_agent_download_options（其 host 尊重 GT_PUBLIC_HOST，
 * 是部署方通告的对外地址）；MCP 服务端口与当前 Web 是同一个 HTTP/SSE 服务，
 * 故取浏览器当前端口。
 */
export function McpAccessDialog({ open, onClose }: McpAccessDialogProps) {
  const { data, isLoading } = useAgentDownloadOptions();
  const [copied, setCopied] = useState<"sse" | "message" | "token" | "prompt" | null>(null);

  const token = typeof window !== "undefined" ? getToken() : null;
  const [showToken, setShowToken] = useState(false);

  // 计算对外可访问的 MCP 地址：host 用后端通告值（GT_PUBLIC_HOST 优先），
  // 端口用浏览器当前访问端口（MCP/SSE 与 Web 同属一个 HTTP 服务）。
  const conn = useMemo(() => {
    const scheme = typeof window !== "undefined" ? window.location.protocol : "http:";
    const browserHost = typeof window !== "undefined" ? window.location.hostname : "";
    const port = typeof window !== "undefined" ? window.location.port || "" : "";
    const host = data?.host ? data.host.replace(/^https?:\/\//, "") : browserHost;
    return { scheme, host, port };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data?.host]);

  const sseUrl = useMemo(() => {
    const { scheme, host, port } = conn;
    return `${scheme}//${host}${port ? `:${port}` : ""}/sse`;
  }, [conn]);

  const messageUrl = useMemo(() => {
    const { scheme, host, port } = conn;
    return `${scheme}//${host}${port ? `:${port}` : ""}/message`;
  }, [conn]);

  const isLoopback = useMemo(
    () => !conn.host || ["localhost", "127.0.0.1", "::1", "[::1]"].includes(conn.host),
    [conn.host],
  );

  const prompt = useMemo(
    () => `# GameTrace MCP 接入说明

你是一个需要使用 GameTrace 平台能力的 coding agent。

当前环境可能尚未接入 GameTrace MCP Server。

你的目标：

1. 完成 MCP Server 接入。
2. 获取 GameTrace 提供的 MCP tools。
3. 使用这些 tools 完成游戏抓包分析、协议分析、会话查询、插件管理等任务。

---

# MCP Server 信息

GameTrace MCP Server:

* Transport:
  MCP SSE over HTTP

* SSE Endpoint:

\`\`\`
${sseUrl}
\`\`\`

* Message Endpoint:

\`\`\`
${messageUrl}
\`\`\`

* Authentication:

HTTP Header:

\`\`\`
Authorization: Bearer <token>
\`\`\`

Token:

\`\`\`
${token ?? "（未配置，匿名访问，无需令牌）"}
\`\`\`

---

# 接入要求

如果你的运行环境支持 MCP 配置：

请优先通过 MCP 配置方式接入。

例如：

* mcp.json
* MCP client configuration
* IDE MCP settings

不要自行实现 MCP 协议客户端，除非当前环境没有 MCP 配置能力。

---

# 接入验证

完成配置后：

1. 建立 MCP connection。
2. 完成 MCP initialize handshake。
3. 获取 tools/list。
4. 调用 get_capabilities 获取 Skill Catalog（平台 skills/ 目录的技能目录：名称、用途与触发场景；每个技能同时是可读的 MCP resource：gametrace://skills/<name>）。
5. 确认 GameTrace tools 与技能已加载。

成功后输出：

* MCP connection status
* available tools list
* 每个 tool 的用途说明
* Skill Catalog（每个技能的名称与触发场景）

---

# 使用规则

连接成功后：

优先使用 GameTrace MCP tools 完成任务。

不要：

* 手写 HTTP 请求模拟 MCP 调用
* 自己实现 MCP SSE client
* 绕过 MCP 直接访问数据库
* 根据猜测生成协议数据

---

# GameTrace 能力范围

GameTrace 提供：

## 抓包会话分析

用于：

* 开始 / 停止抓包（start_capture / stop_capture）
* 查看当前抓包 session（get_session_status / list_live_sessions）
* 分析历史会话（list_all_sessions）
* 查看连接和流（list_decoded_data / get_protocol_catalog）

## 协议分析

用于：

* 查看客户端发送协议
* 查看服务端响应
* 分析 request/response 关系
* 查看 decoded payload

推荐流程：

\`\`\`
get_protocol_catalog
        ↓
list_decoded_data
        ↓
list_state_changes
\`\`\`

## 插件管理

用于：

* 开发新协议插件（get_plugin_dev_guide → create_plugin → build_plugin）
* 验证与激活插件（activate_plugin → verify_plugin）
* 查看插件状态与解码归因（status_plugin / explain_plugin）
* 插件注册表查询（list_plugins / get_plugin_manifest）

## Coding 任务

如果用户要求：

* 补充压测脚本
* 编写协议调用代码
* 分析玩法流程

执行：

1. 查看已有代码结构。
2. 使用 GameTrace 获取真实协议数据。
3. 区分业务协议和背景协议。
4. 基于已有代码风格修改。

不要直接复制抓包数据生成代码。

---

# 注意

GameTrace 是持续运行的抓包平台。

通常：

* 用户已经完成游戏操作。
* 最新 session 通常是目标分析对象。

除非用户明确指定，否则不要要求用户重新开始抓包流程。`,
    [sseUrl, messageUrl, token],
  );

  async function copy(field: "sse" | "message" | "token" | "prompt", text: string, empty?: boolean) {
    if (empty) {
      toast.info("暂无可复制内容", "请先保存访问令牌");
      return;
    }
    try {
      await navigator.clipboard.writeText(text);
      setCopied(field);
      toast.success("已复制");
      setTimeout(() => setCopied(null), 1500);
    } catch {
      toast.error("复制失败", "浏览器拒绝访问剪贴板");
    }
  }

  const KeyValue = ({
    label,
    value,
    field,
  }: {
    label: string;
    value: string;
    field: "sse" | "message";
  }) => (
    <div className="flex items-center gap-2">
      <span className="w-16 shrink-0 text-xs text-muted-foreground">{label}</span>
      <code className="min-w-0 flex-1 truncate rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-foreground">
        {value}
      </code>
      <Button variant="outline" size="sm" className="h-7 shrink-0" onClick={() => copy(field, value, !value)}>
        {copied === field ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        复制
      </Button>
    </div>
  );

  return (
    <Dialog
      open={open}
      onClose={onClose}
      icon={<Plug className="h-5 w-5" />}
      title="MCP 接入"
      description="让别人工智能以 MCP 客户端身份连上本平台，调用抓包 / 协议解析等功能。复制下方提示词到 AI（Claude / ChatGPT / Cursor …）即可。"
      footer={
        <Button variant="outline" onClick={onClose}>
          关闭
        </Button>
      }
      className="max-w-lg"
    >
      <div className="space-y-4">
        {isLoading ? (
          <p className="py-2 text-xs text-muted-foreground">正在读取服务端地址…</p>
        ) : (
          <>
            {/* 连接信息 */}
            <div>
              <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
                <Server className="h-3.5 w-3.5 text-muted-foreground" />
                连接信息
              </p>
              <div className="space-y-1.5">
                <KeyValue label="SSE 端点" value={sseUrl} field="sse" />
                <KeyValue label="消息端点" value={messageUrl} field="message" />
              </div>
            </div>

            {/* 访问令牌 */}
            <div>
              <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
                <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
                访问令牌
              </p>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 truncate rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-foreground">
                  {token ? (showToken ? token : "••••••••••••") : "（匿名模式，无需令牌）"}
                </code>
                {token && (
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-7 shrink-0"
                    onClick={() => setShowToken((v) => !v)}
                    title={showToken ? "隐藏令牌" : "查看令牌"}
                    aria-label={showToken ? "隐藏令牌" : "查看令牌"}
                  >
                    {showToken ? <ShieldAlert className="h-3.5 w-3.5" /> : <KeyRound className="h-3.5 w-3.5" />}
                    显示
                  </Button>
                )}
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 shrink-0"
                  onClick={() => copy("token", token ?? "", !token)}
                >
                  {copied === "token" ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
                  复制
                </Button>
              </div>
              {token && (
                <p className="mt-1 flex gap-1.5 text-[11px] text-amber-600 dark:text-amber-400">
                  <ShieldAlert className="mt-0.5 h-3 w-3 shrink-0" />
                  令牌等同于平台访问凭证，请勿把提示词发给不受信任的一方。
                </p>
              )}
            </div>

            {/* 对外地址可信度提示 */}
            {data && data.addr_source !== "env" && (
              <p className="flex gap-1.5 rounded-md border border-border bg-muted/40 px-3 py-2 text-[11px] text-amber-600 dark:text-amber-400">
                <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                服务端未配置 GT_PUBLIC_HOST，地址是按当前访问方式推测的。跨网络/公网给 AI 用时，请在服务端设置
                GT_PUBLIC_HOST（必要时配 GT_PUBLIC_REGISTRY_PORT / GT_PUBLIC_INGEST_PORT），否则外部 AI 可能连不上。
              </p>
            )}
            {isLoopback && (
              <p className="flex gap-1.5 rounded-md border border-border bg-muted/40 px-3 py-2 text-[11px] text-amber-600 dark:text-amber-400">
                <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                当前地址是回环地址（localhost），仅本机可用。远程 AI 需要可被其访问的公网地址——请在服务端配置
                GT_PUBLIC_HOST。
              </p>
            )}

            {/* 提示词 */}
            <div>
              <div className="mb-2 flex items-center gap-2">
                <p className="flex items-center gap-1.5 text-sm font-medium">
                  <Bot className="h-3.5 w-3.5 text-muted-foreground" />
                  给 AI 的提示词
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  className="ml-auto h-7"
                  onClick={() => copy("prompt", prompt)}
                >
                  {copied === "prompt" ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
                  复制提示词
                </Button>
              </div>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-background p-3 font-mono text-[11.5px] leading-relaxed text-foreground gt-scroll">
                {prompt}
              </pre>
            </div>
          </>
        )}
      </div>
    </Dialog>
  );
}