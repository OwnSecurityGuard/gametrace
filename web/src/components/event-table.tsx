import { useState, useEffect, useMemo, Fragment, memo } from "react";
import { useDecodedData } from "@/hooks/use-mcp";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Badge } from "@/components/ui/badge";
import {
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  Table2,
  SearchX,
  RotateCw,
  ArrowRight,
  ArrowLeft,
  Link2,
} from "lucide-react";
import type { DecodedEvent } from "@/types/event";
import type { CaptureContext } from "@/types/connection";
import { unpackJsonStrings } from "@/lib/utils";

interface EventTableProps {
  sessionId: string | null;
  filter: string;
  /** 供配对定位写回筛选表达式（App 状态，同步到 FilterBar）。 */
  onFilterChange?: (filter: string) => void;
}

const PAGE_SIZE = 20;

// ─── 元数据提取 ─────────────────────────────────────────────

interface EventMeta {
  direction: string;   // "client_to_server" | "server_to_client" | ""（端口推断的传输层事实）
  msgName: string;     // e.g. "on_client_delta"
  semantic: string[];  // SDK annotate 规则标签：request | response | notification | error
  blocks?: number;     // Blocks 数量（如有）
}

/** 安全提取 _meta 字段，缺失时返回空默认值 */
function extractMeta(data: Record<string, unknown>): EventMeta {
  const meta = data._meta as Record<string, unknown> | undefined;
  if (!meta || typeof meta !== "object") {
    return { direction: "", msgName: "", semantic: [] };
  }
  const direction = String(meta.direction ?? "");
  const msgName = String(meta.msg_name ?? "");
  const semantic = Array.isArray(meta.semantic)
    ? meta.semantic.map((s) => String(s)).filter(Boolean)
    : [];

  // 尝试提取 Blocks 数量（Godot 协议特有）
  let blocks: number | undefined;
  const blocksArr = data.Blocks as Array<Record<string, unknown>> | undefined;
  if (Array.isArray(blocksArr)) {
    blocks = blocksArr.length;
  }
  // 也尝试 count 字段
  if (blocks === 0 && typeof data.count === "number") {
    blocks = data.count;
  }

  return { direction, msgName, semantic, blocks };
}

// ─── 格式化工具 ─────────────────────────────────────────────

function formatTimestamp(isoStr: string): string {
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
function formatSize(bytes: number): string {
  if (bytes <= 0) return "-";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** 生成一行 payload 摘要文本 */
function summarizePayload(data: Record<string, unknown>, meta: EventMeta): string {
  const parts: string[] = [];

  // Blocks 数量
  if (meta.blocks != null) {
    parts.push(`${meta.blocks} block${meta.blocks > 1 ? "s" : ""}`);
  }

  // 提取顶层非 _meta 的标量字段作为补充信息
  for (const [k, v] of Object.entries(data)) {
    if (k.startsWith("_") || k === "Blocks") continue;
    if (typeof v === "string" && v.length < 40) {
      parts.push(`${k}: ${v}`);
    } else if (typeof v === "number") {
      parts.push(`${k}: ${v}`);
    } else if (typeof v === "boolean") {
      parts.push(`${k}: ${v}`);
    }
    // 超过 3 个字段就停止，保持摘要简洁
    if (parts.length >= 4) break;
  }

  return parts.length > 0 ? parts.join(" · ") : "(empty)";
}

// ─── 方向箭头 Badge ──────────────────────────────────────────

function DirectionBadge({ direction }: { direction: string }) {
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

function SemanticBadge({ label }: { label: string }) {
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
// ponytail: 协议语义只显示 SDK 规则产出（annotate/pair）；插件私有字段（如
// http-decoder 的 _meta.is_push）不上 UI——要展示就改成声明 annotate 规则。

function MessageCell({ msgName, semantic }: { msgName: string; semantic: string[] }) {
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

// ─── JSON 语法高亮（展开行复用） ──────────────────────────────

function HighlightedJson({ data }: { data: Record<string, unknown> }) {
  const formatted = useMemo(() => JSON.stringify(unpackJsonStrings(data), null, 2), [data]);
  return (
    <pre className="gt-json-pre max-h-[400px] overflow-auto">
      <HighlightedText text={formatted} />
    </pre>
  );
}

function HighlightedText({ text }: { text: string }) {
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

interface JsonToken {
  type: "key" | "string" | "number" | "boolean" | "null" | "punct";
  text: string;
}

function tokenizeJson(text: string): JsonToken[] {
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

// ─── 捕获上下文（Capture Context，代理抓包特有） ────────────────

/** 展示 Captured By / Connection / Stream / Source 归属徽标组。 */
function CaptureCell({ capture }: { capture: CaptureContext }) {
  return (
    <div className="flex flex-wrap items-center gap-1 min-w-0" title={`连接 ${capture.conn_id} · 流 ${capture.stream_id} · 来源 ${capture.source || ""}`}>
      <Badge variant="outline" className="text-[10px] font-normal whitespace-nowrap">
        {capture.captured_by || "Proxy"}
      </Badge>
      <Badge variant="secondary" className="font-mono text-[10px] whitespace-nowrap">
        C#{String(capture.conn_seq).padStart(3, "0")}
      </Badge>
      <Badge variant="secondary" className="font-mono text-[10px] whitespace-nowrap">
        S#{capture.stream_seq}
      </Badge>
    </div>
  );
}

// ─── 展开行：配对关系 + 完整 JSON ────────────────────────────

const COLSPAN = 6; // Timestamp | Dir | Msg | Capture | Summary | Size

/** 配对面板：展示 pair 语义规则配出的对侧消息（响应 causation_id → 请求），可跳转定位。 */
function PairPanel({
  event,
  partners,
  onJumpToPartner,
  onLocatePair,
}: {
  event: DecodedEvent;
  partners: DecodedEvent[];
  onJumpToPartner: (id: string) => void;
  onLocatePair: (event: DecodedEvent) => void;
}) {
  if (partners.length === 0 && !event.causation_id) return null;

  return (
    <div className="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-border bg-background px-3 py-2">
      <span className="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground">
        <Link2 className="h-3.5 w-3.5" />
        协议配对
      </span>
      {partners.length > 0 ? (
        partners.map((p) => {
          const pMeta = extractMeta(p.data);
          const isResponse = p.causation_id === event.id;
          return (
            <button
              key={p.id}
              type="button"
              onClick={() => onJumpToPartner(p.id)}
              className="inline-flex items-center gap-1.5 rounded-full border border-border bg-muted/40 px-2 py-0.5 text-xs hover:bg-muted transition-colors"
              title={`跳转到配对消息 ${pMeta.msgName || p.id}`}
            >
              <ArrowRight className="h-3 w-3 text-muted-foreground" />
              <span className="font-mono font-semibold">{pMeta.msgName || "(unknown)"}</span>
              <span className="text-muted-foreground">({isResponse ? "响应" : "请求"})</span>
              <span className="font-mono text-muted-foreground">{formatTimestamp(p.timestamp)}</span>
            </button>
          );
        })
      ) : (
        <span className="text-xs text-muted-foreground">
          配对消息不在当前页
          <button
            type="button"
            onClick={() => onLocatePair(event)}
            className="ml-2 inline-flex items-center rounded border border-border px-1.5 py-0.5 text-[11px] text-primary hover:bg-muted transition-colors"
          >
            筛选定位
          </button>
        </span>
      )}
    </div>
  );
}

function ExpandedRow({
  event,
  partners,
  onJumpToPartner,
  onLocatePair,
}: {
  event: DecodedEvent;
  partners: DecodedEvent[];
  onJumpToPartner: (id: string) => void;
  onLocatePair: (event: DecodedEvent) => void;
}) {
  return (
    <TableRow className="gt-fade-in">
      <TableCell colSpan={COLSPAN} className="bg-muted/30 p-4">
        <PairPanel
          event={event}
          partners={partners}
          onJumpToPartner={onJumpToPartner}
          onLocatePair={onLocatePair}
        />
        <div className="gt-json-view">
          <HighlightedJson data={event.data} />
        </div>
      </TableCell>
    </TableRow>
  );
}

// ─── 单行事件（memo 化） ──────────────────────────────────────

const EventRow = memo(function EventRow({
  event,
  partners,
  isExpanded,
  isHighlighted,
  onToggle,
  onJumpToPartner,
  onLocatePair,
}: {
  event: DecodedEvent;
  partners: DecodedEvent[];
  isExpanded: boolean;
  isHighlighted: boolean;
  onToggle: (id: string) => void;
  onJumpToPartner: (id: string) => void;
  onLocatePair: (event: DecodedEvent) => void;
}) {
  const meta = useMemo(() => extractMeta(event.data), [event.data]);
  const summary = useMemo(() => summarizePayload(event.data, meta), [event.data, meta]);

  return (
    <Fragment key={event.id}>
      <TableRow
        id={`event-row-${event.id}`}
        className={`cursor-pointer hover:bg-muted/50 transition-colors ${isHighlighted ? "bg-primary/10" : ""}`}
        onClick={() => onToggle(event.id)}
        aria-expanded={isExpanded}
      >
        {/* 时间 */}
        <TableCell className="font-mono text-xs whitespace-nowrap">
          {formatTimestamp(event.timestamp)}
        </TableCell>

        {/* 方向 */}
        <TableCell className="w-24">
          <DirectionBadge direction={meta.direction} />
        </TableCell>

        {/* 消息名 */}
        <TableCell className="min-w-[140px] max-w-[220px]">
          <MessageCell msgName={meta.msgName} semantic={meta.semantic} />
          {partners.length > 0 && !isExpanded && (
            <span className="ml-1.5 inline-flex items-center text-muted-foreground/70" title="已配对请求/响应">
              <Link2 className="h-3 w-3" />
            </span>
          )}
        </TableCell>

        {/* 捕获上下文（代理抓包特有） */}
        <TableCell className="w-44 max-w-[200px]">
          {event.capture ? <CaptureCell capture={event.capture} /> : <span className="text-xs text-muted-foreground/50">-</span>}
        </TableCell>

        {/* Payload 摘要 */}
        <TableCell className="max-w-md">
          <span className="text-xs text-muted-foreground truncate block" title={summary}>
            {summary}
          </span>
        </TableCell>

        {/* 原始包大小 */}
        <TableCell className="w-16 text-right tabular-nums text-xs text-muted-foreground whitespace-nowrap">
          {formatSize(event.raw_len)}
        </TableCell>
      </TableRow>

      {isExpanded && (
        <ExpandedRow
          event={event}
          partners={partners}
          onJumpToPartner={onJumpToPartner}
          onLocatePair={onLocatePair}
        />
      )}
    </Fragment>
  );
});

// ─── 主表格组件 ───────────────────────────────────────────────

export function EventTable({ sessionId, filter, onFilterChange }: EventTableProps) {
  const [page, setPage] = useState<number>(0);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [highlightId, setHighlightId] = useState<string | null>(null);

  useEffect(() => {
    setPage(0);
  }, [sessionId, filter]);

  const offset = page * PAGE_SIZE;

  const {
    data,
    isLoading,
    isError,
    error,
    refetch,
    isFetching,
    isPlaceholderData,
  } = useDecodedData(sessionId, {
    limit: PAGE_SIZE,
    offset,
    filter: filter || undefined,
  });

  const events = useMemo(() => data?.events ?? [], [data]);
  const totalMatched = data?.total_matched ?? 0;
  const totalPages = Math.ceil(totalMatched / PAGE_SIZE);

  /**
   * 配对索引：事件 id → 当前页内的配对伙伴。
   * 配对信号是 causation_id（响应 → 请求事件 id，SDK pair 规则写入），
   * 不用 correlation_id —— 后者可能是插件 decode 侧写的流键（全流共享），不是配对关系。
   */
  const partnersMap = useMemo(() => {
    const byId = new Map(events.map((e) => [e.id, e]));
    const m = new Map<string, DecodedEvent[]>();
    const push = (key: string, val: DecodedEvent) => {
      const arr = m.get(key);
      if (arr) arr.push(val);
      else m.set(key, [val]);
    };
    for (const ev of events) {
      if (!ev.causation_id) continue;
      const req = byId.get(ev.causation_id);
      if (!req) continue;
      push(req.id, ev);
      push(ev.id, req);
    }
    return m;
  }, [events]);

  /** 跳转到当前页内的配对消息：展开并短暂高亮。 */
  function handleJumpToPartner(id: string) {
    setExpandedId(id);
    setTimeout(() => {
      document.getElementById(`event-row-${id}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
    }, 0);
    setHighlightId(id);
    setTimeout(() => setHighlightId(null), 2000);
  }

  /** 配对消息不在当前页：用 causation 关系写筛选表达式定位（App 的 FilterBar 同步更新）。 */
  function handleLocatePair(ev: DecodedEvent) {
    if (!onFilterChange) return;
    setExpandedId(null);
    // 响应方：按请求事件 id 找；请求方：找 causation_id 指向自己的响应。
    onFilterChange(ev.causation_id ? `id == "${ev.causation_id}"` : `causation_id == "${ev.id}"`);
  }

  function handleToggleExpand(eventId: string) {
    setExpandedId((prev) => (prev === eventId ? null : eventId));
  }

  if (!sessionId) {
    return (
      <EmptyState
        icon={<Table2 className="h-5 w-5" />}
        title="未选择会话"
        hint="在左侧会话列表中选择一个会话以查看解码出的协议事件。"
        className="h-64 justify-center"
      />
    );
  }

  if (isLoading) {
    return (
      <div className="space-y-2 p-4">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full" />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <div
        role="alert"
        className="flex items-center gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
      >
        <span className="flex-1">加载失败：{error?.message ?? "未知错误"}</span>
        <Button variant="outline" size="sm" onClick={() => refetch()} className="h-7">
          <RotateCw className="h-3.5 w-3.5" />
          重试
        </Button>
      </div>
    );
  }

  if (events.length === 0) {
    return (
      <EmptyState
        icon={totalMatched === 0 ? <Table2 className="h-5 w-5" /> : <SearchX className="h-5 w-5" />}
        title={totalMatched === 0 ? "暂无解码数据" : "无匹配结果"}
        hint={
          totalMatched === 0
            ? "该会话尚未产生可解码的协议事件，或解码插件尚未绑定。"
            : "尝试调整筛选表达式，或清除筛选查看全部数据。"
        }
        className="h-64 justify-center"
      />
    );
  }

  return (
    <div className="space-y-3 relative">
      {/* 后台刷新指示 */}
      {isFetching && !isLoading && <div className="gt-loading-bar" aria-hidden="true" />}

      {/* 统计信息 */}
      <div className="flex items-center justify-between px-1 text-xs text-muted-foreground" aria-live="polite">
        <span className="tabular-nums">
          共 {totalMatched} 条 · 当前第 {offset + 1}–{Math.min(offset + PAGE_SIZE, totalMatched)} 条
          {isPlaceholderData ? " · 更新中…" : ""}
        </span>
        <span className="text-[11px] text-muted-foreground/70">点击行展开完整 JSON</span>
      </div>

      {/* 数据表格 */}
      <Table className="gt-table">
        <TableHeader>
          <TableRow>
            <TableHead className="w-44">时间</TableHead>
            <TableHead className="w-24">方向</TableHead>
            <TableHead className="min-w-[140px]">消息</TableHead>
            <TableHead className="w-44">捕获</TableHead>
            <TableHead>摘要</TableHead>
            <TableHead className="w-16 text-right">大小</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {events.map((event: DecodedEvent) => (
            <EventRow
              key={event.id}
              event={event}
              partners={partnersMap.get(event.id) ?? []}
              isExpanded={expandedId === event.id}
              isHighlighted={highlightId === event.id}
              onToggle={handleToggleExpand}
              onJumpToPartner={handleJumpToPartner}
              onLocatePair={handleLocatePair}
            />
          ))}
        </TableBody>
      </Table>

      {/* 分页控件 */}
      {totalPages > 1 && (
        <div className="flex items-center justify-center gap-2 pt-2">
          <Button variant="outline" size="icon" className="h-8 w-8" onClick={() => setPage(0)} disabled={page === 0} aria-label="第一页">
            <ChevronsLeft className="h-4 w-4" />
          </Button>
          <Button variant="outline" size="icon" className="h-8 w-8" onClick={() => setPage((p) => Math.max(0, p - 1))} disabled={page === 0} aria-label="上一页">
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <span className="px-2 text-sm tabular-nums">{page + 1} / {totalPages}</span>
          <Button variant="outline" size="icon" className="h-8 w-8" onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))} disabled={page >= totalPages - 1} aria-label="下一页">
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
      )}
    </div>
  );
}
