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

  const tokenLine = token ? `> 访问令牌：\`${token}\`` : "> 访问令牌：未配置（当前为匿名访问，无需令牌）";

  const prompt = useMemo(
    () => `请作为 MCP 客户端，连接到我团队的 GameTrace 抓包平台 MCP 服务器。

【接入信息】
- 传输方式：MCP SSE over HTTP（不是 stdio，需要 HTTP 端点）
- SSE 端点：${sseUrl}
- 消息端点：${messageUrl}
- 认证方式：HTTP 请求头 \`Authorization: Bearer <token>\`
${tokenLine}

【要求】
1. 以上面的 SSE 端点为入口，按 MCP 协议完成握手（Initialize → tools/list）。
2. 如果你的环境是用配置文件（如 mcp.json）接入 MCP 的，请给我一份包含上述 SSE 端点、消息端点与令牌的配置。
3. 连接成功后，把可用的工具清单及其用途列给我；之后按需调用这些工具，帮我完成抓包、协议解析、连接/会话数据查看、探针管理等任务。`,
    [sseUrl, messageUrl, tokenLine],
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