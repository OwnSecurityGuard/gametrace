// 协议事件展示的公共工具与组件。
//
// 从 event-table.tsx 抽出，供事件表格、会话级关系树、状态变更等视图复用，
// 避免在多处复制 extractMeta / MessageCell / JSON 高亮等逻辑。
import { useMemo } from "react";
import { ArrowRight, ArrowLeft } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { unpackJsonStrings } from "@/lib/utils";

// ─── 元数据提取 ─────────────────────────────────────────────

export interface EventMeta {
  /** "client_to_server" | "server_to_client" | ""（端口推断的传输层事实）。 */
  direction: string;
  /** e.g. "on_client_delta"。 */
  msgName: string;
  /** SDK annotate 规则标签：request | response | notification | error。 */
  semantic: string[];
  /** Blocks 数量（如有）。 */
  blocks?: number;
}

/** 安全提取 data 里的 _meta/Blocks 字段，缺失时返回空默认值。 */
export function extractMeta(data: Record<string, unknown>): EventMeta {
  const meta = data._meta as Record<string, unknown> | undefined;
  if (!meta || typeof meta !== "object") {
    return { direction: "", msgName: "", semantic: [] };
  }
  const direction = String(meta.direction ?? "");
  const msgName = String(meta.msg_name ?? "");
  const semantic = Array.isArray(meta.semantic)
    ? meta.semantic.map((s) => String(s)).filter(Boolean)
    : [];

  let blocks: number | undefined;
  const blocksArr = data.Blocks as Array<Record<string, unknown>> | undefined;
  if (Array.isArray(blocksArr)) {
    blocks = blocksArr.length;
  }
  if (blocks === 0 && typeof data.count === "number") {
    blocks = data.count;
  }

  return { direction, msgName, semantic, blocks };
}

// ─── 格式化工具 ─────────────────────────────────────────────

export function formatTimestamp(isoStr: string): string {
  try {
    return new Date(isoStr).toLocaleString("zh-CN", {
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  } catch {
    return isoStr;
  }
}

/** 字节 → 可读大小 */
export function formatSize(bytes: number): string {
  if (bytes <= 0) return "-";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// ─── 方向箭头 Badge ──────────────────────────────────────────

export function DirectionBadge({ direction }: { direction: string }) {
  switch (direction) {
    case "client_to_server":
      return (
        <span className="inline-flex items-center gap-0.5 rounded-full bg-blue-50 text-blue-700 px-2 py-0.5 text-xs font-medium dark:bg-blue-950 dark:text-blue-300">
          <ArrowRight className="h-3 w-3" />
          C→S
        </span>
      );
    case "server_to_client":
      return (
        <span className="inline-flex items-center gap-0.5 rounded-full bg-emerald-50 text-emerald-700 px-2 py-0.5 text-xs font-medium dark:bg-emerald-950 dark:text-emerald-300">
          <ArrowLeft className="h-3 w-3" />
          S→C
        </span>
      );
    default:
      return (
        <span className="inline-flex items-center rounded-full bg-muted text-muted-foreground px-2 py-0.5 text-xs">
          ?
        </span>
      );
  }
}

// ─── 语义标签（annotate 规则产出） ───────────────────────────

const SEMANTIC_STYLES: Record<string, string> = {
  request: "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-300",
  response: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300",
  notification: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300",
  error: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-300",
};

export function SemanticBadge({ label }: { label: string }) {
  return (
    <span
      className={`shrink-0 rounded px-1 py-px text-[10px] font-medium ${
        SEMANTIC_STYLES[label] ?? "bg-muted text-muted-foreground"
      }`}
    >
      {label}
    </span>
  );
}

// ─── 消息名 + SDK 语义标签 ───────────────────────────────────
// 协议语义只显示 SDK 规则产出（annotate/pair）；插件私有字段（如
// http-decoder 的 _meta.is_push）不上 UI——要展示就改成声明 annotate 规则。

export function MessageCell({ msgName, semantic }: { msgName: string; semantic: string[] }) {
  return (
    <div className="flex items-center gap-1.5 min-w-0">
      <span className="font-mono text-xs font-semibold truncate" title={msgName}>
        {msgName || "(unknown)"}
      </span>
      {semantic.map((s) => (
        <SemanticBadge key={s} label={s} />
      ))}
    </div>
  );
}

// ─── JSON 语法高亮 ──────────────────────────────────────────

export interface JsonToken {
  type: "key" | "string" | "number" | "boolean" | "null" | "punct";
  text: string;
}

export function tokenizeJson(text: string): JsonToken[] {
  const tokens: JsonToken[] = [];
  let i = 0;
  let inString = false;
  let stringChar = "";

  while (i < text.length) {
    const ch = text[i]!;
    if ((ch === '"' || ch === "'") && !inString) {
      inString = true;
      stringChar = ch;
      let end = i + 1;
      while (end < text.length && text[end] !== stringChar) {
        if (text[end] === "\\") end++;
        end++;
      }
      const str = text.slice(i, end + 1);
      const afterStr = text.slice(end + 1).trimStart();
      tokens.push({ type: afterStr.startsWith(":") ? "key" : "string", text: str });
      i = end + 1;
      continue;
    }
    if (inString) { tokens.push({ type: "string", text: ch }); i++; continue; }
    if (ch === "-" || (ch >= "0" && ch <= "9")) {
      let end = i + 1;
      while (end < text.length && /[\d.eE+\-]/.test(text[end]!)) end++;
      tokens.push({ type: "number", text: text.slice(i, end) }); i = end; continue;
    }
    if (text.startsWith("true", i)) { tokens.push({ type: "boolean", text: "true" }); i += 4; continue; }
    if (text.startsWith("false", i)) { tokens.push({ type: "boolean", text: "false" }); i += 5; continue; }
    if (text.startsWith("null", i)) { tokens.push({ type: "null", text: "null" }); i += 4; continue; }
    if (":,{}[]".includes(ch)) { tokens.push({ type: "punct", text: ch }); }
    else if (ch !== " " && ch !== "\n" && ch !== "\r" && ch !== "\t") { tokens.push({ type: "punct", text: ch }); }
    i++;
  }
  return tokens;
}

export function HighlightedText({ text }: { text: string }) {
  const tokens = useMemo(() => tokenizeJson(text), [text]);
  return (
    <>
      {tokens.map((token, i) => {
        switch (token.type) {
          case "key":
            return <span key={i} className="gt-json-key">{token.text}</span>;
          case "string":
            return <span key={i} className="gt-json-string">{token.text}</span>;
          case "number":
            return <span key={i} className="gt-json-number">{token.text}</span>;
          case "boolean":
            return <span key={i} className="gt-json-boolean">{token.text}</span>;
          case "null":
            return <span key={i} className="gt-json-null">{token.text}</span>;
          default:
            return <span key={i} className="gt-json-punct">{token.text}</span>;
        }
      })}
    </>
  );
}

export function HighlightedJson({ data }: { data: unknown }) {
  const formatted = useMemo(() => JSON.stringify(unpackJsonStrings(data), null, 2), [data]);
  return (
    <pre className="gt-json-pre max-h-[400px] overflow-auto">
      <HighlightedText text={formatted} />
    </pre>
  );
}

/** op 徽标：set 绿 / delete 红 / merge 琥珀（状态变更视图复用）。 */
export function OpBadge({ op }: { op: string }) {
  const style =
    op === "set"
      ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300"
      : op === "delete"
        ? "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-300"
        : op === "merge"
          ? "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300"
          : "bg-muted text-muted-foreground";
  return <Badge variant="outline" className={`font-mono text-[10px] ${style}`}>{op || "-"}</Badge>;
}