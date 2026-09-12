import { useState, useEffect, useMemo, Fragment, memo, useRef } from "react";
import { useDecodedData } from "@/hooks/use-mcp";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Badge } from "@/components/ui/badge";
import { toast } from "@/components/ui/toast";
import {
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronRight as TreeChevron,
  Table2,
  SearchX,
  RotateCw,
  ArrowRight,
  Link2,
  GitFork,
  Copy,
  ChevronUp,
  Box,
  Activity,
  Hash,
  FileCode2,
} from "lucide-react";
import type { DecodedEvent } from "@/types/event";
import type { CaptureContext, ConnectionSummary } from "@/types/connection";
import {
  extractMeta,
  formatTimestamp,
  formatSize,
  DirectionIcon,
  DirectionChip,
  MessageCell,
  HighlightedJson,
  StructuredFields,
  classifyPayload,
  businessPayload,
  analysisOf,
  OpBadge,
  type EventMeta,
} from "@/lib/event-display";
import { eventMatchesQuery, eventMatchesDirection, eventMatchesConnection, type DirectionFilter } from "@/lib/fuzzy";

interface EventTableProps {
  sessionId: string | null;
  /** 模糊查询关键词；非空时一次性拉取较大批次在前端内存过滤，空时保持分页拉取。 */
  query: string;
  /** 消息方向过滤（C→S / S→C，空 = 全部）；与 query 叠加为 AND。 */
  direction: DirectionFilter;
  /** 连接过滤（null = 全部连接，由顶部过滤栏切换）；按捕获上下文 conn_id 匹配，与 query/direction 叠加为 AND。 */
  connFilter: ConnectionSummary | null;
}

const PAGE_SIZES = [20, 50, 100];
/** 有查询词时向前端内存过滤提供的事件批次上限。 */
const QUERY_FETCH_LIMIT = 1000;

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

/** 复制 JSON 到剪贴板（失败给 toast，不静默）。 */
async function copyJson(data: unknown) {
  try {
    await navigator.clipboard.writeText(JSON.stringify(data, null, 2));
    toast.success("已复制 JSON");
  } catch {
    toast.error("复制失败", "浏览器拒绝访问剪贴板");
  }
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

/** 配对面板：展示 pair 语义规则配出的对侧消息（响应 causation_id → 请求），可跳转定位。 */
function PairPanel({
  event,
  partners,
  onJumpToPartner,
}: {
  event: DecodedEvent;
  partners: DecodedEvent[];
  onJumpToPartner: (id: string) => void;
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
          const pMeta = extractMeta(p.data, p.meta);
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
          配对消息不在当前页（可在上方模糊搜索框输入该消息名或 id 定位）
        </span>
      )}
    </div>
  );
}

/** 父子层级面板：展示 extract 规则产出的子事件（ParentID 指向本事件），可跳转定位。 */
function ChildPanel({
  children,
  onJumpToChild,
}: {
  children: DecodedEvent[];
  onJumpToChild: (id: string) => void;
}) {
  if (children.length === 0) return null;

  return (
    <div className="mb-3 rounded-md border border-border bg-background px-3 py-2">
      <div className="mb-1.5 inline-flex items-center gap-1 text-xs font-medium text-muted-foreground">
        <GitFork className="h-3.5 w-3.5" />
        父子层级
        <span className="text-[10px] font-normal text-muted-foreground/70">
          {children.length} 个子事件
        </span>
      </div>
      <ul className="space-y-1">
        {children.map((child) => {
          const cMeta = extractMeta(child.data, child.meta);
          return (
            <li key={child.id} className="flex items-center gap-2">
              <span className="text-[10px] text-muted-foreground/50">
                ├─{" "}
              </span>
              <button
                type="button"
                onClick={() => onJumpToChild(child.id)}
                className="inline-flex items-center gap-1.5 rounded-full border border-border bg-muted/40 px-2 py-0.5 text-xs hover:bg-muted transition-colors"
                title={`跳转到子事件 ${cMeta.msgName || child.id}`}
              >
                <TreeChevron className="h-3 w-3 text-muted-foreground" />
                <span className="font-mono font-semibold">{cMeta.msgName || "(unknown)"}</span>
                <span className="text-muted-foreground">({child.protocol})</span>
                <span className="font-mono text-muted-foreground">{formatTimestamp(child.timestamp)}</span>
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/**
 * 配对并排视图：展开 pair 关系时左请求 / 右响应，左右并列展示双方原始 JSON payload。
 * 仅当当前事件处于某个配对组时使用；否则回退到单事件展示。
 */
function PairDetail({
  request,
  responses,
}: {
  request: DecodedEvent;
  responses: DecodedEvent[];
}) {
  const reqMeta = useMemo(() => extractMeta(request.data, request.meta), [request.data, request.meta]);
  const reqBiz = useMemo(() => businessPayload(request), [request]);
  const responseItems = useMemo(
    () =>
      responses.map((ev) => ({
        ev,
        meta: extractMeta(ev.data, ev.meta),
        business: businessPayload(ev),
      })),
    [responses],
  );

  const renderPayload = (biz: Record<string, unknown>) =>
    Object.keys(biz).length > 0 ? (
      <div className="gt-json-view">
        <HighlightedJson data={biz} />
      </div>
    ) : (
      <p className="px-1 py-2 text-center text-xs text-muted-foreground">（无业务 payload 字段）</p>
    );

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
      {/* 左：请求 */}
      <div className="flex min-w-0 flex-col rounded-lg border border-border bg-background p-2">
        <div className="mb-1.5 flex flex-wrap items-center gap-1.5 border-b border-border pb-1.5">
          <DirectionIcon direction={reqMeta.direction} />
          <MessageCell msgName={reqMeta.msgName} semantic={reqMeta.semantic} />
          <span className="rounded bg-blue-50 px-1 py-px text-[10px] font-medium text-blue-700 dark:bg-blue-950 dark:text-blue-300">
            请求
          </span>
          <span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground">
            {formatTimestamp(request.timestamp)}
          </span>
        </div>
        {renderPayload(reqBiz)}
      </div>

      {/* 右：响应（可能多条，纵向堆叠） */}
      <div className="flex min-w-0 flex-col gap-3">
        {responseItems.length === 0 ? (
          <div className="flex h-full items-center justify-center rounded-lg border border-dashed border-border text-xs text-muted-foreground">
            无响应数据
          </div>
        ) : (
          responseItems.map(({ ev, meta, business }) => (
            <div key={ev.id} className="flex min-w-0 flex-col rounded-lg border border-border bg-background p-2">
              <div className="mb-1.5 flex flex-wrap items-center gap-1.5 border-b border-border pb-1.5">
                <DirectionIcon direction={meta.direction} />
                <MessageCell msgName={meta.msgName} semantic={meta.semantic} />
                <span className="rounded bg-emerald-50 px-1 py-px text-[10px] font-medium text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300">
                  响应
                </span>
                <span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground">
                  {formatTimestamp(ev.timestamp)}
                </span>
              </div>
              {renderPayload(business)}
            </div>
          ))
        )}
      </div>
    </div>
  );
}

// ─── 分析 / 业务数据面板（两栏） ───────────────────────────────
// 心智：业务数据（左）| GameTrace 分析（右）。_meta 与原始 JSON 收进「高级信息」。

function SectionCard({
  icon,
  title,
  children,
}: {
  icon?: React.ReactNode;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="rounded-lg border border-border bg-background p-2">
      <div className="mb-1.5 flex items-center gap-1.5 border-b border-border pb-1.5">
        {icon}
        <span className="text-xs font-semibold text-foreground">{title}</span>
      </div>
      <div className="space-y-1">{children}</div>
    </div>
  );
}

function RenderTable({ rows }: { rows: { k: string; v: React.ReactNode }[] }) {
  if (rows.length === 0) return null;
  return (
    <dl className="divide-y divide-border/60 rounded-md border border-border bg-background text-xs">
      {rows.map((r) => (
        <div key={r.k} className="flex items-start gap-2 px-2 py-1">
          <dt className="shrink-0 pt-px font-mono text-muted-foreground">{r.k}</dt>
          <dd className="min-w-0 break-all text-foreground/90">{r.v}</dd>
        </div>
      ))}
    </dl>
  );
}

/** 「GameTrace 分析」：实体、状态变更、correlation、配对、父子聚合区。 */
function AnalysisPanel({
  event,
  partners,
  children,
  analysis,
  onJumpToPartner,
  onJumpToChild,
}: {
  event: DecodedEvent;
  partners: DecodedEvent[];
  children: DecodedEvent[];
  analysis: Record<string, unknown>;
  onJumpToPartner: (id: string) => void;
  onJumpToChild: (id: string) => void;
}) {
  const sc = Array.isArray(analysis._state_changes) ? analysis._state_changes : [];
  const entityRows: { k: string; v: React.ReactNode }[] = [];
  if (analysis.entity_type != null) entityRows.push({ k: "entity_type", v: String(analysis.entity_type) });
  if (analysis.entity_id != null) entityRows.push({ k: "entity_id", v: String(analysis.entity_id) });
  if (analysis.entity != null) entityRows.push({ k: "entity", v: String(analysis.entity) });
  if (analysis.change_count != null) entityRows.push({ k: "change_count", v: String(analysis.change_count) });

  const correlationRows: { k: string; v: React.ReactNode }[] = [];
  if (event.correlation_id) correlationRows.push({ k: "correlation_id", v: event.correlation_id });
  if (analysis.flow_id != null) correlationRows.push({ k: "flow_id", v: String(analysis.flow_id) });
  if (event.causation_id) correlationRows.push({ k: "causation_id", v: event.causation_id });
  if (event.parent_id) correlationRows.push({ k: "parent_id", v: event.parent_id });

  return (
    <div className="space-y-2">
      <PairPanel
        event={event}
        partners={partners}
        onJumpToPartner={onJumpToPartner}
      />
      <ChildPanel children={children} onJumpToChild={onJumpToChild} />

      {entityRows.length > 0 && (
        <SectionCard icon={<Box className="h-3.5 w-3.5" />} title="实体">
          <RenderTable rows={entityRows} />
        </SectionCard>
      )}

      {sc.length > 0 && (
        <SectionCard icon={<Activity className="h-3.5 w-3.5" />} title={`状态变更 · ${sc.length}`}>
          <ul className="space-y-1">
            {sc.map((item, i) => {
              const it = item as Record<string, unknown> | null;
              if (!it) return null;
              return (
                <li key={i} className="flex items-center gap-1.5 text-xs">
                  <OpBadge op={String(it.op ?? "")} />
                  <span className="truncate font-mono text-muted-foreground">{String(it.path ?? "")}</span>
                  <span className="truncate text-foreground/70">
                    {fmtBrief(it.before)} → {fmtBrief(it.after)}
                  </span>
                </li>
              );
            })}
          </ul>
        </SectionCard>
      )}

      {correlationRows.length > 0 && (
        <SectionCard icon={<Hash className="h-3.5 w-3.5" />} title="关联">
          <RenderTable rows={correlationRows} />
        </SectionCard>
      )}
    </div>
  );
}

function fmtBrief(v: unknown): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "object") return JSON.stringify(v).slice(0, 24);
  return String(v);
}

/**
 * 元信息小窗口：点击「元信息」按钮弹出 _meta 内容（结构化字段），
 * 让 payload 区保持原始 JSON 干净，meta 只在需要时看。
 */
function MetaPopover({ meta }: { meta: Record<string, unknown> }) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDocClick = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  }, [open]);

  const metaObj = meta;
  const hasMeta = Object.keys(metaObj).length > 0;

  return (
    <span className="relative inline-block" ref={wrapRef}>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
        aria-expanded={open}
        className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground"
      >
        <FileCode2 className="h-3 w-3" />
        元信息
      </button>
      {open && (
        <div className="absolute right-0 top-full z-50 mt-1 max-h-80 w-80 overflow-auto rounded-lg border border-border bg-card p-2 shadow-lg">
          {hasMeta ? (
            <StructuredFields obj={metaObj} />
          ) : (
            <p className="px-2 py-1.5 text-xs text-muted-foreground">（无 _meta 数据）</p>
          )}
        </div>
      )}
    </span>
  );
}

/**
 * 展开区头部：展开后内容较长，很容易忘记自己点开的是哪一行，
 * 所以先重复一遍身份信息，并把「元信息 / 分析 / 复制 / 收起」放在手边。
 */
function ExpandedHeader({
  event,
  meta,
  metaRaw,
  showAnalysis,
  onToggleAnalysis,
  onCollapse,
}: {
  event: DecodedEvent;
  meta: EventMeta;
  metaRaw: Record<string, unknown>;
  showAnalysis: boolean;
  onToggleAnalysis: () => void;
  onCollapse: () => void;
}) {
  return (
    <div className="mb-3 flex flex-wrap items-center gap-2 border-b border-border pb-2">
      <DirectionChip direction={meta.direction} />
      <span className="font-mono text-sm font-semibold">{meta.msgName || "(unknown)"}</span>
      <span className="font-mono text-xs text-muted-foreground">
        {formatTimestamp(event.timestamp)}
      </span>
      {event.protocol && (
        <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
          {event.protocol}
        </span>
      )}
      <span className="text-xs text-muted-foreground">{formatSize(event.raw_len)}</span>
      <span className="ml-auto flex items-center gap-1.5">
        <MetaPopover meta={metaRaw} />
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onToggleAnalysis();
          }}
          aria-expanded={showAnalysis}
          className={`inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs transition-colors ${
            showAnalysis
              ? "border-primary/40 bg-primary/10 text-primary"
              : "border-border text-muted-foreground hover:bg-muted/60 hover:text-foreground"
          }`}
        >
          <Activity className="h-3 w-3" />
          分析
        </button>
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            void copyJson(event.data);
          }}
          className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground"
        >
          <Copy className="h-3 w-3" />
          复制 JSON
        </button>
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onCollapse();
          }}
          className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground"
        >
          <ChevronUp className="h-3 w-3" />
          收起
        </button>
      </span>
    </div>
  );
}

function ExpandedRow({
  event,
  partners,
  children,
  colSpan,
  onJumpToPartner,
  onJumpToChild,
  onCollapse,
}: {
  event: DecodedEvent;
  partners: DecodedEvent[];
  children: DecodedEvent[];
  colSpan: number;
  onJumpToPartner: (id: string) => void;
  onJumpToChild: (id: string) => void;
  onCollapse: () => void;
}) {
  const meta = useMemo(() => extractMeta(event.data, event.meta), [event.data, event.meta]);
  const classes = useMemo(
    () => ({
      // v0.8.0 契约：data 已是纯业务，meta/analysis 走独立字段；旧数据兜底拆分。
      meta: event.meta ?? classifyPayload(event.data).meta,
      business: businessPayload(event),
      analysis: analysisOf(event),
    }),
    [event],
  );
  const [showAnalysis, setShowAnalysis] = useState(false);

  // 配对并排视图所需的请求/响应分组：
  // - 当前事件无 causation_id → 它是请求，响应是 partners 里 causation_id 指向它的事件。
  // - 当前事件有 causation_id → 它是响应，在 partners 里找到它的请求；找不到则不进入并排。
  const { request, responses } = useMemo(() => {
    if (event.causation_id) {
      const req = partners.find((p) => p.id === event.causation_id);
      return req ? { request: req, responses: [event] } : { request: event, responses: [] };
    }
    const resps = partners.filter((p) => p.causation_id === event.id);
    return { request: event, responses: resps };
  }, [event, partners]);
  const showPair = responses.length > 0;

  return (
    <TableRow className="gt-fade-in">
      <TableCell colSpan={colSpan} className="bg-muted/30 p-3">
        <ExpandedHeader
          event={event}
          meta={meta}
          metaRaw={classes.meta}
          showAnalysis={showAnalysis}
          onToggleAnalysis={() => setShowAnalysis((o) => !o)}
          onCollapse={onCollapse}
        />

        {/* 第一眼：业务 payload 原始 JSON（剥离 _meta 与分析字段）；pair 时左右并列请求/响应 */}
        {showPair ? (
          <PairDetail request={request} responses={responses} />
        ) : Object.keys(classes.business).length > 0 ? (
          <div className="rounded-lg border border-border bg-background p-2">
            <div className="gt-json-view">
              <HighlightedJson data={classes.business} />
            </div>
          </div>
        ) : (
          <div className="rounded-lg border border-dashed border-border bg-background p-4 text-center text-xs text-muted-foreground">
            该事件没有业务 payload 字段，元信息与分析见头部「元信息 / 分析」按钮
          </div>
        )}

        {/* 分析区：折叠，需要时展开 */}
        {showAnalysis && (
          <div className="mt-3">
            <AnalysisPanel
              event={event}
              partners={partners}
              children={children}
              analysis={classes.analysis}
              onJumpToPartner={onJumpToPartner}
              onJumpToChild={onJumpToChild}
            />
          </div>
        )}
      </TableCell>
    </TableRow>
  );
}

// ─── 单行事件（memo 化） ──────────────────────────────────────

const EventRow = memo(function EventRow({
  event,
  partners,
  children,
  showCapture,
  isExpanded,
  isHighlighted,
  onToggle,
  onJumpToPartner,
  onJumpToChild,
  onCollapse,
}: {
  event: DecodedEvent;
  partners: DecodedEvent[];
  children: DecodedEvent[];
  showCapture: boolean;
  isExpanded: boolean;
  isHighlighted: boolean;
  onToggle: (id: string) => void;
  onJumpToPartner: (id: string) => void;
  onJumpToChild: (id: string) => void;
  onCollapse: (id: string) => void;
}) {
  const meta = useMemo(() => extractMeta(event.data, event.meta), [event.data, event.meta]);
  const summary = useMemo(() => summarizePayload(event.data, meta), [event.data, meta]);
  const colSpan = showCapture ? 6 : 5;

  return (
    <Fragment key={event.id}>
      <TableRow
        id={`event-row-${event.id}`}
        className={`cursor-pointer transition-colors ${isHighlighted ? "bg-primary/10" : ""} ${isExpanded ? "bg-muted/40" : ""}`}
        onClick={() => onToggle(event.id)}
        aria-expanded={isExpanded}
      >
        {/* 展开指示：没有它用户看不出行是可点的 */}
        <TableCell className="w-8 py-1.5 pl-2 pr-0 text-muted-foreground/60">
          <ChevronRight
            className={`h-3.5 w-3.5 transition-transform ${isExpanded ? "rotate-90" : ""}`}
          />
        </TableCell>

        {/* 时间 */}
        <TableCell className="w-28 py-1.5 font-mono text-[11px] whitespace-nowrap tabular-nums">
          {formatTimestamp(event.timestamp)}
        </TableCell>

        {/* 消息名：方向文字 + 名称 + 语义标签 + 子事件/配对角标 */}
        <TableCell className="min-w-[220px] max-w-[340px] py-1.5">
          <div className="flex items-center gap-1.5 min-w-0">
            {event.parent_id && (
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground/60" title={`父事件 ${event.parent_id}`}>
                ⊢
              </span>
            )}
            <DirectionChip direction={meta.direction} />
            <MessageCell msgName={meta.msgName} semantic={meta.semantic} />
            {children.length > 0 && (
              <span className="ml-0.5 shrink-0 inline-flex items-center gap-0.5 text-muted-foreground/70" title={`${children.length} 个 extract 子事件`}>
                <GitFork className="h-3 w-3" />
                <span className="text-[10px]">{children.length}</span>
              </span>
            )}
            {partners.length > 0 && !isExpanded && (
              <span className="ml-0.5 shrink-0 inline-flex items-center text-muted-foreground/70" title="已配对请求/响应">
                <Link2 className="h-3 w-3" />
              </span>
            )}
          </div>
        </TableCell>

        {/* 捕获上下文（代理抓包特有）：非代理抓包整列不渲染，把宽度让给消息与摘要 */}
        {showCapture && (
          <TableCell className="w-40 max-w-[180px] py-1.5">
            {event.capture ? (
              <CaptureCell capture={event.capture} />
            ) : (
              <span className="text-[11px] text-muted-foreground/50">-</span>
            )}
          </TableCell>
        )}

        {/* Payload 摘要 */}
        <TableCell className="max-w-[28rem] py-1.5">
          <span className="block truncate text-[11px] text-foreground/70" title={summary}>
            {summary}
          </span>
        </TableCell>

        {/* 原始包大小 */}
        <TableCell className="w-16 py-1.5 text-right tabular-nums text-[11px] text-muted-foreground whitespace-nowrap">
          {formatSize(event.raw_len)}
        </TableCell>
      </TableRow>

      {isExpanded && (
        <ExpandedRow
          event={event}
          partners={partners}
          children={children}
          colSpan={colSpan}
          onJumpToPartner={onJumpToPartner}
          onJumpToChild={onJumpToChild}
          onCollapse={() => onCollapse(event.id)}
        />
      )}
    </Fragment>
  );
});

// ─── 主表格组件 ───────────────────────────────────────────────

export function EventTable({ sessionId, query, direction, connFilter }: EventTableProps) {
  const [page, setPage] = useState<number>(0);
  const [pageSize, setPageSize] = useState<number>(PAGE_SIZES[0]!);
  // 允许多行同时展开：对比请求/响应时不用来回点，这是最常见的阅读动作。
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const [highlightId, setHighlightId] = useState<string | null>(null);

  useEffect(() => {
    setPage(0);
    setExpandedIds(new Set());
  }, [sessionId, query, direction, connFilter]);

  const offset = page * pageSize;

  // 有查询词、方向过滤或连接过滤时：一次性拉取较大批次在前端内存过滤
  // （保证搜索跨页完整不遗漏）；无过滤条件时：保持原有分页拉取。
  const useLargeLimit = !!query || !!direction || !!connFilter;
  const effectiveLimit = useLargeLimit ? QUERY_FETCH_LIMIT : pageSize;
  const effectiveOffset = useLargeLimit ? 0 : offset;

  const {
    data,
    isLoading,
    isError,
    error,
    refetch,
    isFetching,
    isPlaceholderData,
  } = useDecodedData(sessionId, {
    limit: effectiveLimit,
    offset: effectiveOffset,
  });

  const events = useMemo(() => data?.events ?? [], [data]);
  const totalMatched = data?.total_matched ?? 0;

  // 前端过滤：搜索/方向/连接过滤态在已拉取的批次上过滤（AND）。
  const filteredEvents = useMemo(() => {
    if (!query && !direction && !connFilter) return events;
    return events.filter((e) => {
      if (query && !eventMatchesQuery(e, query)) return false;
      if (direction && !eventMatchesDirection(e, direction)) return false;
      if (connFilter && !eventMatchesConnection(e, connFilter)) return false;
      return true;
    });
  }, [events, query, direction, connFilter]);

  const filtering = !!query || !!direction || !!connFilter;

  const totalPages = Math.ceil(totalMatched / pageSize);
  // 整页都没有 capture 上下文（非代理抓包）时隐藏该列，把宽度让给消息与摘要。
  const showCapture = useMemo(() => filteredEvents.some((e) => !!e.capture), [filteredEvents]);

  /**
   * 配对索引：事件 id → 当前页内的配对伙伴。
   * 配对信号是 causation_id（响应 → 请求事件 id，SDK pair 规则写入），
   * 不用 correlation_id —— 后者可能是插件 decode 侧写的流键（全流共享），不是配对关系。
   */
  const partnersMap = useMemo(() => {
    const byId = new Map(filteredEvents.map((e) => [e.id, e]));
    const m = new Map<string, DecodedEvent[]>();
    const push = (key: string, val: DecodedEvent) => {
      const arr = m.get(key);
      if (arr) arr.push(val);
      else m.set(key, [val]);
    };
    for (const ev of filteredEvents) {
      if (!ev.causation_id) continue;
      const req = byId.get(ev.causation_id);
      if (!req) continue;
      push(req.id, ev);
      push(ev.id, req);
    }
    return m;
  }, [filteredEvents]);

  /**
   * 父子索引：父事件 id → 当前页内的提取子事件（parent_id 指回父事件，extract 规则写入）。
   */
  const childrenMap = useMemo(() => {
    const m = new Map<string, DecodedEvent[]>();
    for (const ev of filteredEvents) {
      if (!ev.parent_id) continue;
      const arr = m.get(ev.parent_id);
      if (arr) arr.push(ev);
      else m.set(ev.parent_id, [ev]);
    }
    return m;
  }, [filteredEvents]);

  /** 展开并滚动定位到某事件（配对伙伴 / 子事件跳转）。 */
  function handleJumpTo(id: string) {
    setExpandedIds((prev) => new Set(prev).add(id));
    setHighlightId(id);
    setTimeout(() => {
      document.getElementById(`event-row-${id}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
    }, 0);
    setTimeout(() => setHighlightId(null), 2000);
  }

  function handleToggleExpand(eventId: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(eventId)) next.delete(eventId);
      else next.add(eventId);
      return next;
    });
  }

  function handleCollapse(eventId: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      next.delete(eventId);
      return next;
    });
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

  if (filteredEvents.length === 0) {
    return (
      <EmptyState
        icon={filtering || totalMatched === 0 ? <SearchX className="h-5 w-5" /> : <Table2 className="h-5 w-5" />}
        title={filtering ? "无匹配结果" : totalMatched === 0 ? "暂无解码数据" : "无数据"}
        hint={
          filtering
            ? "没有事件命中当前过滤条件，尝试更换或清除搜索条件 / 方向过滤。"
            : totalMatched === 0
              ? "该会话尚未产生可解码的协议事件，或解码插件尚未绑定。"
              : "暂无数据。"
        }
        className="h-64 justify-center"
      />
    );
  }

  return (
    <div className="space-y-3 relative">
      {/* 后台刷新指示 */}
      {isFetching && !isLoading && <div className="gt-loading-bar" aria-hidden="true" />}

      {/* 统计信息 + 每页条数（搜索态隐藏分页相关控件） */}
      <div className="flex flex-wrap items-center justify-between gap-2 px-1 text-xs text-muted-foreground" aria-live="polite">
        <span className="tabular-nums">
          {filtering ? (
            <>
              命中 <b className="font-semibold text-foreground">{filteredEvents.length}</b> 条
            </>
          ) : (
            <>
              共 {totalMatched} 条 · 当前第 {offset + 1}–{Math.min(offset + pageSize, totalMatched)} 条
            </>
          )}
          {isPlaceholderData ? " · 更新中…" : ""}
          {expandedIds.size > 0 ? ` · 已展开 ${expandedIds.size} 行` : ""}
        </span>
        <span className="flex items-center gap-3">
          {!filtering && (
            <span className="flex items-center gap-2">
              <span className="text-[11px] text-muted-foreground/70">点击行展开完整 JSON</span>
              <label className="flex items-center gap-1">
                <span className="text-[11px] text-muted-foreground/70">每页</span>
                <select
                  value={pageSize}
                  onChange={(e) => {
                    setPageSize(Number(e.target.value));
                    setPage(0);
                    setExpandedIds(new Set());
                  }}
                  className="h-7 rounded-md border border-input bg-background px-1.5 text-xs"
                  aria-label="每页条数"
                >
                  {PAGE_SIZES.map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </select>
              </label>
            </span>
          )}
        </span>
      </div>

      {/* 数据表格 */}
      {/* 表头吸顶交给页面级滚动容器（见 ui/table.tsx 的 containerClassName 说明） */}
      <Table className="gt-table" containerClassName="relative w-full overflow-visible">
        <TableHeader>
          <TableRow>
            <TableHead className="w-8 pl-2 pr-0" aria-label="展开" />
            <TableHead className="w-28">时间</TableHead>
            <TableHead className="min-w-[220px]">消息</TableHead>
            {showCapture && <TableHead className="w-40">捕获</TableHead>}
            <TableHead>摘要</TableHead>
            <TableHead className="w-16 text-right">大小</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filteredEvents.map((event: DecodedEvent) => (
            <EventRow
              key={event.id}
              event={event}
              partners={partnersMap.get(event.id) ?? []}
              children={childrenMap.get(event.id) ?? []}
              showCapture={showCapture}
              isExpanded={expandedIds.has(event.id)}
              isHighlighted={highlightId === event.id}
              onToggle={handleToggleExpand}
              onJumpToPartner={handleJumpTo}
              onJumpToChild={handleJumpTo}
              onCollapse={handleCollapse}
            />
          ))}
        </TableBody>
      </Table>

      {/* 分页控件（过滤态隐藏） */}
      {!filtering && totalPages > 1 && (
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
