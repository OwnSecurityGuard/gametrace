// 协议事件展示的公共工具与组件。
//
// 从 event-table.tsx 抽出，供事件表格、会话级关系树、状态变更等视图复用，
// 避免在多处复制 extractMeta / MessageCell / JSON 高亮等逻辑。
import { useMemo, useState } from "react";
import { ArrowRight, ArrowLeft, ChevronRight } from "lucide-react";
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

/** 安全提取 _meta/Blocks 字段，缺失时返回空默认值。
 *  v0.8.0 起 MCP 返回独立 meta 字段（优先读取）；旧数据兜底读 data._meta。 */
export function extractMeta(
  data: Record<string, unknown>,
  standaloneMeta?: Record<string, unknown>,
): EventMeta {
  const meta =
    standaloneMeta && typeof standaloneMeta === "object"
      ? standaloneMeta
      : ((data._meta as Record<string, unknown> | undefined) ?? {});
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

// ─── 信息分层：业务数据 / Metadata / 分析 ──────────────────────
// 产品心智：Payload ≠ Metadata ≠ Analysis。
// 前端把事件 data 拆成三份，避免把平台推导出的分析字段混进业务数据展示。

/** payload 顶层被归为「GameTrace 分析」的键（其余非 _meta 字段归「业务数据」）。 */
export const ANALYSIS_KEYS = new Set<string>([
  "_state_changes",
  "entity",
  "entity_type",
  "entity_id",
  "change_count",
  "correlation_id",
  "flow_id",
  "causation_id",
  "parent_id",
  "relation",
]);

/** 与 _meta 内容重复的顶层冗余元信息键：解码器会同时写 _meta 与顶层，归 Metadata 而非业务数据。 */
export const META_REDUNDANT_KEYS = new Set<string>(["msg_name", "role", "is_push"]);

export interface PayloadClasses {
  /** _meta（direction/msg_name/semantic/role/is_push…）+ 顶层冗余元信息键——Metadata，默认不直接展示。 */
  meta: Record<string, unknown>;
  /** 业务字段：如 playerId/reason、http 的 method/path/status。 */
  business: Record<string, unknown>;
  /** 分析字段：实体、状态变更、correlation、relation…——进「GameTrace 分析」区。 */
  analysis: Record<string, unknown>;
}

/** 把事件 data 按 key 拆成 meta / business / analysis 三份。 */
export function classifyPayload(data: Record<string, unknown>): PayloadClasses {
  const meta: Record<string, unknown> = {};
  const business: Record<string, unknown> = {};
  const analysis: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(data)) {
    if (k === "_meta" || META_REDUNDANT_KEYS.has(k)) meta[k] = v;
    else if (ANALYSIS_KEYS.has(k)) analysis[k] = v;
    else business[k] = v;
  }
  return { meta, business, analysis };
}

/** v0.8.0 起 MCP 的 data 已是纯业务 payload；旧数据（无独立 meta/analysis 字段）兜底拆分。 */
export function businessPayload(ev: {
  data: Record<string, unknown>;
  meta?: Record<string, unknown>;
}): Record<string, unknown> {
  return ev.meta !== undefined ? ev.data : classifyPayload(ev.data).business;
}

/** v0.8.0 起 MCP 返回独立 analysis 字段；旧数据兜底从 data 分类。 */
export function analysisOf(ev: {
  data: Record<string, unknown>;
  analysis?: Record<string, unknown>;
}): Record<string, unknown> {
  return ev.analysis ?? classifyPayload(ev.data).analysis;
}

/** direction 枚举 → 人话方向文本（未知返回空串）。 */
export function directionText(direction: string): string {
  switch (direction) {
    case "client_to_server":
      return "客户端 → 服务端";
    case "server_to_client":
      return "服务端 → 客户端";
    default:
      return "";
  }
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

/**
 * 紧凑方向图标：只编码方向（形状 + 箭头指向），不抢语义 badge 的颜色。
 *
 * 表格里方向 pill（C→S 蓝 / S→C 绿）与语义 pill（request 蓝 / response 绿）撞色，
 * 两排同色小圆点分不清哪个是哪个——这是「看着费劲」的主要来源。
 * 这里改成中性灰底 + 只给箭头上色，颜色留给语义。
 */
export function DirectionIcon({ direction }: { direction: string }) {
  if (direction === "client_to_server") {
    return (
      <span
        className="shrink-0 rounded bg-muted px-1 py-px text-muted-foreground"
        title="客户端 → 服务端"
      >
        <ArrowRight className="h-3 w-3 text-blue-600 dark:text-blue-400" />
      </span>
    );
  }
  if (direction === "server_to_client") {
    return (
      <span
        className="shrink-0 rounded bg-muted px-1 py-px text-muted-foreground"
        title="服务端 → 客户端"
      >
        <ArrowLeft className="h-3 w-3 text-emerald-600 dark:text-emerald-400" />
      </span>
    );
  }
  return (
    <span className="shrink-0 rounded bg-muted px-1 py-px text-muted-foreground" title="方向未知">
      <ArrowRight className="h-3 w-3 text-muted-foreground/50" />
    </span>
  );
}

/** 方向文字 chip：箭头已表达方向，只标注缩写端点，不再写全两端中文。 */
export function DirectionChip({ direction }: { direction: string }) {
  const text = directionText(direction);
  if (!text) {
    return (
      <span className="shrink-0 rounded bg-muted px-1.5 py-px text-[10px] text-muted-foreground/60" title="方向未知">
        方向未知
      </span>
    );
  }
  const label = direction === "client_to_server" ? "C→S" : "S→C";
  const color =
    direction === "client_to_server"
      ? "text-blue-600 dark:text-blue-400"
      : "text-emerald-600 dark:text-emerald-400";
  return (
    <span
      className={`shrink-0 rounded bg-muted px-1.5 py-px font-mono text-[10px] font-semibold whitespace-nowrap ${color}`}
      title={text}
    >
      {label}
    </span>
  );
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

// ─── 结构化字段视图 ───────────────────────────────────────────
// 默认把业务字段按 key → value 逐行渲染（而非整段 JSON），嵌套对象/数组折叠成
// 摘要行可展开；深层不做全量树，展开后以单行 JSON 高亮兜底。

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === "object" && !Array.isArray(v);
}

function scalarText(v: unknown): string {
  if (v === null) return "null";
  if (typeof v === "string") return v;
  return String(v);
}

/** 嵌套值的折叠摘要：数组显示项数，对象显示前几个键名。 */
function nestedSummary(v: unknown): string {
  if (Array.isArray(v)) return `[${v.length} 项]`;
  if (isPlainObject(v)) {
    const keys = Object.keys(v);
    return keys.length > 0 ? `{ ${keys.slice(0, 3).join(", ")}${keys.length > 3 ? ", …" : ""} }` : "{}";
  }
  return scalarText(v);
}

function FieldValue({ value }: { value: unknown }) {
  const [open, setOpen] = useState(false);
  const nested = Array.isArray(value) || isPlainObject(value);

  if (!nested) {
    return <dd className="min-w-0 break-all text-foreground/90">{scalarText(value)}</dd>;
  }

  return (
    <dd className="min-w-0">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-1.5 text-left"
        aria-expanded={open}
      >
        <ChevronRight className={`h-3 w-3 shrink-0 text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`} />
        <span className="truncate text-muted-foreground">{nestedSummary(value)}</span>
      </button>
      {open && (
        <pre className="gt-json-pre mt-1 max-h-64 overflow-auto rounded border border-border/60 p-2">
          <HighlightedText text={JSON.stringify(unpackJsonStrings(value), null, 2)} />
        </pre>
      )}
    </dd>
  );
}

/** 结构化字段视图：逐行 key → value；嵌套值折叠为摘要行、可展开看完整 JSON。 */
export function StructuredFields({ obj }: { obj: Record<string, unknown> }) {
  const entries = useMemo(() => Object.entries(obj), [obj]);
  if (entries.length === 0) {
    return <p className="px-2 py-1.5 text-xs text-muted-foreground">（空）</p>;
  }
  return (
    <dl className="divide-y divide-border/60 rounded-md border border-border bg-background text-xs">
      {entries.map(([k, v]) => (
        <div key={k} className="flex items-start gap-2 px-2 py-1">
          <dt className="shrink-0 pt-px font-mono text-muted-foreground">{k}</dt>
          <FieldValue value={v} />
        </div>
      ))}
    </dl>
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