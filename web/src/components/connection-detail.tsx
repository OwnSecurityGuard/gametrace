import { useMemo, useState, Fragment } from "react";
import {
  useConnectionDetail,
  useConnectionStreams,
  useConnectionFrames,
} from "@/hooks/use-mcp";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table";
import {
  ArrowLeft,
  Cable,
  Clock,
  ChevronRight,
  ChevronUp,
  Copy,
  Rows3,
  Network,
  ListTree,
  Braces,
  FileText,
  Smartphone,
  Server,
  RotateCw,
} from "lucide-react";
import { toast } from "@/components/ui/toast";
import { unpackJsonStrings } from "@/lib/utils";
import { formatDuration, protocolLabel } from "@/components/connections-page";
import { DirectionIcon, formatTimestamp } from "@/lib/event-display";
import { base64ToBytes, hexDump } from "@/lib/hex";
import type {
  ConnectionDetail,
  ConnectionEvent,
  ConnectionFrame,
  ConnectionStream,
} from "@/types/connection";

interface ConnectionDetailViewProps {
  sessionId: string | null;
  connId: string;
  /** 连接列表中的序号（用于展示 Connection #001） */
  connSeq: number;
  onBack: () => void;
}

type DetailTab = "timeline" | "streams" | "frames" | "events" | "raw";

/** 帧子页单次取数上限（后端 limit；达到即视为被截断）。 */
const FRAME_LIMIT = 500;
/** 帧表列数：展开指示 | 时间 | 方向 | 源→目标 | 协议 | 大小 */
const FRAME_COLSPAN = 6;

const TABS: { id: DetailTab; label: string; icon: typeof Clock }[] = [
  { id: "timeline", label: "时间线", icon: Clock },
  { id: "streams", label: "流", icon: Rows3 },
  { id: "frames", label: "帧", icon: ListTree },
  { id: "events", label: "事件", icon: Braces },
  { id: "raw", label: "原始", icon: FileText },
];

/** 时间（仅时分秒，用于流内紧凑展示）。 */
function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleTimeString("zh-CN", { hour12: false });
  } catch {
    return iso;
  }
}

/** 复制文本到剪贴板（失败给 toast，不静默）。 */
async function copyText(label: string, text: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(`已复制${label}`);
  } catch {
    toast.error("复制失败", "浏览器拒绝访问剪贴板");
  }
}

/** JSON 视图（去转义 + 等宽展示）。 */
function JsonView({ data }: { data: Record<string, unknown> }) {
  const formatted = useMemo(() => JSON.stringify(unpackJsonStrings(data), null, 2), [data]);
  return (
    <pre className="gt-json-pre max-h-[400px] overflow-auto whitespace-pre text-xs font-mono">
      {formatted}
    </pre>
  );
}

// ─── 子页：Timeline ────────────────────────────────────────────

function TimelineTab({ streams }: { streams: ConnectionStream[] }) {
  // 摊平所有流的事件并按时间正序，形成纵向时间线。
  const events = useMemo(() => {
    const all: (ConnectionEvent & { streamSeq: number })[] = [];
    for (const st of streams) {
      for (const ev of st.events) {
        all.push({ ...ev, streamSeq: st.seq });
      }
    }
    all.sort((a, b) => a.timestamp.localeCompare(b.timestamp));
    return all;
  }, [streams]);

  if (events.length === 0) {
    return (
      <EmptyState
        icon={<Clock className="h-5 w-5" />}
        title="时间线为空"
        hint="该连接暂无解码事件。"
        className="h-48 justify-center"
      />
    );
  }

  return (
    <div className="relative ml-2 border-l border-border pl-6">
      {events.map((ev) => (
        <div key={ev.id} className="relative pb-4">
          <span className="absolute -left-[31px] top-1.5 h-2.5 w-2.5 rounded-full bg-primary ring-4 ring-background" />
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-xs tabular-nums text-muted-foreground whitespace-nowrap">
              {formatTimestamp(ev.timestamp)}
            </span>
            <DirectionIcon direction={ev.direction} />
            <Badge variant="secondary" className="font-mono text-[10px]">
              Stream #{ev.streamSeq}
            </Badge>
            <span className="font-mono text-xs font-semibold truncate">
              {ev.msg_name || ev.type || "(unknown)"}
            </span>
          </div>
        </div>
      ))}
    </div>
  );
}

// ─── 子页：Streams（Stream View）──────────────────────────────

function StreamsTab({
  streams,
}: {
  streams: ConnectionStream[];
}) {
  if (streams.length === 0) {
    return (
      <EmptyState
        icon={<Rows3 className="h-5 w-5" />}
        title="暂无流"
        hint="该连接尚未产生解码事件，无法划分流。"
        className="h-48 justify-center"
      />
    );
  }

  return (
    <div className="space-y-4">
      {streams.map((stream) => (
        <div
          key={stream.key}
          className="rounded-lg border border-border bg-card/60 p-4 gt-fade-in"
        >
          {/* 流头 */}
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <Badge variant="default" className="font-mono">
              Stream #{stream.seq}
            </Badge>
            {stream.correlation_id && (
              <Badge variant="outline" className="font-mono text-[10px]" title="关联对话 ID">
                correlation: {stream.correlation_id}
              </Badge>
            )}
            <span className="text-xs text-muted-foreground tabular-nums">
              {formatTimestamp(stream.start_time)}
              {stream.end_time !== stream.start_time && ` → ${formatTimestamp(stream.end_time)}`}
            </span>
            <span className="text-xs text-muted-foreground tabular-nums">
              {stream.event_count} 个事件
            </span>
          </div>

          {/* 流内事件 */}
          <div className="space-y-1.5">
            {stream.events.map((ev) => (
              <div
                key={ev.id}
                className="flex flex-wrap items-center gap-2 rounded-md bg-muted/40 px-3 py-1.5"
              >
                <span className="font-mono text-xs tabular-nums text-muted-foreground whitespace-nowrap">
                  {formatTime(ev.timestamp)}
                </span>
                <DirectionIcon direction={ev.direction} />
                <span className="font-mono text-xs font-semibold truncate" title={ev.msg_name}>
                  {ev.msg_name || ev.type || "(unknown)"}
                </span>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

// ─── 子页：Frames ─────────────────────────────────────────────

/** 展开行：完整 hex dump。头部重复一遍身份信息 + 复制/收起，与协议数据表一致。 */
function FrameHexRow({
  frame,
  colSpan,
  onCollapse,
}: {
  frame: ConnectionFrame;
  colSpan: number;
  onCollapse: () => void;
}) {
  const hex = useMemo(() => {
    try {
      return hexDump(base64ToBytes(frame.payload));
    } catch {
      return "(decode error)";
    }
  }, [frame.payload]);

  return (
    <TableRow className="gt-fade-in">
      <TableCell colSpan={colSpan} className="bg-muted/30 p-4">
        <div className="mb-3 flex flex-wrap items-center gap-2 border-b border-border pb-2">
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {formatTimestamp(frame.timestamp)}
          </span>
          <DirectionIcon direction={frame.direction} />
          <span className="font-mono text-sm font-semibold">
            {frame.src} → {frame.dst}
          </span>
          <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
            {frame.protocol || "-"}
          </span>
          <span className="text-xs tabular-nums text-muted-foreground">
            {frame.payload ? byteLen(frame.payload) : 0} B
          </span>
          <span className="ml-auto flex items-center gap-1.5">
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                void copyText(" hex dump", hex);
              }}
              className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground"
            >
              <Copy className="h-3 w-3" />
              复制 hex
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
        <pre className="max-h-[360px] overflow-auto whitespace-pre text-xs font-mono">{hex}</pre>
      </TableCell>
    </TableRow>
  );
}

/** Raw 子页：帧的连续 hex dump 流（hex 计算 memo 化，避免轮询重复计算）。 */
function RawFrames({ frames }: { frames: ConnectionFrame[] }) {
  const hexes = useMemo(() => {
    const map: Record<string, string> = {};
    for (const f of frames) {
      map[f.id] = frameHex(f);
    }
    return map;
  }, [frames]);

  return (
    <div className="space-y-3">
      {frames.map((frame) => (
        <div key={frame.id} className="rounded-lg border border-border bg-card/60 p-3 gt-fade-in">
          <div className="mb-2 flex flex-wrap items-center gap-2 text-xs">
            <span className="font-mono tabular-nums text-muted-foreground">
              {formatTimestamp(frame.timestamp)}
            </span>
            <DirectionIcon direction={frame.direction} />
            <span className="font-mono text-muted-foreground">
              {frame.src} → {frame.dst}
            </span>
            <span className="ml-auto font-mono tabular-nums text-muted-foreground">
              {frame.payload ? byteLen(frame.payload) : 0} B
            </span>
          </div>
          <pre className="text-xs font-mono whitespace-pre overflow-x-auto max-h-[300px] overflow-y-auto">
            {hexes[frame.id] ?? ""}
          </pre>
        </div>
      ))}
    </div>
  );
}

function FramesTab({
  sessionId,
  connId,
  rawOnly,
}: {
  sessionId: string | null;
  connId: string;
  /** rawOnly=true 时直接以纯 hex dump 展示（Raw 子页）。 */
  rawOnly?: boolean;
}) {
  // 多行可同时展开：对比相邻帧是这里最常见的动作。
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const { data, isLoading, isError, error, refetch } = useConnectionFrames(sessionId, connId, {
    limit: FRAME_LIMIT,
    offset: 0,
  });
  const frames = useMemo(() => data?.frames ?? [], [data]);
  const truncated = frames.length >= FRAME_LIMIT;

  function toggle(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  if (isLoading) {
    return (
      <div className="space-y-2 p-4">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-10 w-full" />
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

  if (frames.length === 0) {
    return (
      <EmptyState
        icon={<ListTree className="h-5 w-5" />}
        title="暂无帧"
        hint="该连接尚无原始帧记录。"
        className="h-48 justify-center"
      />
    );
  }

  return (
    <div className="space-y-3">
      {/* 帧子页：说明取数上限，避免把「只载入了 500 条」误读成「连接只有 500 帧」 */}
      {!rawOnly && (
        <div className="px-1 text-xs text-muted-foreground" aria-live="polite">
          <span className="tabular-nums">
            {truncated ? `最多展示前 ${FRAME_LIMIT} 帧` : `共 ${frames.length} 帧`}
            {expandedIds.size > 0 ? ` · 已展开 ${expandedIds.size} 行` : ""}
          </span>
        </div>
      )}

      {/* Raw 子页：连续 hex dump 流 */}
      {rawOnly ? (
        <RawFrames frames={frames} />
      ) : (
        <Table className="gt-table" containerClassName="relative w-full overflow-visible">
          <TableHeader>
            <TableRow>
              <TableHead className="w-8 pl-2 pr-0" aria-label="展开" />
              <TableHead className="w-28">时间</TableHead>
              <TableHead className="w-12">方向</TableHead>
              <TableHead>源 → 目标</TableHead>
              <TableHead className="w-16">协议</TableHead>
              <TableHead className="w-16 text-right">大小</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {frames.map((frame: ConnectionFrame) => (
              <Fragment key={frame.id}>
                <TableRow
                  className={`cursor-pointer transition-colors ${expandedIds.has(frame.id) ? "bg-muted/40" : ""}`}
                  onClick={() => toggle(frame.id)}
                  aria-expanded={expandedIds.has(frame.id)}
                >
                  <TableCell className="w-8 pl-2 pr-0 text-muted-foreground/60">
                    <ChevronRight
                      className={`h-3.5 w-3.5 transition-transform ${expandedIds.has(frame.id) ? "rotate-90" : ""}`}
                    />
                  </TableCell>
                  <TableCell className="w-28 font-mono text-xs whitespace-nowrap tabular-nums">
                    {formatTimestamp(frame.timestamp)}
                  </TableCell>
                  <TableCell>
                    <DirectionIcon direction={frame.direction} />
                  </TableCell>
                  <TableCell className="font-mono text-xs max-w-[300px]">
                    <span className="truncate block" title={`${frame.src} → ${frame.dst}`}>
                      {frame.src} → {frame.dst}
                    </span>
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground uppercase">
                    {frame.protocol || "-"}
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums text-muted-foreground">
                    {frame.payload ? byteLen(frame.payload) : 0}
                  </TableCell>
                </TableRow>
                {expandedIds.has(frame.id) && (
                  <FrameHexRow
                    frame={frame}
                    colSpan={FRAME_COLSPAN}
                    onCollapse={() => toggle(frame.id)}
                  />
                )}
              </Fragment>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

function byteLen(b64: string): number {
  try {
    return base64ToBytes(b64).length;
  } catch {
    return 0;
  }
}

function frameHex(frame: ConnectionFrame): string {
  if (!frame.payload) return "(empty)";
  try {
    return hexDump(base64ToBytes(frame.payload), 8192);
  } catch {
    return "(decode error)";
  }
}

// ─── 子页：Events ─────────────────────────────────────────────

function EventsTab({
  streams,
}: {
  streams: ConnectionStream[];
}) {
  // 多行可同时展开：请求/响应要对着看，accordion 会逼着来回点。
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());

  // 摊平全部流的事件，按时间正序，作为连接内的事件列表。
  const events = useMemo(() => {
    const all: (ConnectionEvent & { streamSeq: number })[] = [];
    for (const st of streams) {
      for (const ev of st.events) {
        all.push({ ...ev, streamSeq: st.seq });
      }
    }
    all.sort((a, b) => a.timestamp.localeCompare(b.timestamp));
    return all;
  }, [streams]);

  function toggle(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  if (events.length === 0) {
    return (
      <EmptyState
        icon={<Braces className="h-5 w-5" />}
        title="暂无事件"
        hint="该连接尚未产生解码事件。"
        className="h-48 justify-center"
      />
    );
  }

  return (
    <div className="space-y-2">
      <div className="px-1 text-xs text-muted-foreground" aria-live="polite">
        <span className="tabular-nums">
          共 {events.length} 个事件
          {expandedIds.size > 0 ? ` · 已展开 ${expandedIds.size} 行` : ""}
        </span>
      </div>
      {events.map((ev) => (
        <div key={ev.id} className="rounded-md border border-border bg-card/40 overflow-hidden">
          <button
            type="button"
            className={`flex w-full flex-wrap items-center gap-2 px-3 py-2 text-left hover:bg-muted/50 transition-colors ${expandedIds.has(ev.id) ? "bg-muted/40" : ""}`}
            onClick={() => toggle(ev.id)}
            aria-expanded={expandedIds.has(ev.id)}
          >
            <span className="font-mono text-xs tabular-nums text-muted-foreground whitespace-nowrap">
              {formatTimestamp(ev.timestamp)}
            </span>
            <DirectionIcon direction={ev.direction} />
            <Badge variant="secondary" className="font-mono text-[10px]">
              Stream #{ev.streamSeq}
            </Badge>
            <span className="font-mono text-xs font-semibold truncate">
              {ev.msg_name || ev.type || "(unknown)"}
            </span>
          </button>
          {expandedIds.has(ev.id) && (
            <div className="border-t border-border bg-muted/30 p-4 gt-fade-in">
              <div className="mb-3 flex flex-wrap items-center gap-2 border-b border-border pb-2">
                <DirectionIcon direction={ev.direction} />
                <span className="font-mono text-sm font-semibold">
                  {ev.msg_name || ev.type || "(unknown)"}
                </span>
                <span className="font-mono text-xs text-muted-foreground">
                  {formatTimestamp(ev.timestamp)}
                </span>
                <span className="ml-auto flex items-center gap-1.5">
                  <button
                    type="button"
                    onClick={() =>
                      void copyText(" JSON", JSON.stringify(unpackJsonStrings({ ...ev.data, ...(ev.meta && Object.keys(ev.meta).length > 0 ? { _meta: ev.meta } : {}), ...(ev.analysis ?? {}) }), null, 2))
                    }
                    className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground"
                  >
                    <Copy className="h-3 w-3" />
                    复制 JSON
                  </button>
                  <button
                    type="button"
                    onClick={() => toggle(ev.id)}
                    className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground"
                  >
                    <ChevronUp className="h-3 w-3" />
                    收起
                  </button>
                </span>
              </div>
              <JsonView data={{ ...ev.data, ...(ev.meta && Object.keys(ev.meta).length > 0 ? { _meta: ev.meta } : {}), ...(ev.analysis ?? {}) }} />
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

// ─── 详情头 ───────────────────────────────────────────────────

function DetailHeader({
  detail,
  connSeq,
}: {
  detail: ConnectionDetail;
  connSeq: number;
}) {
  return (
    <div className="rounded-lg border border-border bg-card/60 p-4 gt-fade-in">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <h2 className="font-mono text-sm font-semibold">
          Connection #{String(connSeq).padStart(3, "0")}
        </h2>
        <Badge variant="secondary" className="font-mono">
          {protocolLabel(detail)}
        </Badge>
        {detail.source && (
          <Badge variant="outline" className="text-xs">
            {detail.source === "mobile" ? "Mobile Proxy" : detail.source}
          </Badge>
        )}
        <span className="ml-auto flex items-center gap-1.5 text-xs text-muted-foreground tabular-nums">
          <Clock className="h-3.5 w-3.5" />
          {formatDuration(detail.duration_sec)}
        </span>
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {/* Client */}
        <div className="rounded-md bg-muted/40 p-3">
          <div className="mb-1 flex items-center gap-1.5 text-[11px] uppercase tracking-wide text-muted-foreground">
            <Smartphone className="h-3 w-3" />
            Client
          </div>
          <p className="font-mono text-sm truncate" title={detail.client}>
            {detail.client || "-"}
          </p>
          {detail.device && (
            <p className="mt-0.5 text-[11px] text-muted-foreground truncate">{detail.device}</p>
          )}
        </div>

        {/* Server */}
        <div className="rounded-md bg-muted/40 p-3">
          <div className="mb-1 flex items-center gap-1.5 text-[11px] uppercase tracking-wide text-muted-foreground">
            <Server className="h-3 w-3" />
            Server
          </div>
          <p className="font-mono text-sm truncate" title={detail.server}>
            {detail.server || "-"}
          </p>
          {detail.app && (
            <p className="mt-0.5 text-[11px] text-muted-foreground truncate">{detail.app}</p>
          )}
        </div>

        {/* Source */}
        <div className="rounded-md bg-muted/40 p-3">
          <div className="mb-1 flex items-center gap-1.5 text-[11px] uppercase tracking-wide text-muted-foreground">
            <Cable className="h-3 w-3" />
            Source
          </div>
          <p className="text-sm truncate">{detail.source === "mobile" ? "Mobile Proxy" : detail.source || "-"}</p>
        </div>

        {/* 统计 */}
        <div className="rounded-md bg-muted/40 p-3">
          <div className="mb-1 flex items-center gap-1.5 text-[11px] uppercase tracking-wide text-muted-foreground">
            <Network className="h-3 w-3" />
            Stats
          </div>
          <p className="text-sm tabular-nums">
            {detail.event_count} 事件 · {detail.stream_count} 流 · {detail.frame_count} 帧
          </p>
        </div>
      </div>
    </div>
  );
}

// ─── 主组件 ───────────────────────────────────────────────────

export function ConnectionDetailView({
  sessionId,
  connId,
  connSeq,
  onBack,
}: ConnectionDetailViewProps) {
  const [tab, setTab] = useState<DetailTab>("timeline");

  const {
    data: detailData,
    isLoading,
    isError,
    error,
    refetch,
  } = useConnectionDetail(sessionId, connId);
  const { data: streamsData, isLoading: streamsLoading } = useConnectionStreams(
    sessionId,
    connId,
    { limit: 500, offset: 0 },
  );

  const detail = detailData?.connection;
  const streams = useMemo(() => streamsData?.streams ?? [], [streamsData]);

  return (
    <div className="space-y-4">
      {/* 返回按钮 */}
      <Button variant="ghost" size="sm" className="-ml-2 h-7 gap-1" onClick={onBack}>
        <ArrowLeft className="h-4 w-4" />
        返回连接列表
      </Button>

      {/* 详情头 */}
      {isLoading && <Skeleton className="h-36 w-full" />}
      {isError && (
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
      )}
      {!isLoading && !isError && detail && <DetailHeader detail={detail} connSeq={connSeq} />}

      {/* 子页 Tab */}
      <div
        role="tablist"
        aria-label="连接详情视图"
        className="flex items-center gap-1 rounded-lg bg-muted p-1"
      >
        {TABS.map((t) => {
          const selected = tab === t.id;
          const Icon = t.icon;
          return (
            <button
              key={t.id}
              role="tab"
              aria-selected={selected}
              onClick={() => setTab(t.id)}
              className={
                "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-[background-color,color,box-shadow] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 " +
                (selected
                  ? "bg-card text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground")
              }
            >
              <Icon className="h-3.5 w-3.5" />
              {t.label}
            </button>
          );
        })}
      </div>

      {/* 子页内容 */}
      <div className="min-h-[200px]">
        {streamsLoading && tab !== "frames" && (
          <div className="space-y-2 p-4">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        )}
        {!streamsLoading && tab === "timeline" && <TimelineTab streams={streams} />}
        {!streamsLoading && tab === "streams" && (
          <StreamsTab streams={streams} />
        )}
        {tab === "frames" && <FramesTab sessionId={sessionId} connId={connId} />}
        {!streamsLoading && tab === "events" && (
          <EventsTab streams={streams} />
        )}
        {tab === "raw" && <FramesTab sessionId={sessionId} connId={connId} rawOnly />}
      </div>
    </div>
  );
}
