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
import { changeMatchesQuery } from "@/lib/fuzzy";
import { cn } from "@/lib/utils";
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

export function StateChangeExplorer({ sessionId, query }: StateChangeExplorerProps) {
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
  const hasQuery = !!query;
  // 前端模糊过滤：搜索态只展示命中的扁平变更；无搜索时走原有三视图。
  const matchedChanges = useMemo(
    () => (hasQuery ? changes.filter((c) => changeMatchesQuery(c, query!)) : changes),
    [changes, hasQuery, query],
  );

  // 锚点候选随查询结果累积：先看到什么就能拿什么当锚点，不必额外拉全量列表。
  const [catalog, setCatalog] = useState<{ ops: { key: string; label: string }[]; entities: string[] }>({
    ops: [],
    entities: [],
  });
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
      return { ops, entities };
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
      {/* 查询态只做扁平模糊搜索，隐藏锚点/窗口/排序等高级筛选提干扰 */}
      {!hasQuery && (
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

      {/* 三视图切换：共用同一份数据，只是换渲染维度（查询态隐藏） */}
      {!hasQuery && (
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

      {/* 查询态：扁平命中变更列表 */}
      {hasQuery && changes.length > 0 && (
        <div className="space-y-2">
          <div className="px-1 text-xs text-muted-foreground" aria-live="polite">
            命中 <b className="font-semibold text-foreground">{matchedChanges.length}</b> 条状态变更
          </div>
          {matchedChanges.length === 0 ? (
            <EmptyState
              icon={<TableProperties className="h-5 w-5" />}
              title="无匹配状态变更"
              hint="没有变更命中当前关键词，尝试更换或清除模糊搜索条件。"
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

      {/* 非查询态：三视图 */}
      {!hasQuery && data && changes.length > 0 && (
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

      <DetailDialog sessionId={sessionId} detail={detail} onClose={() => setDetail(null)} timeMode={timeMode} />
    </div>
  );
}

// ===== 查询栏 =====

interface QueryBarProps {
  anchorKind: AnchorKind;
  onAnchorKind: (k: AnchorKind) => void;
  anchorId: string;
  onAnchorId: (v: string) => void;
  catalog: { ops: { key: string; label: string }[]; entities: string[] };
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
        className="h-7 w-28 rounded-md border border-border bg-background px-1.5 text-xs"
        placeholder="实体类型"
        value={p.typeFilter}
        onChange={(e) => p.onTypeFilter(e.target.value)}
        aria-label="按实体类型过滤"
      />
      <input
        className="h-7 w-28 rounded-md border border-border bg-background px-1.5 text-xs"
        placeholder="字段路径"
        value={p.pathFilter}
        onChange={(e) => p.onPathFilter(e.target.value)}
        aria-label="按字段路径过滤"
      />
      <input
        className="h-7 w-24 rounded-md border border-border bg-background px-1.5 text-xs"
        placeholder="变化类型"
        value={p.opFilter}
        onChange={(e) => p.onOpFilter(e.target.value)}
        aria-label="按变化类型过滤"
      />

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
      <span className="tabular-nums">
        <b className="font-semibold text-foreground">{s.change_count}</b> 条变化 ·{" "}
        <b className="font-semibold text-foreground">{s.entity_count}</b> 个实体 ·{" "}
        <b className="font-semibold text-foreground">{s.field_count}</b> 个字段 ·{" "}
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
      {groups.map((g) => (
        <div
          key={g.key}
          id={`sc-op-${g.key}`}
          className={cn(
            "rounded-lg border border-border bg-card p-3 transition-shadow",
            focus.op === g.key && "ring-2 ring-primary/60",
          )}
        >
          <div className="flex flex-wrap items-center gap-2">
            <KindBadge kind={g.kind} />
            <span className="font-mono text-sm font-semibold">{g.label}</span>
            <TimeLabel offsetMs={g.start_offset_ms} iso={g.anchor.timestamp} mode={timeMode} className="text-muted-foreground" />
            <span className="text-[11px] text-muted-foreground">
              {g.entity_count} 个实体 · {g.change_count} 次变化 · {g.field_count} 个字段
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

          {/* 协议连：请求 → 响应 → 推送 */}
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            {g.chain.map((m, i) => (
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

          {/* 实体 → 字段变化 */}
          <div className="mt-2 space-y-1">
            {g.entities?.map((e) => (
              <EntityRow
                key={e.key}
                entity={e}
                timeMode={timeMode}
                focus={focus}
                onOpen={() => onFocusEntity(e.key)}
                onDetail={onDetail}
              />
            ))}
          </div>
        </div>
      ))}
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
  return (
    <div className="gt-scroll flex-1 space-y-2 overflow-y-auto pr-1">
      {groups.map((g) => (
        <div
          key={g.key}
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
      ))}
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
        <span className="truncate font-mono text-[10px] text-muted-foreground/70">{entity.field_paths.join(", ")}</span>
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
      <span className="w-8 shrink-0 font-mono text-[10px] text-muted-foreground/60">#{change.seq}</span>
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
        <span className="truncate text-muted-foreground/70 line-through" title={compactJson(change.before)}>
          {compactJson(change.before)}
        </span>
        <ArrowRight className="h-3 w-3 shrink-0 text-muted-foreground/60" />
        <span className="truncate font-medium" title={compactJson(change.after)}>
          {compactJson(change.after)}
        </span>
      </span>
    </div>
  );
}

// ===== 详情弹窗 =====

function DetailDialog({
  sessionId,
  detail,
  onClose,
  timeMode,
}: {
  sessionId: string | null;
  detail: { changeId?: string; eventId?: string; entity?: string; path?: string } | null;
  onClose: () => void;
  timeMode: "relative" | "absolute";
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
