// RawDataView — 协议数据页「原始数据」子视图。
//
// 与事件 / 关系 / 状态变更三个视图不同，这里不做三段分离（payload/meta/analysis）、
// 不做提炼与配对分析，把 list_decoded_data 返回的完整事件对象原样铺开
// （id/timestamp/protocol/raw_len/correlation_id/causation_id/parent_id +
// data/meta/analysis/capture），用于排查解码器产出的原始内容
// （例如 meta.msg_name 为空、direction 缺失这类"为什么视图里是 unknown"的问题）。
//
// 交互与其他子视图一致：无查询词时分页拉取、有查询词时拉较大批次在前端
// 按 eventMatchesQuery 模糊过滤；点击行展开完整原始 JSON。
import { useState, useMemo } from "react";
import { useDecodedData } from "@/hooks/use-mcp";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import {
  ChevronRight,
  ChevronDown,
  ChevronsLeft,
  ChevronLeft,
  Copy,
  FileJson2,
  Inbox,
  RotateCw,
  SearchX,
} from "lucide-react";
import type { DecodedEvent } from "@/types/event";
import {
  extractMeta,
  formatTimestamp,
  formatSize,
  DirectionIcon,
  MessageCell,
  HighlightedJson,
} from "@/lib/event-display";
import { eventMatchesQuery, eventMatchesDirection, type DirectionFilter } from "@/lib/fuzzy";

interface RawDataViewProps {
  sessionId: string | null;
  /** 模糊查询关键词；非空时一次性拉取较大批次在前端内存过滤，空时保持分页拉取。 */
  query: string;
  /** 消息方向过滤（C→S / S→C，空 = 全部）；与 query 叠加为 AND。 */
  direction: DirectionFilter;
}

const PAGE_SIZES = [20, 50, 100];
/** 有查询词时向前端内存过滤提供的批次上限（与事件表一致）。 */
const QUERY_FETCH_LIMIT = 1000;

/** 把事件重组为「原始形态」：完整对象原样保留，字段顺序固定、未命中字段不输出。 */
function toRawEvent(ev: DecodedEvent): Record<string, unknown> {
  const raw: Record<string, unknown> = {
    id: ev.id,
    timestamp: ev.timestamp,
    session_id: ev.session_id,
    protocol: ev.protocol,
    raw_len: ev.raw_len,
  };
  if (ev.correlation_id) raw.correlation_id = ev.correlation_id;
  if (ev.causation_id) raw.causation_id = ev.causation_id;
  if (ev.parent_id) raw.parent_id = ev.parent_id;
  raw.data = ev.data;
  if (ev.meta && Object.keys(ev.meta).length > 0) raw.meta = ev.meta;
  if (ev.analysis && Object.keys(ev.analysis).length > 0) raw.analysis = ev.analysis;
  if (ev.capture) raw.capture = ev.capture;
  return raw;
}

/** 复制完整原始 JSON 到剪贴板（失败给 toast，不静默）。 */
async function copyRawJson(ev: DecodedEvent) {
  try {
    await navigator.clipboard.writeText(JSON.stringify(toRawEvent(ev), null, 2));
    toast.success("已复制原始 JSON");
  } catch {
    toast.error("复制失败", "浏览器拒绝访问剪贴板");
  }
}

export function RawDataView({ sessionId, query, direction }: RawDataViewProps) {
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(50);
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());

  // 有查询词或方向过滤时：一次性拉取较大批次在前端内存过滤（保证搜索跨页完整不遗漏）；
  // 无过滤条件时：保持原有分页拉取。
  const useLargeLimit = !!query || !!direction;
  const effectiveLimit = useLargeLimit ? QUERY_FETCH_LIMIT : pageSize;
  const effectiveOffset = useLargeLimit ? 0 : page * pageSize;

  const { data, isLoading, isError, error, refetch } = useDecodedData(sessionId, {
    limit: effectiveLimit,
    offset: effectiveOffset,
  });

  const events = useMemo(() => data?.events ?? [], [data]);
  const totalMatched = data?.total_matched ?? 0;

  const filteredEvents = useMemo(() => {
    if (!query && !direction) return events;
    return events.filter((e) => {
      if (query && !eventMatchesQuery(e, query)) return false;
      if (direction && !eventMatchesDirection(e, direction)) return false;
      return true;
    });
  }, [events, query, direction]);

  const totalPages = Math.ceil(totalMatched / pageSize);

  function handleToggleExpand(eventId: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(eventId)) next.delete(eventId);
      else next.add(eventId);
      return next;
    });
  }

  if (!sessionId) {
    return (
      <EmptyState
        icon={<FileJson2 className="h-5 w-5" />}
        title="未选择会话"
        hint="在左侧会话列表中选择一个会话以查看解码出的原始事件。"
        className="h-64 justify-center"
      />
    );
  }

  if (isLoading) {
    return (
      <div className="space-y-2">
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
        <span className="flex-1">原始数据加载失败：{error?.message ?? "未知错误"}</span>
        <Button variant="outline" size="sm" onClick={() => refetch()} className="h-7">
          <RotateCw className="h-3.5 w-3.5" />
          重试
        </Button>
      </div>
    );
  }

  const filtering = !!query || !!direction;

  if (filteredEvents.length === 0) {
    return (
      <EmptyState
        icon={filtering || totalMatched === 0 ? <SearchX className="h-5 w-5" /> : <Inbox className="h-5 w-5" />}
        title={filtering ? "无匹配结果" : totalMatched === 0 ? "暂无原始数据" : "无数据"}
        hint={
          filtering
            ? "没有事件命中当前过滤条件，尝试更换或清除搜索条件 / 方向过滤。"
            : "该会话尚未产生可解码的协议事件，或解码插件尚未绑定。"
        }
        className="h-64 justify-center"
      />
    );
  }

  return (
    <div className="space-y-3">
      {/* 统计信息 + 每页条数（搜索态隐藏分页相关控件） */}
      <div className="flex flex-wrap items-center justify-between gap-2 px-1 text-xs text-muted-foreground" aria-live="polite">
        <span className="tabular-nums">
          {filtering ? (
            <>
              命中 <b className="font-semibold text-foreground">{filteredEvents.length}</b> 条
            </>
          ) : (
            <>
              共 {totalMatched} 条 · 当前第 {effectiveOffset + 1}–{Math.min(effectiveOffset + pageSize, totalMatched)} 条
            </>
          )}
          {expandedIds.size > 0 ? ` · 已展开 ${expandedIds.size} 行` : ""}
        </span>
        {!filtering && (
          <span className="flex items-center gap-2">
            <span className="text-[11px] text-muted-foreground/70">点击行展开完整原始 JSON</span>
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
      </div>

      {/* 原始事件列表：摘要行可点击展开完整 JSON */}
      <ul className="space-y-2">
        {filteredEvents.map((ev) => {
          const meta = extractMeta(ev.data, ev.meta);
          const expanded = expandedIds.has(ev.id);
          return (
            <li key={ev.id} className="overflow-hidden rounded-lg border border-border bg-background">
              <button
                type="button"
                onClick={() => handleToggleExpand(ev.id)}
                aria-expanded={expanded}
                className="flex w-full items-center gap-1.5 px-2 py-1.5 text-left hover:bg-muted/40"
              >
                <ChevronRight
                  className={`h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform ${expanded ? "rotate-90" : ""}`}
                />
                <DirectionIcon direction={meta.direction} />
                <MessageCell msgName={meta.msgName} semantic={meta.semantic} />
                {ev.protocol && (
                  <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">{ev.protocol}</span>
                )}
                <span className="truncate font-mono text-[11px] text-muted-foreground/70" title={ev.id}>
                  {ev.id.slice(0, 16)}
                </span>
                <span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground">
                  {formatTimestamp(ev.timestamp)}
                </span>
                <span className="shrink-0 font-mono text-[10px] text-muted-foreground/60">{formatSize(ev.raw_len)}</span>
              </button>
              {expanded && (
                <div className="border-t border-border">
                  <div className="flex items-center justify-between gap-2 border-b border-border/60 bg-muted/20 px-2 py-1">
                    <span className="truncate font-mono text-[11px] text-muted-foreground">
                      {ev.id}
                    </span>
                    <span className="flex shrink-0 items-center gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-6 px-2 text-[11px]"
                        onClick={() => copyRawJson(ev)}
                      >
                        <Copy className="h-3 w-3" />
                        复制
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-6 px-2 text-[11px]"
                        onClick={() => handleToggleExpand(ev.id)}
                      >
                        <ChevronDown className="h-3 w-3" />
                        收起
                      </Button>
                    </span>
                  </div>
                  <div className="gt-json-view">
                    <HighlightedJson data={toRawEvent(ev)} />
                  </div>
                </div>
              )}
            </li>
          );
        })}
      </ul>

      {/* 分页控件（过滤态隐藏） */}
      {!filtering && totalPages > 1 && (
        <div className="flex items-center justify-center gap-2 pt-1">
          <Button variant="outline" size="icon" className="h-8 w-8" onClick={() => setPage(0)} disabled={page === 0} aria-label="第一页">
            <ChevronsLeft className="h-4 w-4" />
          </Button>
          <Button variant="outline" size="icon" className="h-8 w-8" onClick={() => setPage((p) => Math.max(0, p - 1))} disabled={page === 0} aria-label="上一页">
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <span className="px-2 text-sm tabular-nums">
            {page + 1} / {totalPages}
          </span>
          <Button variant="outline" size="icon" className="h-8 w-8" onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))} disabled={page >= totalPages - 1} aria-label="下一页">
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
      )}
    </div>
  );
}
