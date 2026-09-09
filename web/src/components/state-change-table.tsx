// StateChangeTable — 协议数据页「状态变更」子视图。
//
// 展示 state_changes 投影表：`subject_type:subject_id` 的 `path` 字段在 `op` 下
// 从 before 变到 after。行内给紧凑的 before/after 摘要，点击展开并排对比完整 JSON。
import { useState, useMemo } from "react";
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
import { ChevronDown, TableProperties, RotateCw } from "lucide-react";
import type { StateChangeRow } from "@/types/state-change";
import { formatTimestamp, OpBadge, HighlightedJson } from "@/lib/event-display";

interface StateChangeTableProps {
  sessionId: string | null;
}

const PAGE_SIZE = 50;
const COLSPAN = 7; // Time | Entity | Path | Op | Version | Before | After

/** 把未知 JSON 值压成单行紧凑文本（行内摘要用）。 */
function compactJson(value: unknown): string {
  if (value === undefined || value === null) return "∅";
  try {
    const s = JSON.stringify(value);
    if (s.length <= 60) return s;
    return `${s.slice(0, 57)}…`;
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

export function StateChangeTable({ sessionId }: StateChangeTableProps) {
  const [page, setPage] = useState<number>(0);
  const [expandedId, setExpandedId] = useState<string | null>(null);
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

      <div className="flex items-center justify-between px-1 text-xs text-muted-foreground" aria-live="polite">
        <span className="tabular-nums">
          共 {totalMatched} 条
          {isPlaceholderData ? " · 更新中…" : ""}
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

      <Table className="gt-table">
        <TableHeader>
          <TableRow>
            <TableHead className="w-44">时间</TableHead>
            <TableHead>实体</TableHead>
            <TableHead>字段路径</TableHead>
            <TableHead className="w-20">操作</TableHead>
            <TableHead className="w-16 text-right">版本</TableHead>
            <TableHead>变更前</TableHead>
            <TableHead>变更后</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {changes.map((row) => {
            const isExpanded = expandedId === row.id;
            return (
              <FragmentRow
                key={row.id}
                row={row}
                isExpanded={isExpanded}
                onToggle={() => setExpandedId(isExpanded ? null : row.id)}
              />
            );
          })}
        </TableBody>
      </Table>

      {totalPages > 1 && (
        <div className="flex items-center justify-center gap-2 pt-2">
          <Button variant="outline" size="sm" className="h-8" onClick={() => setPage((p) => Math.max(0, p - 1))} disabled={page === 0} aria-label="上一页">
            上一页
          </Button>
          <span className="px-2 text-sm tabular-nums">{page + 1} / {totalPages}</span>
          <Button variant="outline" size="sm" className="h-8" onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))} disabled={page >= totalPages - 1} aria-label="下一页">
            下一页
          </Button>
        </div>
      )}
    </div>
  );
}

/** 单行状态变更：主行 + 展开的 before/after 并排对比。 */
function FragmentRow({
  row,
  isExpanded,
  onToggle,
}: {
  row: StateChangeRow;
  isExpanded: boolean;
  onToggle: () => void;
}) {
  return (
    <>
      <TableRow
        id={`state-row-${row.id}`}
        className={`cursor-pointer hover:bg-muted/50 transition-colors ${isExpanded ? "bg-primary/10" : ""}`}
        onClick={onToggle}
        aria-expanded={isExpanded}
      >
        <TableCell className="font-mono text-xs whitespace-nowrap">
          {formatTimestamp(row.timestamp)}
        </TableCell>
        <TableCell><EntityCell row={row} /></TableCell>
        <TableCell className="font-mono text-[11px]">{row.path}</TableCell>
        <TableCell><OpBadge op={row.op} /></TableCell>
        <TableCell className="text-right tabular-nums font-mono text-xs text-muted-foreground">
          v{row.version}
        </TableCell>
        <TableCell className="max-w-[200px]">
          <span className="block truncate font-mono text-[11px] text-muted-foreground" title={compactJson(row.before)}>
            {compactJson(row.before)}
          </span>
        </TableCell>
        <TableCell className="max-w-[200px]">
          <span className="block truncate font-mono text-[11px] text-foreground" title={compactJson(row.after)}>
            {compactJson(row.after)}
          </span>
        </TableCell>
      </TableRow>
      {isExpanded && (
        <TableRow className="gt-fade-in">
          <TableCell colSpan={COLSPAN} className="bg-muted/30 p-4">
            <div className="mb-2 inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
              <ChevronDown className="h-3.5 w-3.5" />
              字段 {row.path}
              <OpBadge op={row.op} />
              <span className="text-[10px] text-muted-foreground/70">
                变更前 → 变更后（实体 {row.subject_type}:{row.subject_id} · v{row.version}）
              </span>
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <p className="mb-1 text-[11px] text-muted-foreground">变更前</p>
                <HighlightedJson data={row.before} />
              </div>
              <div>
                <p className="mb-1 text-[11px] text-muted-foreground">变更后</p>
                <HighlightedJson data={row.after} />
              </div>
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}