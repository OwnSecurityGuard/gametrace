// StateChangeTable — 协议数据页「状态变更」子视图。
//
// 展示 state_changes 投影表：`subject_type:subject_id` 的 `path` 字段在 `op` 下
// 从 before 变到 after。行内直接给出 `before → after` 的一条变更，点击展开并排对比完整 JSON。
import { useState, useMemo, Fragment } from "react";
import { useStateChanges } from "@/hooks/use-mcp";
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { toast } from "@/components/ui/toast";
import {
  ChevronRight,
  ChevronLeft,
  ChevronsLeft,
  ChevronUp,
  ArrowRight,
  Copy,
  TableProperties,
  RotateCw,
} from "lucide-react";
import type { StateChangeRow } from "@/types/state-change";
import { formatTimestamp, OpBadge, HighlightedJson } from "@/lib/event-display";

interface StateChangeTableProps {
  sessionId: string | null;
}

const PAGE_SIZE = 50;
/** 展开指示 | 时间 | 实体 | 路径 | 操作 | 版本 | 变更 */
const COLSPAN = 7;

/** 把未知 JSON 值压成单行紧凑文本（行内摘要用）。 */
function compactJson(value: unknown): string {
  if (value === undefined || value === null) return "∅";
  try {
    const s = JSON.stringify(value);
    if (s.length <= 40) return s;
    return `${s.slice(0, 37)}…`;
  } catch {
    return String(value);
  }
}

function EntityCell({ row }: { row: StateChangeRow }) {
  return (
    <span className="font-mono text-[11px]">
      <span className="text-muted-foreground/70">{row.subject_type}:</span>
      <span className="text-foreground">{row.subject_id}</span>
    </span>
  );
}

async function copyJson(label: string, data: unknown) {
  try {
    await navigator.clipboard.writeText(JSON.stringify(data, null, 2));
    toast.success(`已复制${label}`);
  } catch {
    toast.error("复制失败", "浏览器拒绝访问剪贴板");
  }
}

export function StateChangeTable({ sessionId }: StateChangeTableProps) {
  const [page, setPage] = useState<number>(0);
  // 多行可同时展开（与事件表一致）：状态对比经常要连着看几行。
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const [opFilter, setOpFilter] = useState<string>("");

  const {
    data,
    isLoading,
    isError,
    error,
    refetch,
    isFetching,
    isPlaceholderData,
  } = useStateChanges(sessionId, { limit: PAGE_SIZE, offset: page * PAGE_SIZE });

  const rawChanges = useMemo(() => data?.changes ?? [], [data]);
  // 前端过滤 op（用后端 op 参数也行，但这里保持极简，避免额外轮询抖动）。
  const changes = useMemo(
    () => (opFilter ? rawChanges.filter((c) => c.op === opFilter) : rawChanges),
    [rawChanges, opFilter],
  );
  const totalMatched = data?.count ?? 0;
  const totalPages = Math.ceil(totalMatched / PAGE_SIZE);

  function toggle(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  if (!sessionId) {
    return (
      <EmptyState
        icon={<TableProperties className="h-5 w-5" />}
        title="未选择会话"
        hint="在左侧会话列表中选择一个会话以查看它的实体状态变更。"
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
        <span className="flex-1">状态变更加载失败：{error?.message ?? "未知错误"}</span>
        <Button variant="outline" size="sm" onClick={() => refetch()} className="h-7">
          <RotateCw className="h-3.5 w-3.5" />
          重试
        </Button>
      </div>
    );
  }

  if (changes.length === 0) {
    return (
      <EmptyState
        icon={<TableProperties className="h-5 w-5" />}
        title={totalMatched === 0 ? "暂无状态变更" : "无匹配的变更"}
        hint={
          totalMatched === 0
            ? "该会话尚未产生实体状态变更投影（插件需声明 _state_changes）。"
            : "清除 op 过滤查看全部变更。"
        }
        className="h-64 justify-center"
      />
    );
  }

  return (
    <div className="space-y-3 relative">
      {isFetching && !isLoading && <div className="gt-loading-bar" aria-hidden="true" />}

      <div className="flex flex-wrap items-center justify-between gap-2 px-1 text-xs text-muted-foreground" aria-live="polite">
        <span className="tabular-nums">
          {/* op 过滤是前端在当前页做的，后端 count 不会变——过滤时明确说清是本页计数，避免误导。 */}
          {opFilter
            ? `本页 ${changes.length} 条（已按 op=${opFilter} 过滤）`
            : `共 ${totalMatched} 条`}
          {isPlaceholderData ? " · 更新中…" : ""}
          {expandedIds.size > 0 ? ` · 已展开 ${expandedIds.size} 行` : ""}
        </span>
        <div className="flex items-center gap-2">
          <label className="text-[11px]">op</label>
          <select
            className="h-7 rounded-md border border-border bg-background px-1.5 text-xs"
            value={opFilter}
            onChange={(e) => setOpFilter(e.target.value)}
            aria-label="按 op 过滤"
          >
            <option value="">全部</option>
            <option value="set">set</option>
            <option value="delete">delete</option>
            <option value="merge">merge</option>
          </select>
        </div>
      </div>

      <Table className="gt-table" containerClassName="relative w-full overflow-visible">
        <TableHeader>
          <TableRow>
            <TableHead className="w-8 pl-2 pr-0" aria-label="展开" />
            <TableHead className="w-28">时间</TableHead>
            <TableHead>实体</TableHead>
            <TableHead>字段路径</TableHead>
            <TableHead className="w-20">操作</TableHead>
            <TableHead className="w-16 text-right">版本</TableHead>
            <TableHead>变更</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {changes.map((row) => (
            <Fragment key={row.id}>
              <StateChangeRowView
                row={row}
                isExpanded={expandedIds.has(row.id)}
                onToggle={() => toggle(row.id)}
              />
              {expandedIds.has(row.id) && (
                <ExpandedChange row={row} onCollapse={() => toggle(row.id)} />
              )}
            </Fragment>
          ))}
        </TableBody>
      </Table>

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

/** 主行：before/after 合成一条「变更」，而不是两列各截一半让人自己对齐。 */
function StateChangeRowView({
  row,
  isExpanded,
  onToggle,
}: {
  row: StateChangeRow;
  isExpanded: boolean;
  onToggle: () => void;
}) {
  return (
    <TableRow
      id={`state-row-${row.id}`}
      className={`cursor-pointer transition-colors ${isExpanded ? "bg-muted/40" : ""}`}
      onClick={onToggle}
      aria-expanded={isExpanded}
    >
      <TableCell className="w-8 pl-2 pr-0 text-muted-foreground/60">
        <ChevronRight className={`h-3.5 w-3.5 transition-transform ${isExpanded ? "rotate-90" : ""}`} />
      </TableCell>
      <TableCell className="w-28 font-mono text-xs whitespace-nowrap tabular-nums">
        {formatTimestamp(row.timestamp)}
      </TableCell>
      <TableCell>
        <EntityCell row={row} />
      </TableCell>
      <TableCell className="max-w-[220px]">
        <span className="block truncate font-mono text-[11px]" title={row.path}>
          {row.path}
        </span>
      </TableCell>
      <TableCell>
        <OpBadge op={row.op} />
      </TableCell>
      <TableCell className="text-right tabular-nums font-mono text-xs text-muted-foreground">
        v{row.version}
      </TableCell>
      <TableCell className="max-w-[26rem]">
        <span className="flex min-w-0 items-center gap-1.5 font-mono text-[11px]">
          <span className="truncate text-muted-foreground/70 line-through" title={compactJson(row.before)}>
            {compactJson(row.before)}
          </span>
          <ArrowRight className="h-3 w-3 shrink-0 text-muted-foreground/60" />
          <span className="truncate font-medium text-foreground" title={compactJson(row.after)}>
            {compactJson(row.after)}
          </span>
        </span>
      </TableCell>
    </TableRow>
  );
}

/** 展开区：并排 before/after 完整 JSON，带复制与收起。 */
function ExpandedChange({ row, onCollapse }: { row: StateChangeRow; onCollapse: () => void }) {
  return (
    <TableRow className="gt-fade-in">
      <TableCell colSpan={COLSPAN} className="bg-muted/30 p-4">
        <div className="mb-3 flex flex-wrap items-center gap-2 border-b border-border pb-2">
          <span className="font-mono text-sm font-semibold">{row.path}</span>
          <OpBadge op={row.op} />
          <span className="text-[11px] text-muted-foreground">
            实体 {row.subject_type}:{row.subject_id} · v{row.version} ·{" "}
            {formatTimestamp(row.timestamp)}
          </span>
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              onCollapse();
            }}
            className="ml-auto inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground"
          >
            <ChevronUp className="h-3 w-3" />
            收起
          </button>
        </div>
        <div className="grid gap-4 lg:grid-cols-2">
          <div>
            <div className="mb-1 flex items-center justify-between">
              <p className="text-[11px] text-muted-foreground">变更前</p>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  void copyJson("变更前", row.before);
                }}
                className="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground"
              >
                <Copy className="h-3 w-3" />
                复制
              </button>
            </div>
            <HighlightedJson data={row.before} />
          </div>
          <div>
            <div className="mb-1 flex items-center justify-between">
              <p className="text-[11px] text-muted-foreground">变更后</p>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  void copyJson("变更后", row.after);
                }}
                className="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground"
              >
                <Copy className="h-3 w-3" />
                复制
              </button>
            </div>
            <HighlightedJson data={row.after} />
          </div>
        </div>
      </TableCell>
    </TableRow>
  );
}
