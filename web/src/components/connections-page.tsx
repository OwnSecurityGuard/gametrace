import { useState, useEffect, useMemo } from "react";
import { useConnections } from "@/hooks/use-mcp";
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
import { Badge } from "@/components/ui/badge";
import {
  Network,
  ChevronRight,
  RotateCw,
  ChevronLeft,
  ChevronRight as ChevronRightIcon,
} from "lucide-react";
import { ConnectionDetailView } from "@/components/connection-detail";
import { CaptureSummary } from "@/components/capture-summary";
import { formatTimestamp } from "@/lib/event-display";
import type { ConnectionSummary } from "@/types/connection";

const PAGE_SIZES = [20, 50, 100];

/** 抓包来源 → 展示名（与后端 captureDisplayName 保持一致）。 */
export function sourceDisplayName(source: string): string {
  if (source === "mobile") return "Mobile Proxy";
  if (source === "") return "Proxy";
  return source;
}

/** 时长秒 → 人类可读（如 35s / 5m 12s）。 */
export function formatDuration(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return "-";
  if (sec < 1) return "<1s";
  const total = Math.round(sec);
  const s = total % 60;
  const m = Math.floor(total / 60);
  if (m <= 0) return `${s}s`;
  const h = Math.floor(m / 60);
  if (h <= 0) return `${m}m ${s}s`;
  return `${h}h ${m % 60}m`;
}

/** 协议展示：优先解码事件类型（http_req → HTTP），回退到原始协议大写。 */
export function protocolLabel(c: Pick<ConnectionSummary, "protocol" | "event_type">): string {
  const t = c.event_type || "";
  if (t.toLowerCase().startsWith("http")) return t.startsWith("http_req") ? "HTTP" : "HTTPS";
  if (t) return t.toUpperCase();
  return (c.protocol || "?").toUpperCase();
}

interface ConnectionsPageProps {
  sessionId: string | null;
  /** 点击连接详情中的 flow_id 后跳转到「行为」Tab 并预填 */
  onJumpToRun?: (flowId: string) => void;
}

export function ConnectionsPage({ sessionId, onJumpToRun }: ConnectionsPageProps) {
  const [selected, setSelected] = useState<{ connId: string; seq: number } | null>(null);
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(PAGE_SIZES[1]!);

  // 切换会话时重置选中与分页
  useEffect(() => {
    setSelected(null);
    setPage(0);
  }, [sessionId]);

  const offset = page * pageSize;
  const { data, isLoading, isError, error, refetch, isFetching, isPlaceholderData } = useConnections(
    sessionId,
    { limit: pageSize, offset },
  );

  const connections = useMemo(() => data?.connections ?? [], [data]);
  const count = data?.count ?? 0;
  const totalPages = Math.ceil(count / pageSize);
  // 来源只有一种时不值得占一列（与协议数据表「捕获」列同一规则：无信息就不渲染）。
  const showSource = useMemo(
    () => new Set(connections.map((c) => c.source || "")).size > 1,
    [connections],
  );

  // 已选中连接：渲染详情视图（含 Timeline/Streams/Frames/Events/Raw 子页）。
  if (selected) {
    return (
      <ConnectionDetailView
        sessionId={sessionId}
        connId={selected.connId}
        connSeq={selected.seq}
        onBack={() => setSelected(null)}
        onJumpToRun={onJumpToRun}
      />
    );
  }

  if (!sessionId) {
    return (
      <EmptyState
        icon={<Network className="h-5 w-5" />}
        title="未选择会话"
        hint="在左侧选择一个移动代理抓包会话，此处将按连接聚合展示抓包结果。"
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

  if (connections.length === 0) {
    return (
      <div className="space-y-3 relative">
        <CaptureSummary sessionId={sessionId} connectionCount={count} />
        <EmptyState
          icon={<Network className="h-5 w-5" />}
          title="暂无连接"
          hint="尚未产生任何连接。请确认设备已接入并产生流量（TCP/HTTP 等）。"
          className="h-48 justify-center"
        />
      </div>
    );
  }

  return (
    <div className="space-y-3 relative">
      {isFetching && !isLoading && <div className="gt-loading-bar" aria-hidden="true" />}

      {/* 本次抓包结果摘要：直接回答「抓到没有」。 */}
      <CaptureSummary sessionId={sessionId} connectionCount={count} />

      {/* 统计信息 */}
      <div className="flex flex-wrap items-center justify-between gap-2 px-1 text-xs text-muted-foreground" aria-live="polite">
        <span className="tabular-nums">
          共 {count} 条连接 · 当前第 {offset + 1}–{Math.min(offset + pageSize, count)} 条
          {isPlaceholderData ? " · 更新中…" : ""}
        </span>
        <span className="flex items-center gap-2">
          <span className="text-[11px] text-muted-foreground/70">点击连接查看详情</span>
          <label className="flex items-center gap-1">
            <span className="text-[11px] text-muted-foreground/70">每页</span>
            <select
              value={pageSize}
              onChange={(e) => {
                setPageSize(Number(e.target.value));
                setPage(0);
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
      </div>

      <Table className="gt-table" containerClassName="relative w-full overflow-visible">
        <TableHeader>
          <TableRow>
            <TableHead className="w-14">ID</TableHead>
            <TableHead className="w-28">开始时间</TableHead>
            <TableHead>客户端</TableHead>
            <TableHead>服务端</TableHead>
            <TableHead className="w-20">协议</TableHead>
            {showSource && <TableHead className="w-28">来源</TableHead>}
            <TableHead className="w-28">事件 / 帧</TableHead>
            <TableHead className="w-20 text-right">时长</TableHead>
            <TableHead className="w-8" aria-label="详情" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {connections.map((conn, idx) => {
            const seq = offset + idx + 1;
            return (
              <TableRow
                key={conn.conn_id}
                className="cursor-pointer hover:bg-muted/50 transition-colors"
                onClick={() => setSelected({ connId: conn.conn_id, seq })}
                aria-label={`打开连接 ${String(seq).padStart(3, "0")}`}
              >
                {/* ID */}
                <TableCell className="font-mono text-xs whitespace-nowrap text-muted-foreground">
                  {String(seq).padStart(3, "0")}
                </TableCell>

                {/* 开始时间：只有时长时无法判断「这条连接是什么时候发生的」 */}
                <TableCell className="w-28 font-mono text-xs whitespace-nowrap tabular-nums">
                  {conn.start_time ? formatTimestamp(conn.start_time) : "-"}
                </TableCell>

                {/* Client */}
                <TableCell className="font-mono text-xs max-w-[220px]">
                  <span className="truncate block" title={conn.client}>
                    {conn.client || "-"}
                  </span>
                </TableCell>

                {/* Server */}
                <TableCell className="font-mono text-xs max-w-[240px]">
                  <span className="truncate block" title={conn.server}>
                    {conn.server || "-"}
                  </span>
                </TableCell>

                {/* Protocol */}
                <TableCell>
                  <Badge variant="secondary" className="font-mono">
                    {protocolLabel(conn)}
                  </Badge>
                </TableCell>

                {/* Source */}
                {showSource && (
                  <TableCell>
                    <span className="text-xs text-muted-foreground">
                      {sourceDisplayName(conn.source)}
                    </span>
                  </TableCell>
                )}

                {/* 解码产出量：0 事件说明这条连接只有原始帧、没解出协议 */}
                <TableCell className="whitespace-nowrap text-xs tabular-nums">
                  <span className={conn.event_count > 0 ? "text-foreground" : "text-muted-foreground/60"}>
                    {conn.event_count} 事件
                  </span>
                  <span className="text-muted-foreground/60"> · {conn.frame_count} 帧</span>
                </TableCell>

                {/* Duration */}
                <TableCell className="text-right text-xs tabular-nums whitespace-nowrap">
                  {formatDuration(conn.duration_sec)}
                </TableCell>

                <TableCell className="w-8 text-muted-foreground">
                  <ChevronRight className="h-4 w-4" />
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>

      {/* 分页控件 */}
      {totalPages > 1 && (
        <div className="flex items-center justify-center gap-2 pt-2">
          <Button
            variant="outline"
            size="icon"
            className="h-8 w-8"
            onClick={() => setPage((p) => Math.max(0, p - 1))}
            disabled={page === 0}
            aria-label="上一页"
          >
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <span className="px-2 text-sm tabular-nums">
            {page + 1} / {totalPages}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="h-8 w-8"
            onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
            disabled={page >= totalPages - 1}
            aria-label="下一页"
          >
            <ChevronRightIcon className="h-4 w-4" />
          </Button>
        </div>
      )}
    </div>
  );
}
