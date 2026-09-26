import { useState, useEffect, useMemo, Fragment, memo, useRef } from "react";
import type { ReactNode } from "react";
import { useDecodedData, usePairGroup } from "@/hooks/use-mcp";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Dialog } from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast";
import {
  ChevronLeft,
  ChevronRight,
  ChevronDown,
  ChevronsLeft,
  Table2,
  Search,
  SearchX,
  X,
  RotateCw,
  ArrowRight,
  Link2,
  Copy,
  ChevronUp,
  Box,
  Activity,
  Hash,
  FileCode2,
  Crosshair,
} from "lucide-react";
import type { DecodedEvent } from "@/types/event";
import type { ConnectionSummary } from "@/types/connection";
import { navigate } from "@/lib/router";
import { sessionHref } from "@/lib/routes";
import {
  extractMeta,
  formatTimestamp,
  formatSize,
  DirectionIcon,
  DirectionChip,
  MessageCell,
  SemanticBadge,
  HighlightedJson,
  StructuredFields,
  classifyPayload,
  businessPayload,
  analysisOf,
  OpBadge,
  type EventMeta,
} from "@/lib/event-display";
import {
  eventMatchesQuery,
  eventExcludesQuery,
  eventMatchesDirection,
  eventMatchesConnection,
  fuzzyTokens,
  haystackIncludes,
  type DirectionFilter,
  type SemanticFilter,
} from "@/lib/fuzzy";
import { mergePartnerPool, pairGroupFilter, resolvePairGroup } from "@/lib/pair-group";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";

interface EventTableProps {
  sessionId: string | null;
  /** 模糊查询关键词；非空时一次性拉取较大批次在前端内存过滤，空时保持分页拉取。 */
  query: string;
  /** 剔除关键词（逗号或空白分隔，命中任一词即隐藏该条）；与 query 看同一份字段包，
   *  非空时同样切到大批次内存过滤，否则只能剔到当前这一页。 */
  exclude: string;
  /** 消息方向过滤（C→S / S→C，空 = 全部）；与 query 叠加为 AND。 */
  direction: DirectionFilter;
  /** 语义标签过滤集合（插件 annotate 声明，词表随项目而变）；空数组 = 全部。
   *  服务端 list_decoded_data 按「或」合并过滤，保持正常分页与精确 total_matched。 */
  semantics: SemanticFilter;
  /** 连接过滤（null = 全部连接，由顶部过滤栏切换）；按捕获上下文 conn_id 匹配，与 query/direction 叠加为 AND。 */
  connFilter: ConnectionSummary | null;
  /** 跨视图定位：状态变更视图跳来看某一条消息时，服务端按 id 精确取那一条。
   *  走服务端而不是翻分页 —— 目标消息在第几页前端无从得知，翻不到就是跳了个空。 */
  focusEventId?: string | null;
  /** 退出定位模式（回到当前过滤条件下的完整列表）。 */
  onExitFocus?: () => void;
}

const PAGE_SIZES = [20, 50, 100];
/** 有查询词时向前端内存过滤提供的事件批次上限。 */
const QUERY_FETCH_LIMIT = 1000;

/** 生成一行 payload 摘要：结构化键值对（键 = 业务字段名，值 = 标量值）。 */
interface SummaryPart {
  k: string;
  v: string;
}

function summarizePayload(data: Record<string, unknown>, meta: EventMeta): SummaryPart[] {
  const parts: SummaryPart[] = [];

  // Blocks 数量
  if (meta.blocks != null) {
    parts.push({ k: "blocks", v: String(meta.blocks) });
  }

  // 提取顶层非 _meta 的标量字段作为补充信息
  for (const [k, v] of Object.entries(data)) {
    if (k.startsWith("_") || k === "Blocks") continue;
    if (typeof v === "string" && v.length < 40) {
      parts.push({ k, v });
    } else if (typeof v === "number" || typeof v === "boolean") {
      parts.push({ k, v: String(v) });
    }
    // 超过 3 个字段就停止，保持摘要简洁
    if (parts.length >= 4) break;
  }

  return parts;
}

/**
 * 摘要单元格：业务字段值用大字号前景色突出（视觉锚点），
 * 键名作为等宽小字标签，避免整行都是 70% 透明度的弱文本。
 */
function SummaryCell({ parts }: { parts: SummaryPart[] }) {
  if (parts.length === 0) {
    return <span className="text-xs text-muted-foreground">(empty)</span>;
  }
  const title = parts.map((p) => `${p.k}: ${p.v}`).join(" · ");
  return (
    <span className="block truncate text-[13px] leading-snug text-foreground" title={title}>
      {parts.map((p, i) => (
        <span key={i}>
          {i > 0 && <span className="mx-1 text-muted-foreground">·</span>}
          <span className="font-mono text-micro font-medium text-muted-foreground">{p.k}</span>
          <span className="ml-1">{p.v}</span>
        </span>
      ))}
    </span>
  );
}

/** 事件自带的变更数：优先 analysis.change_count，缺字段时回退到 _state_changes 长度。 */
function stateChangeCount(event: DecodedEvent): number {
  const analysis = analysisOf(event);
  if (typeof analysis.change_count === "number") return analysis.change_count;
  const sc = analysis._state_changes;
  return Array.isArray(sc) ? sc.length : 0;
}

/** 展示 before/after；缺失前值写「无前值」而不是 null —— 全量下发的首次赋值不是数据缺失。 */
function fmtValue(v: unknown): string {
  if (v === null || v === undefined) return "无前值";
  if (typeof v === "object") return JSON.stringify(v);
  if (typeof v === "string") return v;
  return String(v);
}

interface ScChange {
  id: string;
  path: string;
  op: string;
  before: unknown;
  after: unknown;
}

interface ScEntity {
  key: string;
  subject_type: string;
  subject_id: string;
  changes: ScChange[];
}

interface ScTypeBucket {
  subject_type: string;
  entities: ScEntity[];
  change_count: number;
}

/**
 * 两级聚合：先按实体类型收拢，再按具体实体分组。
 * 批量同步一条消息能改十几个同类实体，平铺会把要看的那一个埋掉。
 * 类型内按变化数降序，类型之间同样按变化数降序。
 */
function groupStateChanges(sc: Record<string, unknown>[]): ScTypeBucket[] {
  const byType = new Map<string, Map<string, ScEntity>>();
  sc.forEach((it, i) => {
    const type = String(it.subject_type ?? "");
    const id = String(it.subject_id ?? "");
    let entities = byType.get(type);
    if (!entities) {
      entities = new Map();
      byType.set(type, entities);
    }
    const key = `${type}:${id}`;
    let e = entities.get(key);
    if (!e) {
      e = { key, subject_type: type, subject_id: id, changes: [] };
      entities.set(key, e);
    }
    e.changes.push({
      id: `${key}-${i}`,
      path: String(it.path ?? ""),
      op: String(it.op ?? ""),
      before: it.before,
      after: it.after,
    });
  });

  const out: ScTypeBucket[] = [];
  for (const [type, entities] of byType) {
    const list = [...entities.values()].sort((a, b) => b.changes.length - a.changes.length);
    out.push({
      subject_type: type,
      entities: list,
      change_count: list.reduce((n, e) => n + e.changes.length, 0),
    });
  }
  out.sort((a, b) => b.change_count - a.change_count || a.subject_type.localeCompare(b.subject_type));
  return out;
}

/** 变更值 → 检索文本：缺失前值不入 haystack（否则搜「无」会命中所有首次赋值）。 */
function hayValue(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "object") {
    try {
      return JSON.stringify(v);
    } catch {
      return "";
    }
  }
  return String(v);
}

/** 单条字段变更是否命中查询：实体类型 / id / 字段路径 / op / 变更前后的值。 */
function scChangeMatches(entity: ScEntity, c: ScChange, tokens: string[]): boolean {
  if (tokens.length === 0) return true;
  const haystack = [
    entity.subject_type,
    entity.subject_id,
    entity.key,
    c.path,
    c.op,
    hayValue(c.before),
    hayValue(c.after),
  ]
    .join(" ")
    .toLowerCase();
  return haystackIncludes(haystack, tokens);
}

/** 按单条变化的粒度过滤，保留原排序；实体/类型只留仍有命中的。 */
function filterChangeBuckets(buckets: ScTypeBucket[], tokens: string[]): ScTypeBucket[] {
  if (tokens.length === 0) return buckets;
  const out: ScTypeBucket[] = [];
  for (const b of buckets) {
    const entities: ScEntity[] = [];
    for (const e of b.entities) {
      const changes = e.changes.filter((c) => scChangeMatches(e, c, tokens));
      if (changes.length > 0) entities.push({ ...e, changes });
    }
    if (entities.length > 0) {
      out.push({
        ...b,
        entities,
        change_count: entities.reduce((n, e) => n + e.changes.length, 0),
      });
    }
  }
  return out;
}

/** 把命中片段包成 mark，让人看清这行为什么留下来。 */
function Hl({ text, tokens }: { text: string; tokens: string[] }) {
  if (tokens.length === 0) return <>{text}</>;
  const lower = text.toLowerCase();
  const ranges: [number, number][] = [];
  for (const t of tokens) {
    for (let i = lower.indexOf(t); i >= 0; i = lower.indexOf(t, i + t.length)) {
      ranges.push([i, i + t.length]);
    }
  }
  if (ranges.length === 0) return <>{text}</>;
  ranges.sort((a, b) => a[0] - b[0]);

  const out: ReactNode[] = [];
  let cur = 0;
  for (const [s, e] of ranges) {
    if (s < cur) continue; // 关键词重叠时跳过已被覆盖的片段
    if (s > cur) out.push(text.slice(cur, s));
    out.push(
      <mark key={s} className="rounded bg-warning/70 px-0.5 text-foreground">
        {text.slice(s, e)}
      </mark>,
    );
    cur = e;
  }
  if (cur < text.length) out.push(text.slice(cur));
  return <>{out}</>;
}

/** 单个实体的变化块。 */
function EntityChangeBlock({ entity, tokens }: { entity: ScEntity; tokens: string[] }) {
  return (
    <div className="rounded-lg border border-border p-2.5">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <span className="font-mono text-sm">
          <span className="text-muted-foreground">
            <Hl text={`${entity.subject_type}:`} tokens={tokens} />
          </span>
          <span className="font-semibold text-foreground">
            <Hl text={entity.subject_id} tokens={tokens} />
          </span>
        </span>
        <span className="text-xs text-muted-foreground">{entity.changes.length} 条变化</span>
      </div>
      {/* 值可能很长（嵌套 JSON）：宁可换行也别截断 —— 省略号后面是什么永远看不到。 */}
      <ul className="space-y-1 pl-1">
        {entity.changes.map((c) => (
          <li key={c.id} className="flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5 text-sm">
            <OpBadge op={c.op} className="text-micro" />
            <span className="break-all font-mono text-foreground">
              <Hl text={c.path} tokens={tokens} />
            </span>
            <span className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-1 gap-y-0.5 font-mono">
              <span
                className="break-all text-muted-foreground line-through"
                title={fmtValue(c.before)}
              >
                {c.before === null || c.before === undefined ? (
                  fmtValue(c.before)
                ) : (
                  <Hl text={fmtValue(c.before)} tokens={tokens} />
                )}
              </span>
              <ArrowRight className="h-3 w-3 shrink-0 text-muted-foreground" />
              <span className="break-all font-medium" title={fmtValue(c.after)}>
                {c.after === null || c.after === undefined ? (
                  fmtValue(c.after)
                ) : (
                  <Hl text={fmtValue(c.after)} tokens={tokens} />
                )}
              </span>
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * 类型聚合块：默认折叠（只有一个实体时直接展开 —— 那还让用户多点一次没意义）。
 */
function TypeChangeBlock({ bucket, tokens }: { bucket: ScTypeBucket; tokens: string[] }) {
  const only = bucket.entities.length === 1;
  // 用户没手动点过时：单实体直接展开；过滤中一律展开 —— 搜到了还藏着让人再点一次没道理。
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const open = userOpen ?? (only || tokens.length > 0);
  return (
    <div className="rounded-lg border border-border/70">
      <button
        type="button"
        onClick={() => setUserOpen(!open)}
        aria-expanded={open}
        className="flex w-full flex-wrap items-center gap-x-2 px-2 py-1.5 text-left hover:bg-muted/40"
      >
        {open ? (
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
        )}
        <span className="font-mono text-sm font-semibold">
          {only ? (
            <>
              <Hl text={bucket.subject_type} tokens={tokens} />
              <span className="text-muted-foreground">:</span>
              <Hl text={bucket.entities[0]?.subject_id ?? ""} tokens={tokens} />
            </>
          ) : (
            <Hl text={bucket.subject_type} tokens={tokens} />
          )}
        </span>
        <span className="text-xs text-muted-foreground">
          {bucket.change_count} 条变化
          {!only && ` · ${bucket.entities.length} 个实体`}
        </span>
      </button>
      {open && (
        <div className="space-y-1 border-t border-border/70 p-1">
          {bucket.entities.map((e) => (
            <EntityChangeBlock key={e.key} entity={e} tokens={tokens} />
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * 本条消息自己造成的实体变化。
 *
 * 数据直接用事件自带的 analysis._state_changes —— 它就是「这条消息改了什么」的声明，
 * 不必再去拉整条操作链：那会把同一次操作里其它消息（请求 / 响应 / 后续推送）的变化一起倒进来，
 * 点一条消息却看到一堆跟它无关的行。
 */
function StateChangeDialog({
  sessionId,
  event,
  onClose,
}: {
  sessionId: string;
  event: DecodedEvent | null;
  onClose: () => void;
}) {
  const analysis = useMemo(() => (event ? analysisOf(event) : {}), [event]);
  const meta = useMemo(() => (event ? extractMeta(event.data, event.meta) : null), [event]);
  const sc = useMemo(() => {
    const raw = analysis._state_changes;
    return Array.isArray(raw) ? (raw as Record<string, unknown>[]) : [];
  }, [analysis]);
  const groups = useMemo(() => groupStateChanges(sc), [sc]);
  const entityCount = useMemo(() => groups.reduce((n, b) => n + b.entities.length, 0), [groups]);

  // 换一行就重置过滤词：上一行的关键词留到下一行只会让人以为搜不到。
  const [query, setQuery] = useState("");
  const eventId = event?.id;
  useEffect(() => {
    setQuery("");
  }, [eventId]);

  const tokens = useMemo(() => fuzzyTokens(query), [query]);
  const filtered = useMemo(() => filterChangeBuckets(groups, tokens), [groups, tokens]);
  const hitCount = useMemo(
    () => filtered.reduce((n, b) => n + b.change_count, 0),
    [filtered],
  );

  return (
    <Dialog
      open={!!event}
      onClose={onClose}
      title={
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-mono">{meta?.msgName || "(unknown)"}</span>
          <span className="text-sm font-normal text-muted-foreground">
            本次产生 {sc.length} 条状态变化 · {groups.length} 类 / {entityCount} 个实体
          </span>
        </span>
      }
      description={
        event ? (
          <span className="font-mono text-sm">
            {formatTimestamp(event.timestamp)}
            {event.correlation_id ? ` · ${event.correlation_id}` : ""}
          </span>
        ) : undefined
      }
      className="max-w-3xl"
    >
      <div className="mb-2 flex items-center gap-2">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="过滤实体 / 字段 / 值，空格分隔多个词"
            aria-label="过滤状态变更"
            className="h-9 pl-9 pr-9 text-sm"
          />
          {query && (
            <button
              type="button"
              onClick={() => setQuery("")}
              aria-label="清空过滤"
              className="absolute right-2 top-1/2 flex h-7 w-7 -translate-y-1/2 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
        {tokens.length > 0 && (
          <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
            命中 {hitCount} / {sc.length} 条
          </span>
        )}
        {/* 这里只有「这一条消息改了什么」；要看这个实体被整条会话改过来的全过程，去状态变更视图 */}
        <Button
          variant="outline"
          size="sm"
          className="h-9 shrink-0"
          onClick={() => navigate(sessionHref(sessionId, "states"))}
        >
          <Crosshair className="h-3.5 w-3.5" />
          在状态变更视图查看全部
        </Button>
      </div>

      {filtered.length === 0 ? (
        <EmptyState
          className="py-8"
          icon={<SearchX className="h-5 w-5" />}
          title="没有匹配的状态变化"
          hint="试试只输字段片段（如 hp）或实体 id；多个词是「同时满足」。"
          action={
            <Button variant="outline" size="sm" onClick={() => setQuery("")}>
              清空过滤
            </Button>
          }
        />
      ) : (
        <div className="space-y-1.5">
          {filtered.map((b) => (
            <TypeChangeBlock key={b.subject_type} bucket={b} tokens={tokens} />
          ))}
        </div>
      )}
    </Dialog>
  );
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
          配对记录指向消息 <b className="font-mono text-foreground">{event.causation_id}</b>
          ，但本会话里已经查不到它（可能被删除或重新解码过）。
        </span>
      )}
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
          <Badge variant="info" size="micro">
            请求
          </Badge>
          <span className="ml-auto shrink-0 font-mono text-micro text-muted-foreground">
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
                <Badge variant="success" size="micro">
                  响应
                </Badge>
                <span className="ml-auto shrink-0 font-mono text-micro text-muted-foreground">
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

/** 「GameTrace 分析」：实体、状态变更、correlation、配对聚合区。 */
function AnalysisPanel({
  event,
  partners,
  analysis,
  onJumpToPartner,
}: {
  event: DecodedEvent;
  partners: DecodedEvent[];
  analysis: Record<string, unknown>;
  onJumpToPartner: (id: string) => void;
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

  return (
    <div className="space-y-2">
      <PairPanel
        event={event}
        partners={partners}
        onJumpToPartner={onJumpToPartner}
      />

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
      {/* annotate 标签在这里也要出现：展开后是读一条消息的主视图，
          只显示消息名会让人以为语义只在收起态的行里有。 */}
      {meta.semantic.map((s) => (
        <SemanticBadge key={s} label={s} />
      ))}
      <span className="font-mono text-xs text-muted-foreground">
        {formatTimestamp(event.timestamp)}
      </span>
      {event.protocol && (
        <Badge variant="muted" size="micro">{event.protocol}</Badge>
      )}
      <span className="text-xs text-muted-foreground">{formatSize(event.raw_len)}</span>
      {event.capture && (
        <span
          className="rounded bg-muted/60 px-1.5 py-0.5 font-mono text-2xs text-muted-foreground"
          title={`连接 ${event.capture.conn_id} · 流 ${event.capture.stream_id} · 来源 ${event.capture.source || ""}`}
        >
          {event.capture.captured_by || "Proxy"} · C#{String(event.capture.conn_seq).padStart(3, "0")} · S#
          {event.capture.stream_seq}
        </span>
      )}
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
  sessionId,
  partners,
  colSpan,
  onJumpToPartner,
  onCollapse,
}: {
  event: DecodedEvent;
  sessionId: string;
  partners: DecodedEvent[];
  colSpan: number;
  onJumpToPartner: (id: string) => void;
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

  // 配对组：页内伙伴先顶上，缺的成员按血缘向服务端补查一次（详见 lib/pair-group.ts）。
  const groupFilter = useMemo(() => pairGroupFilter(event), [event]);
  const { data: group } = usePairGroup(sessionId, groupFilter);
  const pool = useMemo(
    () => mergePartnerPool(event, partners, group?.events),
    [event, partners, group],
  );
  const { request, responses } = useMemo(() => resolvePairGroup(event, pool), [event, pool]);
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
              partners={pool}
              analysis={classes.analysis}
              onJumpToPartner={onJumpToPartner}
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
  sessionId,
  partners,
  isExpanded,
  isHighlighted,
  onToggle,
  onJumpToPartner,
  onCollapse,
  onOpenStateChange,
}: {
  event: DecodedEvent;
  sessionId: string;
  partners: DecodedEvent[];
  isExpanded: boolean;
  isHighlighted: boolean;
  onToggle: (id: string) => void;
  onJumpToPartner: (id: string) => void;
  onCollapse: (id: string) => void;
  onOpenStateChange: (event: DecodedEvent) => void;
}) {
  const meta = useMemo(() => extractMeta(event.data, event.meta), [event.data, event.meta]);
  const summary = useMemo(() => summarizePayload(event.data, meta), [event.data, meta]);
  const changeCount = useMemo(() => stateChangeCount(event), [event]);
  const colSpan = 6;

  return (
    <Fragment key={event.id}>
      <TableRow
        id={`event-row-${event.id}`}
        className={`cursor-pointer transition-colors ${isHighlighted ? "bg-primary/10" : ""} ${isExpanded ? "bg-muted/40" : ""}`}
        onClick={() => onToggle(event.id)}
        aria-expanded={isExpanded}
      >
        {/* 展开指示：没有它用户看不出行是可点的 */}
        <TableCell className="w-8 py-2 pl-2 pr-0 text-muted-foreground">
          <ChevronRight
            className={`h-3.5 w-3.5 transition-transform ${isExpanded ? "rotate-90" : ""}`}
          />
        </TableCell>

        {/* 时间 */}
        <TableCell className="w-28 py-2 font-mono text-xs whitespace-nowrap tabular-nums">
          {formatTimestamp(event.timestamp)}
        </TableCell>

        {/* 消息名：方向文字 + 名称 + 语义标签 + 配对角标 */}
        <TableCell className="min-w-[220px] max-w-[340px] py-2">
          <div className="flex items-center gap-1.5 min-w-0">
            <DirectionChip direction={meta.direction} />
            <MessageCell msgName={meta.msgName} semantic={meta.semantic} />
            {partners.length > 0 && !isExpanded && (
              <span className="ml-0.5 shrink-0 inline-flex items-center text-muted-foreground" title="已配对请求/响应">
                <Link2 className="h-3 w-3" />
              </span>
            )}
          </div>
        </TableCell>

        {/* Payload 摘要：业务字段值突出展示，是行的视觉锚点 */}
        <TableCell className="max-w-[28rem] py-2">
          <SummaryCell parts={summary} />
        </TableCell>

        {/* 原始包大小 */}
        <TableCell className="w-16 py-2 text-right tabular-nums text-xs text-muted-foreground whitespace-nowrap">
          {formatSize(event.raw_len)}
        </TableCell>

        {/* 状态变更入口：只有真正产生了实体变化的消息才有按钮 */}
        <TableCell className="w-16 py-2 text-right whitespace-nowrap">
          {changeCount > 0 ? (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                onOpenStateChange(event);
              }}
              title={`查看这条消息产生的 ${changeCount} 条实体状态变化`}
              aria-label={`查看这条消息产生的 ${changeCount} 条实体状态变化`}
              className="inline-flex items-center gap-1 rounded-md border border-primary/30 bg-primary/10 px-1.5 py-0.5 text-micro font-medium tabular-nums text-primary hover:bg-primary/20"
            >
              <Activity className="h-3 w-3" />
              {changeCount}
            </button>
          ) : (
            <span className="text-micro text-muted-foreground" aria-hidden="true">
              —
            </span>
          )}
        </TableCell>
      </TableRow>

      {isExpanded && (
        <ExpandedRow
          event={event}
          sessionId={sessionId}
          partners={partners}
          colSpan={colSpan}
          onJumpToPartner={onJumpToPartner}
          onCollapse={() => onCollapse(event.id)}
        />
      )}
    </Fragment>
  );
});

// ─── 主表格组件 ───────────────────────────────────────────────

export function EventTable({
  sessionId,
  query,
  exclude,
  direction,
  semantics,
  connFilter,
  focusEventId,
  onExitFocus,
}: EventTableProps) {
  const [page, setPage] = useState<number>(0);
  const [pageSize, setPageSize] = useState<number>(PAGE_SIZES[0]!);
  // 允许多行同时展开：对比请求/响应时不用来回点，这是最常见的阅读动作。
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const [highlightId, setHighlightId] = useState<string | null>(null);
  // 从事件表直接看实体变化：整条事件留在手边，弹窗只渲染它自带的变更，不用再查一次。
  const [scEvent, setScEvent] = useState<DecodedEvent | null>(null);

  const focus = focusEventId ?? null;

  useEffect(() => {
    setPage(0);
    setExpandedIds(new Set());
  }, [sessionId, query, exclude, direction, semantics, connFilter]);

  // 定位模式：跳过来的人要看的正是这条消息的 payload，不该还让他再点一次；
  // 退出定位时清掉展开集，否则会带着一个来自上一次的展开回到完整列表。
  useEffect(() => {
    if (!focus) {
      setExpandedIds(new Set());
      return;
    }
    setExpandedIds(new Set([focus]));
  }, [focus]);

  const offset = page * pageSize;

  // 有查询词、剔除词、方向过滤或连接过滤时：一次性拉取较大批次在前端内存过滤
  // （保证搜索跨页完整不遗漏）；无过滤条件时：保持原有分页拉取。
  const useLargeLimit = !!query || !!exclude || !!direction || !!connFilter;
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
  } = useDecodedData(
    sessionId,
    focus
      ? { limit: 1, offset: 0, filter: `id == ${JSON.stringify(focus)}` }
      : {
          limit: effectiveLimit,
          offset: effectiveOffset,
          connId: connFilter?.conn_id ?? null,
          // 语义标签由服务端 list_decoded_data 过滤（meta.semantic 数组成员匹配，多值「或」），
          // 保持正常分页与精确 total_matched，不参与前端内存过滤。
          semantics: semantics.length ? semantics : undefined,
        },
  );

  const events = useMemo(() => data?.events ?? [], [data]);
  const totalMatched = data?.total_matched ?? 0;

  // 前端过滤：搜索/剔除/方向/连接过滤态在已拉取的批次上过滤（AND）。
  // 剔除与搜索看同一份字段包，但多值之间是「或」：命中任一词就把这条隐藏。
  // 定位模式下服务端已经精确取到那一条，再套一层前端过滤只会把落点滤没。
  const filteredEvents = useMemo(() => {
    if (focus) return events;
    if (!query && !exclude && !direction && !connFilter) return events;
    return events.filter((e) => {
      if (query && !eventMatchesQuery(e, query)) return false;
      if (exclude && eventExcludesQuery(e, exclude)) return false;
      if (direction && !eventMatchesDirection(e, direction)) return false;
      if (connFilter && !eventMatchesConnection(e, connFilter)) return false;
      return true;
    });
  }, [events, focus, query, exclude, direction, connFilter]);

  const filtering = !!focus || !!query || !!exclude || !!direction || !!connFilter;

  const totalPages = Math.ceil(totalMatched / pageSize);

  /**
   * 配对索引：事件 id → 已取回批次内的配对伙伴。
   * 配对信号是 causation_id（响应 → 请求事件 id，SDK pair 规则写入），
   * 不用 correlation_id —— 后者可能是插件 decode 侧写的流键（全流共享），不是配对关系。
   * 建索引用整批取回的事件而不是过滤后的：过滤把请求那条筛掉时，同页的响应照样配得上，
   * 没必要再为它打一次服务端血缘查询。
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

  /** 展开并滚动定位到某事件（配对伙伴跳转）。 */
  function handleJumpTo(id: string) {
    // 页内没有这一行就直说：把它记进展开集只会得到一个永远渲染不出来的目标。
    if (!document.getElementById(`event-row-${id}`)) {
      toast.info("配对消息不在当前列表", "展开的这一行已经并排显示了它；要跳到它本身，用上方模糊搜索。");
      return;
    }
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
    if (focus) {
      return (
        <EmptyState
          icon={<Crosshair className="h-5 w-5" />}
          title="定位的消息不在本会话"
          hint={`会话里找不到 id 为 ${focus} 的事件，它可能已被删除或属于另一次抓包。`}
          className="h-64 justify-center"
          action={
            <Button variant="outline" size="sm" onClick={onExitFocus}>
              返回全部事件
            </Button>
          }
        />
      );
    }
    return (
      <EmptyState
        icon={filtering || totalMatched === 0 ? <SearchX className="h-5 w-5" /> : <Table2 className="h-5 w-5" />}
        title={filtering ? "无匹配结果" : totalMatched === 0 ? "暂无解码数据" : "无数据"}
        hint={
          filtering
            ? "没有事件命中当前过滤条件，尝试更换或清除搜索 / 剔除 / 方向过滤条件。"
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

      {/* 定位模式横幅：说明这一行是从哪来的，以及怎么回到正常列表 */}
      {focus && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2 text-xs">
          <Crosshair className="h-3.5 w-3.5 shrink-0 text-primary" />
          <span className="min-w-0 truncate text-muted-foreground">
            定位到消息 <b className="font-mono text-foreground">{focus}</b> · 来自状态变更视图
          </span>
          <Button variant="outline" size="sm" className="ml-auto h-7" onClick={onExitFocus}>
            返回全部事件
          </Button>
        </div>
      )}

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
              <span className="text-micro text-muted-foreground">点击行展开完整 JSON</span>
              <label className="flex items-center gap-1">
                <span className="text-micro text-muted-foreground">每页</span>
                <Select
                  value={pageSize}
                  onChange={(e) => {
                    setPageSize(Number(e.target.value));
                    setPage(0);
                    setExpandedIds(new Set());
                  }}
                  size="micro"
                  aria-label="每页条数"
                >
                  {PAGE_SIZES.map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </Select>
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
            <TableHead>摘要</TableHead>
            <TableHead className="w-16 text-right">大小</TableHead>
            <TableHead className="w-16 text-right">状态变更</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filteredEvents.map((event: DecodedEvent) => (
            <EventRow
              key={event.id}
              event={event}
              sessionId={sessionId}
              partners={partnersMap.get(event.id) ?? []}
              isExpanded={expandedIds.has(event.id)}
              isHighlighted={highlightId === event.id || (!!focus && event.id === focus)}
              onToggle={handleToggleExpand}
              onJumpToPartner={handleJumpTo}
              onCollapse={handleCollapse}
              onOpenStateChange={(ev) => setScEvent(ev)}
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

      {/* 实体状态变化弹窗：只展示这条消息自己产生的变化 */}
      <StateChangeDialog sessionId={sessionId} event={scEvent} onClose={() => setScEvent(null)} />
    </div>
  );
}
