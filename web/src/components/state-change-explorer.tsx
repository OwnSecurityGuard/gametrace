// StateChangeExplorer — 协议数据页「状态变更」子视图（三视图统一查询）。
//
// 三种视图（按操作 / 按实体 / 按时间）共用后端一次查询返回的同一份 changes：
//   - 按操作：操作（发出去的协议）→ 协议连（请求/响应/推送）→ 实体 → 字段变化
//   - 按实体：实体 → 操作/事件 → 多次字段变化（含完整历史入口）
//   - 按时间：时间段 → 操作 → 实体变化（观察密度与先后顺序）
// 切换视图只是换渲染维度，不重新查询；跨视图跳转通过 focus + 锚点联动。
import { useEffect, useMemo, useState } from "react";
import { useStateChanges, useStateChangeDetail } from "@/hooks/use-mcp";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Dialog } from "@/components/ui/dialog";
import { OpBadge, HighlightedJson } from "@/lib/event-display";
import { changeMatchesQuery, changeMatchesDirection, changeMatchesConnection, type DirectionFilter } from "@/lib/fuzzy";
import { beforeKind } from "@/lib/state-change-display";
import { cn } from "@/lib/utils";
import type { ConnectionSummary } from "@/types/connection";
import {
  Crosshair,
  ArrowRight,
  ChevronRight,
  ChevronDown,
  Clock,
  History,
  ListTree,
  Network,
  RotateCw,
  Table2,
  TableProperties,
  Timer,
} from "lucide-react";
import type {
  AnchorKind,
  Change,
  EntityGroup,
  MessageKind,
  MessageRef,
  OperationGroup,
  SortBy,
  TimeBucket,
} from "@/types/state-change";

interface StateChangeExplorerProps {
  sessionId: string | null;
  /** 模糊查询关键词；非空时在 changes 流上前端过滤并显示扁平命中列表。 */
  query?: string;
  /** 消息方向过滤（C→S / S→C，空 = 全部）；与 query 叠加为 AND。 */
  direction?: DirectionFilter;
  /** 连接过滤（null = 全部连接）；按变更携带的 conn_id/flow_id 匹配，与 query/direction 叠加为 AND。 */
  connFilter?: ConnectionSummary | null;
  /** 跳到协议事件视图并定位这条消息（协议 ↔ 状态变更 的反向落点）。 */
  onOpenEvent?: (eventId: string) => void;
}

type ViewId = "operation" | "entity" | "time";

const VIEWS: { id: ViewId; label: string; hint: string; icon: typeof ListTree }[] = [
  { id: "operation", label: "按操作", hint: "操作 → 协议连 → 实体 → 字段变化", icon: ListTree },
  { id: "entity", label: "按实体", hint: "实体 → 操作/事件 → 多次字段变化", icon: Network },
  { id: "time", label: "按时间", hint: "时间段 → 操作 → 实体变化（密度与顺序）", icon: Timer },
];

const SORTS: { id: SortBy; label: string }[] = [
  { id: "first_change", label: "首次变化" },
  { id: "time", label: "发生时间" },
  { id: "change_count", label: "变化数量" },
];

const ZERO_TIME = "0001-01-01T00:00:00Z";

// ===== 展示工具 =====

/** 相对时间：T+220ms / T-1.20s。锚点即 T0。 */
function relTime(ms: number): string {
  if (ms === 0) return "T±0ms";
  const sign = ms < 0 ? "-" : "+";
  const abs = Math.abs(ms);
  if (abs < 1000) return `T${sign}${abs}ms`;
  return `T${sign}${(abs / 1000).toFixed(abs < 10_000 ? 2 : 1)}s`;
}

/** 绝对时间（后端零值时间视为无）。 */
function absTime(iso?: string): string {
  if (!iso || iso === ZERO_TIME) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }) + "." + String(d.getMilliseconds()).padStart(3, "0");
}

function compactJson(value: unknown): string {
  if (value === undefined || value === null) return "∅";
  try {
    const s = typeof value === "string" ? value : JSON.stringify(value);
    if (s.length <= 32) return s;
    return `${s.slice(0, 29)}…`;
  } catch {
    return String(value);
  }
}

/** 逗号/空格分隔的输入 → 数组。 */
function parseList(v: string): string[] {
  return v
    .split(/[,\s]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

const KIND_STYLE: Record<string, string> = {
  request: "bg-sky-50 text-sky-700 dark:bg-sky-950 dark:text-sky-300",
  response: "bg-violet-50 text-violet-700 dark:bg-violet-950 dark:text-violet-300",
  push: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300",
  unknown: "bg-muted text-muted-foreground",
};

const KIND_LABEL: Record<string, string> = {
  request: "请求",
  response: "响应",
  push: "推送",
  unknown: "未知",
};

function KindBadge({ kind }: { kind: MessageKind | string }) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded px-1.5 py-0.5 text-[10px] font-medium",
        KIND_STYLE[kind] ?? KIND_STYLE.unknown,
      )}
    >
      {KIND_LABEL[kind] ?? kind}
    </span>
  );
}

/** 相对/绝对时间切换下统一的时间单元。 */
function TimeLabel({
  offsetMs,
  iso,
  mode,
  className,
}: {
  offsetMs: number;
  iso?: string;
  mode: "relative" | "absolute";
  className?: string;
}) {
  const text = mode === "relative" ? relTime(offsetMs) : absTime(iso);
  return (
    <span
      className={cn("font-mono text-[11px] tabular-nums", className)}
      title={mode === "relative" ? absTime(iso) : relTime(offsetMs)}
    >
      {text}
    </span>
  );
}

// ===== 主组件 =====

export function StateChangeExplorer({
  sessionId,
  query,
  direction,
  connFilter,
  onOpenEvent,
}: StateChangeExplorerProps) {
  const [view, setView] = useState<ViewId>("operation");
  const [anchorKind, setAnchorKind] = useState<AnchorKind>("");
  const [anchorId, setAnchorId] = useState("");
  const [beforeMs, setBeforeMs] = useState(0);
  const [afterMs, setAfterMs] = useState(3000);
  const [sortBy, setSortBy] = useState<SortBy>("first_change");
  const [typeFilter, setTypeFilter] = useState("");
  const [pathFilter, setPathFilter] = useState("");
  const [opFilter, setOpFilter] = useState("");
  const [timeMode, setTimeMode] = useState<"relative" | "absolute">("relative");
  // 跨视图跳转的落点（高亮 + 滚动），不改变查询本身。
  const [focus, setFocus] = useState<{ op?: string; entity?: string }>({});
  const [detail, setDetail] = useState<{ changeId?: string; eventId?: string; entity?: string; path?: string } | null>(null);

  const params = useMemo(
    () => ({
      anchorType: anchorKind,
      anchorId: anchorId || undefined,
      // 无锚点时窗口没有原点，传了也会被后端忽略——这里直接归零避免误导。
      windowBeforeMs: anchorKind ? beforeMs : 0,
      windowAfterMs: anchorKind ? afterMs : 0,
      // 后端一次返回全部四种分组，视图切换只换渲染维度——group_by 固定，避免无谓重查。
      groupBy: "operation" as const,
      sortBy,
      subjectTypes: parseList(typeFilter),
      paths: parseList(pathFilter),
      ops: parseList(opFilter),
    }),
    [anchorKind, anchorId, beforeMs, afterMs, sortBy, typeFilter, pathFilter, opFilter],
  );

  const { data, isLoading, isError, error, refetch, isFetching } = useStateChanges(sessionId, params);

  const changes = data?.changes ?? [];
  const hasFilter = !!query || !!direction || !!connFilter;
  // 前端过滤：搜索/方向/连接过滤时只展示命中的扁平变更；无过滤时走原有三视图。
  const matchedChanges = useMemo(
    () =>
      hasFilter
        ? changes.filter((c) => {
            if (query && !changeMatchesQuery(c, query)) return false;
            if (direction && !changeMatchesDirection(c, direction)) return false;
            if (connFilter && !changeMatchesConnection(c, connFilter)) return false;
            return true;
          })
        : changes,
    [changes, hasFilter, query, direction, connFilter],
  );

  // 锚点候选与过滤候选随查询结果累积：不必额外拉全量列表，
  // 也不用要求用户先记住 Power / value / set 这些协议词才敢下过滤条件。
  const [catalog, setCatalog] = useState<{
    ops: { key: string; label: string }[];
    entities: string[];
    subjectTypes: string[];
    paths: string[];
    changeOps: string[];
  }>({ ops: [], entities: [], subjectTypes: [], paths: [], changeOps: [] });
  useEffect(() => {
    if (!data) return;
    setCatalog((prev) => {
      const ops = [...prev.ops];
      for (const op of data.operations ?? []) {
        if (!ops.some((o) => o.key === op.key)) ops.push({ key: op.key, label: op.label });
      }
      const entities = [...prev.entities];
      for (const e of data.entities ?? []) {
        if (!entities.includes(e.key)) entities.push(e.key);
      }
      const add = (arr: string[], v: string) => {
        if (v && !arr.includes(v)) arr.push(v);
      };
      const subjectTypes = [...prev.subjectTypes];
      const paths = [...prev.paths];
      const changeOps = [...prev.changeOps];
      for (const c of data.changes ?? []) {
        add(subjectTypes, c.subject_type);
        add(paths, c.path);
        add(changeOps, c.op);
      }
      subjectTypes.sort();
      paths.sort();
      changeOps.sort();
      return { ops, entities, subjectTypes, paths, changeOps };
    });
  }, [data]);

  // 跳转后滚动到落点。
  useEffect(() => {
    if (!focus.op && !focus.entity) return;
    const id = focus.op ? `sc-op-${focus.op}` : `sc-ent-${focus.entity}`;
    const el = document.getElementById(id);
    el?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [focus, view, data]);

  if (!sessionId) {
    return (
      <EmptyState
        icon={<TableProperties className="h-5 w-5" />}
        title="未选择会话"
        hint="在左侧会话列表中选择一个会话以分析实体状态变更。"
        className="h-64 justify-center"
      />
    );
  }

  return (
    <div className="flex h-full flex-col gap-3">
      {/* 查询/方向过滤态只做扁平过滤，隐藏锚点/窗口/排序等高级筛选提干扰 */}
      {!hasFilter && (
        <QueryBar
          anchorKind={anchorKind}
          onAnchorKind={(k) => {
            setAnchorKind(k);
            setAnchorId("");
          }}
          anchorId={anchorId}
          onAnchorId={setAnchorId}
          catalog={catalog}
          beforeMs={beforeMs}
          afterMs={afterMs}
          onBeforeMs={setBeforeMs}
          onAfterMs={setAfterMs}
          sortBy={sortBy}
          onSortBy={setSortBy}
          typeFilter={typeFilter}
          onTypeFilter={setTypeFilter}
          pathFilter={pathFilter}
          onPathFilter={setPathFilter}
          opFilter={opFilter}
          onOpFilter={setOpFilter}
          timeMode={timeMode}
          onTimeMode={setTimeMode}
          onReset={() => {
            setAnchorKind("");
            setAnchorId("");
            setBeforeMs(0);
            setAfterMs(3000);
            setSortBy("first_change");
            setTypeFilter("");
            setPathFilter("");
            setOpFilter("");
            setFocus({});
          }}
          onRefresh={() => void refetch()}
          busy={isFetching}
        />
      )}

      {/* 三视图切换：共用同一份数据，只是换渲染维度（过滤态隐藏） */}
      {!hasFilter && (
        <div
          role="tablist"
          aria-label="状态变更视图"
          className="inline-flex w-fit items-center gap-1 rounded-lg bg-muted p-1"
        >
          {VIEWS.map(({ id, label, hint, icon: Icon }) => (
            <button
              key={id}
              role="tab"
              aria-selected={view === id}
              title={hint}
              onClick={() => setView(id)}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
                view === id ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              {label}
            </button>
          ))}
        </div>
      )}

      {isLoading && (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-20 w-full" />
          ))}
        </div>
      )}

      {isError && (
        <div
          role="alert"
          className="flex items-center gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
        >
          <span className="flex-1">状态变更加载失败：{error?.message ?? "未知错误"}</span>
          <Button variant="outline" size="sm" onClick={() => void refetch()} className="h-7">
            <RotateCw className="h-3.5 w-3.5" />
            重试
          </Button>
        </div>
      )}

      {data && changes.length === 0 && (
        <EmptyState
          icon={<TableProperties className="h-5 w-5" />}
          title="暂无匹配的状态变更"
          hint={
            data.anchor?.note ||
            "插件需在事件 payload 中声明 _state_changes 才会产生状态变更投影；也可放宽窗口或清除过滤条件。"
          }
          className="h-56 justify-center"
        />
      )}

      {/* 过滤态：扁平命中变更列表 */}
      {hasFilter && changes.length > 0 && (
        <div className="space-y-2">
          <div className="px-1 text-xs text-muted-foreground" aria-live="polite">
            命中 <b className="font-semibold text-foreground">{matchedChanges.length}</b> 条状态变更
          </div>
          {matchedChanges.length === 0 ? (
            <EmptyState
              icon={<TableProperties className="h-5 w-5" />}
              title="无匹配状态变更"
              hint="没有变更命中当前过滤条件，尝试更换或清除模糊搜索条件 / 方向过滤。"
              className="h-56 justify-center"
            />
          ) : (
            <div className="space-y-2">
              {matchedChanges.map((c) => (
                <div key={c.id} className="rounded-lg border border-border bg-card p-2">
                  <ChangeRow
                    change={c}
                    timeMode={timeMode}
                    onDetail={(d) => setDetail({ ...d })}
                  />
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* 非过滤态：三视图 */}
      {!hasFilter && data && changes.length > 0 && (
        <>
          <SummaryBar data={data} />
          {view === "operation" && (
            <OperationView
              groups={data.operations ?? []}
              focus={focus}
              timeMode={timeMode}
              onFocusEntity={(key) => {
                setFocus({ entity: key });
                setView("entity");
              }}
              onAnchor={(id) => {
                setAnchorKind("operation");
                setAnchorId(id);
              }}
              onDetail={(d) => setDetail(d)}
            />
          )}
          {view === "entity" && (
            <EntityView
              groups={data.entities ?? []}
              focus={focus}
              timeMode={timeMode}
              onFocusOperation={(key) => {
                setFocus({ op: key });
                setView("operation");
              }}
              onAnchor={(key) => {
                setAnchorKind("entity");
                setAnchorId(key);
              }}
              onDetail={(d) => setDetail(d)}
            />
          )}
          {view === "time" && (
            <TimeView
              buckets={data.buckets ?? []}
              timeMode={timeMode}
              onFocusOperation={(key) => {
                setFocus({ op: key });
                setView("operation");
              }}
              onFocusEntity={(key) => {
                setFocus({ entity: key });
                setView("entity");
              }}
            />
          )}
        </>
      )}

      <DetailDialog
        sessionId={sessionId}
        detail={detail}
        onClose={() => setDetail(null)}
        timeMode={timeMode}
        onOpenEvent={onOpenEvent}
      />
    </div>
  );
}

// ===== 查询栏 =====

interface QueryBarProps {
  anchorKind: AnchorKind;
  onAnchorKind: (k: AnchorKind) => void;
  anchorId: string;
  onAnchorId: (v: string) => void;
  catalog: {
    ops: { key: string; label: string }[];
    entities: string[];
    subjectTypes: string[];
    paths: string[];
    changeOps: string[];
  };
  beforeMs: number;
  afterMs: number;
  onBeforeMs: (v: number) => void;
  onAfterMs: (v: number) => void;
  sortBy: SortBy;
  onSortBy: (v: SortBy) => void;
  typeFilter: string;
  onTypeFilter: (v: string) => void;
  pathFilter: string;
  onPathFilter: (v: string) => void;
  opFilter: string;
  onOpFilter: (v: string) => void;
  timeMode: "relative" | "absolute";
  onTimeMode: (v: "relative" | "absolute") => void;
  onReset: () => void;
  onRefresh: () => void;
  busy: boolean;
}

function QueryBar(p: QueryBarProps) {
  const anchored = p.anchorKind !== "";
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card/60 p-2 text-xs">
      <span className="inline-flex items-center gap-1 text-muted-foreground">
        <Crosshair className="h-3.5 w-3.5" />
        锚点
      </span>
      <select
        className="h-7 rounded-md border border-border bg-background px-1.5 text-xs"
        value={p.anchorKind}
        onChange={(e) => p.onAnchorKind(e.target.value as AnchorKind)}
        aria-label="锚点类型"
      >
        <option value="">全会话</option>
        <option value="operation">操作</option>
        <option value="entity">实体</option>
        <option value="event">事件</option>
      </select>

      {p.anchorKind === "operation" && p.catalog.ops.length > 0 && (
        <select
          className="h-7 max-w-[16rem] rounded-md border border-border bg-background px-1.5 text-xs"
          value={p.anchorId}
          onChange={(e) => p.onAnchorId(e.target.value)}
          aria-label="选择操作"
        >
          <option value="">选择操作…</option>
          {p.catalog.ops.map((o) => (
            <option key={o.key} value={o.key}>
              {o.label}
            </option>
          ))}
        </select>
      )}
      {p.anchorKind === "entity" && p.catalog.entities.length > 0 && (
        <select
          className="h-7 max-w-[16rem] rounded-md border border-border bg-background px-1.5 text-xs"
          value={p.anchorId}
          onChange={(e) => p.onAnchorId(e.target.value)}
          aria-label="选择实体"
        >
          <option value="">选择实体…</option>
          {p.catalog.entities.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
      )}
      {((p.anchorKind === "operation" && p.catalog.ops.length === 0) ||
        p.anchorKind === "event" ||
        (p.anchorKind === "entity" && p.catalog.entities.length === 0)) && (
        <input
          className="h-7 w-56 rounded-md border border-border bg-background px-2 font-mono text-xs"
          placeholder={
            p.anchorKind === "entity" ? "subject_type:subject_id" : "event_id 或 correlation_id"
          }
          value={p.anchorId}
          onChange={(e) => p.onAnchorId(e.target.value)}
          aria-label="锚点 ID"
        />
      )}

      <span className="ml-1 inline-flex items-center gap-1 text-muted-foreground" title="相对锚点的时间窗口">
        <Clock className="h-3.5 w-3.5" />
        窗口
      </span>
      <label className="inline-flex items-center gap-1">
        前
        <input
          type="number"
          min={0}
          disabled={!anchored}
          className="h-7 w-20 rounded-md border border-border bg-background px-1.5 text-xs disabled:opacity-50"
          value={p.beforeMs}
          onChange={(e) => p.onBeforeMs(Number(e.target.value) || 0)}
          aria-label="窗口前 N 毫秒"
        />
        ms
      </label>
      <label className="inline-flex items-center gap-1">
        后
        <input
          type="number"
          min={0}
          disabled={!anchored}
          className="h-7 w-20 rounded-md border border-border bg-background px-1.5 text-xs disabled:opacity-50"
          value={p.afterMs}
          onChange={(e) => p.onAfterMs(Number(e.target.value) || 0)}
          aria-label="窗口后 N 毫秒"
        />
        ms
      </label>

      <span className="ml-1 text-muted-foreground">排序</span>
      <select
        className="h-7 rounded-md border border-border bg-background px-1.5 text-xs"
        value={p.sortBy}
        onChange={(e) => p.onSortBy(e.target.value as SortBy)}
        aria-label="排序方式"
      >
        {SORTS.map((s) => (
          <option key={s.id} value={s.id}>
            {s.label}
          </option>
        ))}
      </select>

      <span className="ml-1 text-muted-foreground">过滤</span>
      <input
        list="sc-suggest-types"
        className="h-7 w-28 rounded-md border border-border bg-background px-1.5 text-xs"
        placeholder="实体类型"
        value={p.typeFilter}
        onChange={(e) => p.onTypeFilter(e.target.value)}
        aria-label="按实体类型过滤"
        title="从当前查询结果自动收集候选值，逗号分隔可填多个"
      />
      <datalist id="sc-suggest-types">
        {p.catalog.subjectTypes.map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>
      <input
        list="sc-suggest-paths"
        className="h-7 w-28 rounded-md border border-border bg-background px-1.5 text-xs"
        placeholder="字段路径"
        value={p.pathFilter}
        onChange={(e) => p.onPathFilter(e.target.value)}
        aria-label="按字段路径过滤"
        title="从当前查询结果自动收集候选值，逗号分隔可填多个"
      />
      <datalist id="sc-suggest-paths">
        {p.catalog.paths.map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>
      <input
        list="sc-suggest-ops"
        className="h-7 w-24 rounded-md border border-border bg-background px-1.5 text-xs"
        placeholder="变化类型"
        value={p.opFilter}
        onChange={(e) => p.onOpFilter(e.target.value)}
        aria-label="按变化类型过滤"
        title="从当前查询结果自动收集候选值（set / merge / delete），逗号分隔可填多个"
      />
      <datalist id="sc-suggest-ops">
        {p.catalog.changeOps.map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>

      <div className="ml-auto flex items-center gap-2">
        <button
          type="button"
          onClick={() => p.onTimeMode(p.timeMode === "relative" ? "absolute" : "relative")}
          className="inline-flex h-7 items-center gap-1 rounded-md border border-border px-2 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground"
          title="切换相对时间（T+220ms）与绝对时间"
        >
          {p.timeMode === "relative" ? "相对时间" : "绝对时间"}
        </button>
        <Button variant="outline" size="sm" className="h-7" onClick={p.onReset}>
          重置
        </Button>
        <Button variant="outline" size="sm" className="h-7" onClick={p.onRefresh} disabled={p.busy}>
          <RotateCw className={cn("h-3.5 w-3.5", p.busy && "animate-spin")} />
          刷新
        </Button>
      </div>
    </div>
  );
}

// ===== 概览条 =====

function SummaryBar({ data }: { data: { summary: { change_count: number; entity_count: number; field_count: number; operation_count: number }; anchor?: { resolved: boolean; t0: string; note?: string }; window: { before_ms: number; after_ms: number }; truncated: boolean } }) {
  const s = data.summary;
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 px-1 text-xs text-muted-foreground" aria-live="polite">
      <span
        className="tabular-nums"
        title="概览统计的是本次查询结果的全部变更；下方每个组卡片里的计数只统计该组"
      >
        <b className="font-semibold text-foreground">{s.change_count}</b> 条变化 ·{" "}
        <b className="font-semibold text-foreground">{s.entity_count}</b> 个实体 ·{" "}
        <b className="font-semibold text-foreground">{s.field_count}</b> 种字段 ·{" "}
        <b className="font-semibold text-foreground">{s.operation_count}</b> 次操作
      </span>
      {data.anchor?.resolved && (
        <span>T0 = {absTime(data.anchor.t0)}</span>
      )}
      {(data.window.before_ms > 0 || data.window.after_ms > 0) && (
        <span>
          窗口 −{data.window.before_ms}ms / +{data.window.after_ms}ms
        </span>
      )}
      {data.truncated && <span className="text-amber-600">结果已截断（提高 limit 查看全部）</span>}
      {data.anchor?.note && <span className="text-amber-600">{data.anchor.note}</span>}
    </div>
  );
}

// ===== 按操作 =====

interface ViewHandlers {
  focus: { op?: string; entity?: string };
  timeMode: "relative" | "absolute";
  onAnchor: (id: string) => void;
  onDetail: (d: { changeId?: string; eventId?: string; entity?: string; path?: string }) => void;
}

/** 组内主导实体：本次操作里改动最多的那个，拿它当组头的业务主语。 */
function leadEntity(g: OperationGroup): EntityGroup | undefined {
  if (!g.entities?.length) return undefined;
  return g.entities.reduce((a, b) => (b.change_count > a.change_count ? b : a));
}

/** 同类型实体的聚合桶：10 个 Power:1001-* 先收成一行，点开才列出具体实体。 */
interface EntityTypeBucket {
  subject_type: string;
  /** 该类型下的具体实体，按变化数降序。 */
  entities: EntityGroup[];
  entity_count: number;
  change_count: number;
  /** 该类型涉及的全部字段路径（去重，保持首次出现顺序）。 */
  field_paths: string[];
  first_offset_ms: number;
  last_offset_ms: number;
  first_change?: string;
  last_change?: string;
}

function groupEntitiesByType(entities: EntityGroup[]): EntityTypeBucket[] {
  const byType = new Map<string, EntityTypeBucket>();
  for (const e of entities) {
    let b = byType.get(e.subject_type);
    if (!b) {
      b = {
        subject_type: e.subject_type,
        entities: [],
        entity_count: 0,
        change_count: 0,
        field_paths: [],
        first_offset_ms: e.first_offset_ms,
        last_offset_ms: e.last_offset_ms,
        first_change: e.first_change,
        last_change: e.last_change,
      };
      byType.set(e.subject_type, b);
    }
    b.entity_count += 1;
    b.change_count += e.change_count;
    for (const p of e.field_paths) {
      if (!b.field_paths.includes(p)) b.field_paths.push(p);
    }
    if (e.first_offset_ms < b.first_offset_ms) {
      b.first_offset_ms = e.first_offset_ms;
      b.first_change = e.first_change;
    }
    if (e.last_offset_ms > b.last_offset_ms) {
      b.last_offset_ms = e.last_offset_ms;
      b.last_change = e.last_change;
    }
    b.entities.push(e);
  }
  const out = [...byType.values()];
  // 变化多的类型排在前面，避免 10 个同形 Power 把真正的主角挤出首屏。
  out.sort((x, y) => y.change_count - x.change_count || x.subject_type.localeCompare(y.subject_type));
  for (const b of out) {
    b.entities.sort((x, y) => y.change_count - x.change_count || x.key.localeCompare(y.key));
  }
  return out;
}

/**
 * 实体类型聚合块。默认折叠（仅一个实体的类型直接展开，多一次点击没意义），
 * 点标题展开该类型下的具体实体。focus 命中类型内实体时自动展开，保证跨视图跳转看得见落点。
 */
function EntityTypeSection({
  bucket,
  focus,
  timeMode,
  children,
}: {
  bucket: EntityTypeBucket;
  focus: { entity?: string };
  timeMode: "relative" | "absolute";
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(bucket.entity_count === 1);
  const hasFocus = !!focus.entity && bucket.entities.some((e) => e.key === focus.entity);
  useEffect(() => {
    if (bucket.entity_count === 1 || hasFocus) setOpen(true);
  }, [bucket.entity_count, hasFocus]);

  // 类型下只有一个实体时直接以它为标题并展开，没必要让用户多点一次。
  const only = bucket.entity_count === 1 ? bucket.entities[0] : undefined;
  const title = only ? `${only.subject_type}:${only.subject_id}` : bucket.subject_type;
  const preview = bucket.field_paths.slice(0, 5);
  const restFields = bucket.field_paths.length - preview.length;

  return (
    <div className={cn("rounded border border-border/70", hasFocus && "border-primary/60")}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full flex-wrap items-center gap-x-2 gap-y-1 px-2 py-1.5 text-left hover:bg-muted/40"
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" />
        )}
        <span className="font-mono text-xs font-semibold">{title}</span>
        <span className="text-[11px] text-muted-foreground">
          {bucket.change_count} 次变化
          {!only && ` · ${bucket.entity_count} 个实体`} · {bucket.field_paths.length} 种字段
        </span>
        {bucket.field_paths.length > 0 && (
          <span className="truncate font-mono text-[10px] text-muted-foreground/70" title={bucket.field_paths.join(", ")}>
            {preview.join(", ")}
            {restFields > 0 && ` +${restFields}`}
          </span>
        )}
        <TimeLabel
          offsetMs={bucket.first_offset_ms}
          iso={bucket.first_change}
          mode={timeMode}
          className="ml-auto text-muted-foreground"
        />
      </button>
      {open && <div className="space-y-1 border-t border-border/70 p-1">{children}</div>}
    </div>
  );
}

function OperationView({
  groups,
  focus,
  timeMode,
  onFocusEntity,
  onAnchor,
  onDetail,
}: {
  groups: OperationGroup[];
  onFocusEntity: (key: string) => void;
} & ViewHandlers) {
  return (
    <div className="gt-scroll flex-1 space-y-2 overflow-y-auto pr-1">
      {groups.map((g) => {
        const lead = leadEntity(g);
        const typeBuckets = groupEntitiesByType(g.entities ?? []);
        // anchor 已经作为组头上的协议名出现过一次，链里只列同操作的其余消息，
        // 否则同一串「响应 SyncProfile T+400ms」会在同一屏里连着出现两遍。
        const chain = (g.chain ?? []).filter((m) => m.event_id !== g.anchor.event_id);
        return (
          <div
            key={g.key}
            id={`sc-op-${g.key}`}
            className={cn(
              "rounded-lg border border-border bg-card p-3 transition-shadow",
              focus.op === g.key && "ring-2 ring-primary/60",
            )}
          >
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-sm font-semibold">
                {lead ? `${lead.subject_type}:${lead.subject_id}` : g.label}
              </span>
              {lead && g.entity_count > 1 && (
                <span className="text-[11px] text-muted-foreground">等 {g.entity_count} 个实体</span>
              )}
              {lead && (
                <span
                  className="inline-flex shrink-0 items-center gap-1 rounded bg-muted px-1.5 py-0.5"
                  title="协议名：本次操作对应的协议消息（次要信息）"
                >
                  <KindBadge kind={g.kind} />
                  <span className="font-mono text-[10px] text-muted-foreground">{g.label}</span>
                </span>
              )}
              <TimeLabel offsetMs={g.start_offset_ms} iso={g.anchor.timestamp} mode={timeMode} className="text-muted-foreground" />
              <span className="text-[11px] text-muted-foreground">
                {g.change_count} 次变化 · {g.field_count} 种字段
              </span>
              <div className="ml-auto flex items-center gap-1">
                <IconBtn title="以此操作为锚点" onClick={() => onAnchor(g.key)}>
                  <Crosshair className="h-3 w-3" />
                </IconBtn>
                <IconBtn title="查看完整协议链" onClick={() => onDetail({ eventId: g.anchor.event_id })}>
                  <History className="h-3 w-3" />
                </IconBtn>
              </div>
            </div>

            {chain.length > 0 && (
              <div className="mt-2 flex flex-wrap items-center gap-1.5">
                <span className="text-[10px] text-muted-foreground/70">同操作其余消息</span>
                {chain.map((m, i) => (
                  <span key={m.event_id} className="inline-flex items-center gap-1.5">
                    {i > 0 && <ArrowRight className="h-3 w-3 text-muted-foreground/60" />}
                    <button
                      type="button"
                      onClick={() => onDetail({ eventId: m.event_id })}
                      title={`${m.msg_name} · ${absTime(m.timestamp)} · ${m.event_id}`}
                      className={cn(
                        "inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5 font-mono text-[11px] hover:bg-muted/60",
                        m.kind === "request" && "border-sky-300/60",
                        m.kind === "push" && "border-amber-300/60",
                      )}
                    >
                      <KindBadge kind={m.kind} />
                      {m.msg_name}
                      <TimeLabel offsetMs={m.offset_ms} iso={m.timestamp} mode={timeMode} className="text-muted-foreground/70" />
                    </button>
                  </span>
                ))}
              </div>
            )}

            {/* 实体（按类型聚合）→ 字段变化 */}
            <div className="mt-2 space-y-1">
              {typeBuckets.map((b) => (
                <EntityTypeSection key={b.subject_type} bucket={b} focus={focus} timeMode={timeMode}>
                  {b.entities.map((e) => (
                    <EntityRow
                      key={e.key}
                      entity={e}
                      timeMode={timeMode}
                      focus={focus}
                      onOpen={() => onFocusEntity(e.key)}
                      onDetail={onDetail}
                    />
                  ))}
                </EntityTypeSection>
              ))}
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ===== 按实体 =====

function EntityView({
  groups,
  focus,
  timeMode,
  onFocusOperation,
  onAnchor,
  onDetail,
}: {
  groups: EntityGroup[];
  onFocusOperation: (key: string) => void;
} & ViewHandlers) {
  const typeBuckets = useMemo(() => groupEntitiesByType(groups), [groups]);
  return (
    <div className="gt-scroll flex-1 space-y-2 overflow-y-auto pr-1">
      {typeBuckets.map((b) => (
        <EntityTypeSection key={b.subject_type} bucket={b} focus={focus} timeMode={timeMode}>
          {b.entities.map((g) => (
            <EntityCard
              key={g.key}
              entity={g}
              focus={focus}
              timeMode={timeMode}
              onFocusOperation={onFocusOperation}
              onAnchor={onAnchor}
              onDetail={onDetail}
            />
          ))}
        </EntityTypeSection>
      ))}
    </div>
  );
}

/** 单个实体的完整卡片：字段路径 + 命中它的操作列表。 */
function EntityCard({
  entity: g,
  focus,
  timeMode,
  onFocusOperation,
  onAnchor,
  onDetail,
}: {
  entity: EntityGroup;
  focus: { entity?: string };
  timeMode: "relative" | "absolute";
  onFocusOperation: (key: string) => void;
  onAnchor: (key: string) => void;
  onDetail: (d: { entity?: string; path?: string }) => void;
}) {
  return (
    <div
      id={`sc-ent-${g.key}`}
      className={cn(
        "rounded-lg border border-border bg-card p-3 transition-shadow",
        focus.entity === g.key && "ring-2 ring-primary/60",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        <EntityChip entity={g} />
        <span className="text-[11px] text-muted-foreground">
          {g.change_count} 次变化 · {g.field_count} 个字段
        </span>
        <TimeLabel offsetMs={g.first_offset_ms} iso={g.first_change} mode={timeMode} className="text-muted-foreground" />
        <ArrowRight className="h-3 w-3 text-muted-foreground/60" />
        <TimeLabel offsetMs={g.last_offset_ms} iso={g.last_change} mode={timeMode} className="text-muted-foreground" />
        <div className="ml-auto flex items-center gap-1">
          <IconBtn title="以该实体为锚点" onClick={() => onAnchor(g.key)}>
            <Crosshair className="h-3 w-3" />
          </IconBtn>
          <IconBtn title="查看完整变化历史" onClick={() => onDetail({ entity: g.key })}>
            <History className="h-3 w-3" />
          </IconBtn>
        </div>
      </div>

      {g.field_paths.length > 0 && (
        <div className="mt-2 flex flex-wrap items-center gap-1">
          {g.field_paths.map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => onDetail({ entity: g.key, path: p })}
              title={`查看字段 ${p} 的完整历史`}
              className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground hover:text-foreground"
            >
              {p}
            </button>
          ))}
        </div>
      )}

      {/* 操作/事件 → 多次字段变化 */}
      <div className="mt-2 space-y-1">
        {g.operations?.map((hit) => (
          <OperationHitRow
            key={hit.key}
            hit={hit}
            timeMode={timeMode}
            onOpen={() => onFocusOperation(hit.key)}
            onDetail={onDetail}
            entityKey={g.key}
          />
        ))}
      </div>
    </div>
  );
}

function EntityChip({ entity }: { entity: EntityGroup }) {
  return (
    <span className="font-mono text-[11px]">
      <span className="text-muted-foreground/70">{entity.subject_type}:</span>
      <span className="font-semibold text-foreground">{entity.subject_id}</span>
    </span>
  );
}

// ===== 按时间 =====

function TimeView({
  buckets,
  timeMode,
  onFocusOperation,
  onFocusEntity,
}: {
  buckets: TimeBucket[];
  onFocusOperation: (key: string) => void;
  onFocusEntity: (key: string) => void;
  timeMode: "relative" | "absolute";
}) {
  const max = Math.max(1, ...buckets.map((b) => b.change_count));
  return (
    <div className="gt-scroll flex-1 space-y-1.5 overflow-y-auto pr-1">
      {buckets.map((b) => (
        <div key={b.index} className="rounded-lg border border-border bg-card p-2">
          <div className="flex items-center gap-2">
            <TimeLabel offsetMs={b.start_offset_ms} iso={b.start} mode={timeMode} className="font-semibold" />
            <span className="text-[11px] text-muted-foreground">
              {b.change_count} 次变化 · {b.entity_count} 个实体
            </span>
            {/* 密度条：一眼看出变化高峰在哪个时间段 */}
            <div className="ml-auto h-2 w-40 overflow-hidden rounded bg-muted">
              <div
                className="h-full rounded bg-primary/70"
                style={{ width: `${Math.max(3, (b.change_count / max) * 100)}%` }}
              />
            </div>
          </div>
          <div className="mt-1.5 space-y-1">
            {b.operations?.map((op) => (
              <div key={op.key} className="flex flex-wrap items-center gap-1.5 pl-1">
                <button
                  type="button"
                  onClick={() => onFocusOperation(op.key)}
                  title="跳到「按操作」视图"
                  className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5 font-mono text-[11px] hover:bg-muted/60"
                >
                  <KindBadge kind={op.kind} />
                  {op.label}
                  <TimeLabel offsetMs={op.offset_ms} mode={timeMode} className="text-muted-foreground/70" />
                </button>
                {op.entities.map((e) => (
                  <button
                    key={e.key}
                    type="button"
                    onClick={() => onFocusEntity(e.key)}
                    title={`${e.field_paths.join(", ")} · 跳到「按实体」视图`}
                    className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground hover:text-foreground"
                  >
                    {e.subject_type}:{e.subject_id} ×{e.change_count}
                  </button>
                ))}
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

// ===== 通用行组件 =====

function IconBtn({ title, onClick, children }: { title: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      title={title}
      onClick={(e) => {
        e.stopPropagation();
        onClick();
      }}
      className="inline-flex h-6 w-6 items-center justify-center rounded border border-border text-muted-foreground hover:bg-muted/60 hover:text-foreground"
    >
      {children}
    </button>
  );
}

/** 实体行（操作视图内嵌）：点标题跳实体视图，展开看字段变化。 */
function EntityRow({
  entity,
  timeMode,
  focus,
  onOpen,
  onDetail,
}: {
  entity: EntityGroup;
  timeMode: "relative" | "absolute";
  focus: { entity?: string };
  onOpen: () => void;
  onDetail: (d: { changeId?: string; entity?: string; path?: string }) => void;
}) {
  const [open, setOpen] = useState(false);
  const all = entity.changes ?? [];
  const preview = all.slice(0, 3);
  const restCount = all.length - preview.length;
  return (
    <div className={cn("rounded border border-border/70", focus.entity === entity.key && "border-primary/60")}>
      <div className="flex flex-wrap items-center gap-2 px-2 py-1">
        <button type="button" onClick={() => setOpen((v) => !v)} className="text-muted-foreground/70" aria-label="展开">
          {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
        </button>
        <button type="button" onClick={onOpen} title="跳到「按实体」视图" className="hover:underline">
          <EntityChip entity={entity} />
        </button>
        <span className="text-[11px] text-muted-foreground">×{entity.change_count}</span>
        {/* 折叠态就要能看到「变成什么」：只给字段名等于没给信息 */}
        {preview.length > 0 ? (
          <span className="flex min-w-0 flex-wrap items-center gap-x-2.5 font-mono text-[10px]">
            {preview.map((c) => (
              <span key={c.id} className="inline-flex items-center gap-1">
                <span className="text-muted-foreground">{c.path}</span>
                <BeforeText change={c} className="text-[10px]" />
                <ArrowRight className="h-2.5 w-2.5 shrink-0 text-muted-foreground/50" />
                <span className="font-medium text-foreground" title="变更后">
                  {compactJson(c.after)}
                </span>
              </span>
            ))}
            {restCount > 0 && <span className="text-muted-foreground/60">+{restCount} 项</span>}
          </span>
        ) : (
          <span className="truncate font-mono text-[10px] text-muted-foreground/70">{entity.field_paths.join(", ")}</span>
        )}
        <IconBtn title="查看该实体完整历史" onClick={() => onDetail({ entity: entity.key })}>
          <History className="h-3 w-3" />
        </IconBtn>
        <TimeLabel offsetMs={entity.first_offset_ms} iso={entity.first_change} mode={timeMode} className="ml-auto text-muted-foreground" />
      </div>
      {open && (
        <div className="space-y-1 border-t border-border/70 px-2 py-1.5">
          {entity.changes?.map((c) => (
            <ChangeRow key={c.id} change={c} timeMode={timeMode} onDetail={onDetail} />
          ))}
        </div>
      )}
    </div>
  );
}

/** 操作命中行（实体视图内嵌）：显示哪条请求/响应/推送改了本实体。 */
function OperationHitRow({
  hit,
  timeMode,
  entityKey,
  onOpen,
  onDetail,
}: {
  hit: { key: string; label: string; kind: MessageKind | string; offset_ms: number; timestamp: string; change_count: number; field_paths: string[]; events?: MessageRef[] };
  timeMode: "relative" | "absolute";
  entityKey: string;
  onOpen: () => void;
  onDetail: (d: { changeId?: string; eventId?: string; entity?: string; path?: string }) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded border border-border/70">
      <div className="flex flex-wrap items-center gap-2 px-2 py-1">
        <button type="button" onClick={() => setOpen((v) => !v)} className="text-muted-foreground/70" aria-label="展开">
          {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
        </button>
        <button type="button" onClick={onOpen} title="跳到「按操作」视图" className="inline-flex items-center gap-1 hover:underline">
          <KindBadge kind={hit.kind} />
          <span className="font-mono text-[11px]">{hit.label}</span>
        </button>
        <span className="text-[11px] text-muted-foreground">×{hit.change_count}</span>
        {/* 具体是哪条消息改的：请求 / 响应 / 推送 */}
        <span className="flex items-center gap-1">
          {hit.events?.map((m) => (
            <button
              key={m.event_id}
              type="button"
              onClick={() => onDetail({ eventId: m.event_id })}
              title={`${m.msg_name} · ${relTime(m.offset_ms)} · 查看协议链`}
              className="rounded bg-muted px-1 py-0.5 font-mono text-[10px] text-muted-foreground hover:text-foreground"
            >
              {KIND_LABEL[m.kind] ?? m.kind}
            </button>
          ))}
        </span>
        <span className="truncate font-mono text-[10px] text-muted-foreground/70">{hit.field_paths.join(", ")}</span>
        <TimeLabel offsetMs={hit.offset_ms} iso={hit.timestamp} mode={timeMode} className="ml-auto text-muted-foreground" />
      </div>
      {open && (
        <div className="border-t border-border/70 px-2 py-1 text-[11px] text-muted-foreground">
          该操作对本实体共产生 {hit.change_count} 次变化，涉及字段 {hit.field_paths.join(", ")}；
          <button
            type="button"
            className="ml-1 underline hover:text-foreground"
            onClick={() => onDetail({ entity: entityKey })}
          >
            查看完整历史
          </button>
        </div>
      )}
    </div>
  );
}

/** before 的展示：真实旧值 / 首见 / 插件声明的参考值三态，判定见 beforeKind。 */
function BeforeText({ change, className }: { change: Change; className?: string }) {
  const kind = beforeKind(change);
  if (kind === "resolved") {
    return (
      <span className={cn("truncate text-muted-foreground/70 line-through", className)} title={compactJson(change.before)}>
        {compactJson(change.before)}
      </span>
    );
  }
  if (kind === "first-seen") {
    return (
      <span
        className={cn("shrink-0 text-muted-foreground/60", className)}
        title="平台此前未见过该字段：这是首次同步，不是一次真实变化"
      >
        首见
      </span>
    );
  }
  return (
    <span
      className={cn("truncate text-muted-foreground/50 underline decoration-dashed underline-offset-2", className)}
      title={`插件声明的前值（平台未验证）：${compactJson(change.before)}`}
    >
      {compactJson(change.before)}
    </span>
  );
}

/** 单条字段变化：序号、相对时间、来源消息、字段路径、before → after。 */
function ChangeRow({
  change,
  timeMode,
  onDetail,
}: {
  change: Change;
  timeMode: "relative" | "absolute";
  onDetail?: (d: { changeId?: string }) => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 rounded px-1.5 py-1 hover:bg-muted/40">
      <span
        className="w-8 shrink-0 font-mono text-[10px] text-muted-foreground/60"
        title={`本视图第 ${change.seq} 条 · 来源消息内第 ${change.src_seq} 条`}
      >
        #{change.seq}
      </span>
      <TimeLabel offsetMs={change.offset_ms} iso={change.timestamp} mode={timeMode} className="w-20 shrink-0" />
      <button
        type="button"
        onClick={() => onDetail?.({ changeId: change.id })}
        title={`${change.source.msg_name} · ${change.event_id} · 点击查看详情`}
        className="inline-flex items-center gap-1 font-mono text-[11px] hover:underline"
      >
        <KindBadge kind={change.source.kind} />
        {change.source.msg_name}
      </button>
      <span className="font-mono text-[11px] text-foreground">{change.path}</span>
      <OpBadge op={change.op} />
      <span className="flex min-w-0 items-center gap-1 font-mono text-[11px]">
        <BeforeText change={change} />
        <ArrowRight className="h-3 w-3 shrink-0 text-muted-foreground/60" />
        <span className="truncate font-medium" title={compactJson(change.after)}>
          {compactJson(change.after)}
        </span>
      </span>
    </div>
  );
}

// ===== 详情弹窗 =====

/**
 * 反向落点：状态变更讲的是「哪条消息改的」，这里把人送回那条消息本身。
 * 事件表按 id 精确定位，所以不必赌它在第几页。
 */
function EventJump({ eventId, onOpen }: { eventId: string; onOpen: (id: string) => void }) {
  return (
    <button
      type="button"
      onClick={() => onOpen(eventId)}
      title="在协议事件视图查看这条消息"
      className="inline-flex shrink-0 items-center gap-1 rounded border border-border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground hover:bg-muted/60 hover:text-foreground"
    >
      <Table2 className="h-3 w-3" />
      查看该消息
    </button>
  );
}

function DetailDialog({
  sessionId,
  detail,
  onClose,
  timeMode,
  onOpenEvent,
}: {
  sessionId: string | null;
  detail: { changeId?: string; eventId?: string; entity?: string; path?: string } | null;
  onClose: () => void;
  timeMode: "relative" | "absolute";
  onOpenEvent?: (eventId: string) => void;
}) {
  const { data, isLoading, isError, error } = useStateChangeDetail(sessionId, detail);
  const title = detail?.changeId
    ? "变更详情"
    : detail?.eventId
      ? "协议链详情"
      : detail?.entity
        ? "实体变化历史"
        : "详情";

  return (
    <Dialog open={!!detail} onClose={onClose} title={title} className="max-w-4xl">
      <div className="max-h-[70vh] space-y-4 overflow-y-auto pr-1">
        {isLoading && (
          <div className="space-y-2">
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-32 w-full" />
          </div>
        )}
        {isError && <p className="text-sm text-destructive">加载失败：{error?.message ?? "未知错误"}</p>}

        {data?.change && (
          <section>
            <h4 className="mb-1 text-xs font-semibold text-muted-foreground">这条变化</h4>
            <div className="rounded-lg border border-border p-2">
              <div className="mb-2 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                <span className="font-mono text-foreground">
                  {data.change.subject_type}:{data.change.subject_id}
                </span>
                <span className="font-mono text-foreground">{data.change.path}</span>
                <OpBadge op={data.change.op} />
                <span>
                  来自 <KindBadge kind={data.change.source.kind} /> {data.change.source.msg_name}
                </span>
                <TimeLabel offsetMs={data.change.offset_ms} iso={data.change.timestamp} mode={timeMode} />
                {onOpenEvent && <EventJump eventId={data.change.event_id} onOpen={onOpenEvent} />}
              </div>
              <div className="grid gap-3 md:grid-cols-2">
                <div>
                  <p className="mb-1 text-[11px] text-muted-foreground">变更前</p>
                  <HighlightedJson data={data.change.before} />
                </div>
                <div>
                  <p className="mb-1 text-[11px] text-muted-foreground">变更后</p>
                  <HighlightedJson data={data.change.after} />
                </div>
              </div>
            </div>
          </section>
        )}

        {data?.chain && (
          <section>
            <h4 className="mb-1 text-xs font-semibold text-muted-foreground">
              协议链（{data.chain.event_count} 条消息）
            </h4>
            <div className="space-y-1.5">
              {data.chain.steps.map((step, i) => (
                <div key={step.message.event_id} className="rounded-lg border border-border p-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-[10px] text-muted-foreground/60">#{i + 1}</span>
                    <KindBadge kind={step.message.kind} />
                    <span className="font-mono text-xs font-semibold">{step.message.msg_name}</span>
                    <TimeLabel offsetMs={step.offset_ms} iso={step.message.timestamp} mode={timeMode} className="text-muted-foreground" />
                    {step.change_count > 0 ? (
                      <span className="text-[11px] text-muted-foreground">{step.change_count} 次变化</span>
                    ) : (
                      <span className="text-[11px] text-muted-foreground/60">未产生状态变化</span>
                    )}
                    {onOpenEvent && (
                      <span className="ml-auto">
                        <EventJump eventId={step.message.event_id} onOpen={onOpenEvent} />
                      </span>
                    )}
                  </div>
                  {step.entities && step.entities.length > 0 && (
                    <div className="mt-1.5 space-y-1 pl-3">
                      {step.entities.map((e) => (
                        <div key={e.key}>
                          <div className="flex items-center gap-2 text-[11px]">
                            <span className="font-mono text-muted-foreground/70">{e.subject_type}:</span>
                            <span className="font-mono text-foreground">{e.subject_id}</span>
                            <span className="text-muted-foreground/70">{e.field_paths.join(", ")}</span>
                          </div>
                          <div className="pl-2">
                            {e.changes?.map((c) => (
                              <ChangeRow key={c.id} change={c} timeMode={timeMode} />
                            ))}
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          </section>
        )}

        {data?.history && (
          <section>
            <h4 className="mb-1 text-xs font-semibold text-muted-foreground">
              完整变化历史：{data.history.subject_type}:{data.history.subject_id}（{data.history.change_count} 次）
            </h4>
            <div className="space-y-2">
              {data.history.fields?.map((f) => (
                <div key={f.path} className="rounded-lg border border-border p-2">
                  <div className="mb-1 flex flex-wrap items-center gap-2 text-[11px]">
                    <span className="font-mono font-semibold">{f.path}</span>
                    <span className="text-muted-foreground">{f.change_count} 次变化</span>
                    <span className="font-mono text-muted-foreground/70">{f.ops.join("/")}</span>
                    <span className="font-mono text-muted-foreground/70">
                      {compactJson(f.first_value)} → {compactJson(f.last_value)}
                    </span>
                  </div>
                  <div className="space-y-0.5">
                    {f.changes?.map((c) => (
                      <ChangeRow key={c.id} change={c} timeMode={timeMode} />
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </section>
        )}

        {data && !data.change && !data.chain && !data.history && (
          <p className="text-sm text-muted-foreground">没有找到对应的变更上下文。</p>
        )}
      </div>
    </Dialog>
  );
}
