// RawPacketTable — 「原始包」视图：抓到的原始网络包 + 用插件离线解码入口。
//
// 阅读动作与协议数据表保持一致：行可点可展开、可多行同时展开、展开区带头部与复制。
// 注意：后端 list_raw_packets 的 count 是「当前页条数」而非总数，所以这里不能拿它算
// 总页数（否则 totalPages 恒为 1 → 翻页按钮永远不出现 → 只能看到前 20 个包）。
// 改成无总数翻页：本页填满就允许「下一页」，不满即到末尾。
import { useState, useMemo, useEffect, useRef, Fragment, memo } from "react";
import { useRawPackets, useListPlugins, useDecodeRawPackets } from "@/hooks/use-mcp";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { toast } from "@/components/ui/toast";
import {
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronUp,
  ArrowRight,
  Copy,
  Network,
  RotateCw,
  X,
} from "lucide-react";
import type { RawPacket } from "@/types/raw-packet";
import { formatTimestamp } from "@/lib/event-display";
import { hexDump, hexPreview, base64ToBytes } from "@/lib/hex";

interface RawPacketTableProps {
  sessionId: string | null;
  /** 解码成功后由父组件触发切换到协议数据 Tab */
  onDecoded?: () => void;
}

const PAGE_SIZES = [20, 50, 100];
/** 展开指示 | 时间 | 源 | 目标 | 协议 | 长度 | 预览 */
const COLSPAN = 7;

async function copyText(label: string, text: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(`已复制${label}`);
  } catch {
    toast.error("复制失败", "浏览器拒绝访问剪贴板");
  }
}

/** 展开行：完整 hex dump，带头部（哪一行）与复制。 */
function ExpandedHexRow({
  pkt,
  onCollapse,
}: {
  pkt: RawPacket;
  onCollapse: () => void;
}) {
  const hex = useMemo(() => {
    try {
      return hexDump(base64ToBytes(pkt.payload));
    } catch {
      return "(decode error)";
    }
  }, [pkt.payload]);

  return (
    <TableRow className="gt-fade-in">
      <TableCell colSpan={COLSPAN} className="bg-muted/30 p-4">
        <div className="mb-3 flex flex-wrap items-center gap-2 border-b border-border pb-2">
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {formatTimestamp(pkt.timestamp)}
          </span>
          <span className="font-mono text-sm font-semibold">
            {pkt.src}
            <ArrowRight className="mx-1 inline h-3 w-3 text-muted-foreground" />
            {pkt.dst}
          </span>
          <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
            {pkt.protocol || "-"}
          </span>
          <span className="text-xs text-muted-foreground tabular-nums">
            {pkt.payload_len} B
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

/** 单行原始包：memo 化，展开某一行时其余行不重渲染 */
const RawPacketRow = memo(function RawPacketRow({
  pkt,
  isExpanded,
  onToggle,
  onCollapse,
}: {
  pkt: RawPacket;
  isExpanded: boolean;
  onToggle: (id: string) => void;
  onCollapse: (id: string) => void;
}) {
  const preview = useMemo(() => hexPreview(pkt.payload), [pkt.payload]);

  return (
    <Fragment key={pkt.id}>
      <TableRow
        className={`cursor-pointer transition-colors ${isExpanded ? "bg-muted/40" : ""}`}
        onClick={() => onToggle(pkt.id)}
        aria-expanded={isExpanded}
      >
        <TableCell className="w-8 pl-2 pr-0 text-muted-foreground/60">
          <ChevronRight
            className={`h-3.5 w-3.5 transition-transform ${isExpanded ? "rotate-90" : ""}`}
          />
        </TableCell>
        <TableCell className="w-28 font-mono text-xs whitespace-nowrap tabular-nums">
          {formatTimestamp(pkt.timestamp)}
        </TableCell>
        <TableCell className="max-w-[180px] truncate font-mono text-xs" title={pkt.src}>
          {pkt.src}
        </TableCell>
        <TableCell className="max-w-[180px] truncate font-mono text-xs" title={pkt.dst}>
          {pkt.dst}
        </TableCell>
        <TableCell className="w-16 font-mono text-xs text-muted-foreground">
          {pkt.protocol}
        </TableCell>
        <TableCell className="w-16 text-right text-xs tabular-nums text-muted-foreground">
          {pkt.payload_len}
        </TableCell>
        <TableCell className="max-w-[24rem]">
          <span className="block truncate font-mono text-xs text-foreground/70" title={preview}>
            {preview}
          </span>
        </TableCell>
      </TableRow>
      {isExpanded && <ExpandedHexRow pkt={pkt} onCollapse={() => onCollapse(pkt.id)} />}
    </Fragment>
  );
});

export function RawPacketTable({ sessionId, onDecoded }: RawPacketTableProps) {
  const [page, setPage] = useState<number>(0);
  const [pageSize, setPageSize] = useState<number>(PAGE_SIZES[0]!);
  // 多行可同时展开：对比相邻包是这里最常见的动作。
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const [selectedPlugin, setSelectedPlugin] = useState<string>("");
  const [clearExisting, setClearExisting] = useState<boolean>(true);

  // 原始包筛选：本地输入 + 防抖提交后的实际生效条件
  const [protoInput, setProtoInput] = useState("");
  const [srcInput, setSrcInput] = useState("");
  const [dstInput, setDstInput] = useState("");
  const [filters, setFilters] = useState<{
    protocol: string;
    src: string;
    dst: string;
  }>({ protocol: "", src: "", dst: "" });
  const debounceRef = useRef<number | null>(null);

  // 三个筛选输入共用防抖，300ms 后提交，并回到第一页
  useEffect(() => {
    if (debounceRef.current) window.clearTimeout(debounceRef.current);
    debounceRef.current = window.setTimeout(() => {
      setFilters({ protocol: protoInput.trim(), src: srcInput.trim(), dst: dstInput.trim() });
      setPage(0);
      setExpandedIds(new Set());
    }, 300);
    return () => {
      if (debounceRef.current) window.clearTimeout(debounceRef.current);
    };
  }, [protoInput, srcInput, dstInput]);

  function clearFilters() {
    setProtoInput("");
    setSrcInput("");
    setDstInput("");
  }

  const { data: pluginsData } = useListPlugins();
  const decodeMutation = useDecodeRawPackets();
  const plugins = pluginsData?.plugins ?? [];

  // 插件列表加载后默认选第一个
  useEffect(() => {
    if (!selectedPlugin && plugins.length > 0) {
      setSelectedPlugin(plugins[0]!.name);
    }
  }, [plugins, selectedPlugin]);

  function handleDecode() {
    if (!sessionId || !selectedPlugin) return;
    decodeMutation.mutate(
      { sessionId, plugin: selectedPlugin, clearExisting },
      {
        onSuccess: (res) => {
          toast.success(
            "解码完成",
            `成功 ${res?.decoded ?? 0} / 失败 ${res?.decode_errors ?? 0}（共 ${res?.total_raw ?? 0} 包）`,
          );
        },
        onError: (err) => {
          toast.error("解码失败", err.message);
        },
      },
    );
  }

  const offset = page * pageSize;

  const {
    data,
    isLoading,
    isError,
    error,
    refetch,
    isFetching,
    isPlaceholderData,
  } = useRawPackets(sessionId, {
    limit: pageSize,
    offset,
    protocol: filters.protocol || undefined,
    src: filters.src || undefined,
    dst: filters.dst || undefined,
  });

  const packets = useMemo(() => data?.packets ?? [], [data]);
  // 后端不返回总数，只能「本页填满 ⇒ 可能还有下一页」。
  const hasMore = packets.length >= pageSize;

  function handleToggleExpand(packetId: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(packetId)) next.delete(packetId);
      else next.add(packetId);
      return next;
    });
  }

  function handleCollapse(packetId: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      next.delete(packetId);
      return next;
    });
  }

  const hasFilter = !!(protoInput || srcInput || dstInput);

  const filterBar = (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-2.5 shadow-sm">
      <span className="shrink-0 text-xs font-medium text-muted-foreground">筛选</span>
      <Input
        value={protoInput}
        onChange={(e) => setProtoInput(e.target.value)}
        placeholder="协议 (如 TCP)"
        aria-label="按协议筛选"
        className="h-8 w-28 text-xs"
      />
      <Input
        value={srcInput}
        onChange={(e) => setSrcInput(e.target.value)}
        placeholder="源地址"
        aria-label="按源地址筛选"
        className="h-8 w-40 text-xs"
      />
      <Input
        value={dstInput}
        onChange={(e) => setDstInput(e.target.value)}
        placeholder="目的地址"
        aria-label="按目的地址筛选"
        className="h-8 w-40 text-xs"
      />
      {hasFilter && (
        <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={clearFilters}>
          <X className="h-3.5 w-3.5 mr-1" />
          清除
        </Button>
      )}
    </div>
  );

  if (!sessionId) {
    return (
      <EmptyState
        icon={<Network className="h-5 w-5" />}
        title="未选择会话"
        hint="在左侧会话列表中选择一个会话以查看抓包得到的原始包。"
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

  if (packets.length === 0) {
    return (
      <div className="space-y-3 relative">
        {isFetching && !isLoading && <div className="gt-loading-bar" aria-hidden="true" />}
        {filterBar}
        <EmptyState
          icon={<Network className="h-5 w-5" />}
          title={hasFilter ? "无匹配结果" : "暂无原始包"}
          hint={
            hasFilter
              ? "未找到符合筛选条件的原始包，可调整或清除筛选条件。"
              : "该会话尚未抓取到任何原始包数据。"
          }
          className="h-64 justify-center"
        />
      </div>
    );
  }

  return (
    <div className="space-y-3 relative">
      {/* 后台刷新指示 */}
      {isFetching && !isLoading && <div className="gt-loading-bar" aria-hidden="true" />}

      {/* 原始包筛选栏 */}
      {filterBar}

      {/* 解码工具栏：用插件对离线会话原始包批量解码，结果写入 decoded_events */}
      {sessionId && (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-card p-2.5 shadow-sm">
          <span className="shrink-0 text-xs font-medium text-muted-foreground">解码插件</span>
          <select
            className="h-8 rounded-md border border-input bg-background px-2 text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
            value={selectedPlugin}
            onChange={(e) => setSelectedPlugin(e.target.value)}
            disabled={plugins.length === 0}
          >
            {plugins.length === 0 ? (
              <option value="">无可用插件</option>
            ) : (
              plugins.map((p) => (
                <option key={p.name} value={p.name}>{p.name}</option>
              ))
            )}
          </select>
          <label className="flex cursor-pointer select-none items-center gap-1 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={clearExisting}
              onChange={(e) => setClearExisting(e.target.checked)}
              className="h-3.5 w-3.5 rounded border-input accent-primary"
            />
            清空已有解码结果
          </label>
          <Button
            size="sm"
            onClick={handleDecode}
            disabled={!selectedPlugin || decodeMutation.isPending || plugins.length === 0}
          >
            {decodeMutation.isPending ? "解码中…" : "用插件解码"}
          </Button>
          {decodeMutation.isSuccess && decodeMutation.data && decodeMutation.data.decoded > 0 && onDecoded && (
            <Button variant="link" size="sm" className="h-auto p-0" onClick={onDecoded}>
              查看协议数据 →
            </Button>
          )}
        </div>
      )}

      {/* 统计信息 + 每页条数 */}
      <div className="flex flex-wrap items-center justify-between gap-2 px-1 text-xs text-muted-foreground" aria-live="polite">
        <span className="tabular-nums">
          第 {offset + 1}–{offset + packets.length} 条
          {hasMore ? "" : " · 已到末尾"}
          {isPlaceholderData ? " · 更新中…" : ""}
          {expandedIds.size > 0 ? ` · 已展开 ${expandedIds.size} 行` : ""}
        </span>
        <span className="flex items-center gap-2">
          <span className="text-[11px] text-muted-foreground/70">点击行展开完整 hex</span>
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
      </div>

      {/* 数据表格 */}
      <Table className="gt-table" containerClassName="relative w-full overflow-visible">
        <TableHeader>
          <TableRow>
            <TableHead className="w-8 pl-2 pr-0" aria-label="展开" />
            <TableHead className="w-28">时间</TableHead>
            <TableHead>源</TableHead>
            <TableHead>目标</TableHead>
            <TableHead className="w-16">协议</TableHead>
            <TableHead className="w-16 text-right">长度</TableHead>
            <TableHead>Payload (hex)</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {packets.map((pkt) => (
            <RawPacketRow
              key={pkt.id}
              pkt={pkt}
              isExpanded={expandedIds.has(pkt.id)}
              onToggle={handleToggleExpand}
              onCollapse={handleCollapse}
            />
          ))}
        </TableBody>
      </Table>

      {/* 分页控件：后端不返回总数，用「本页是否填满」判断能否继续向后翻 */}
      {(page > 0 || hasMore) && (
        <div className="flex items-center justify-center gap-2 pt-2">
          <Button
            variant="outline"
            size="icon"
            className="h-8 w-8"
            onClick={() => {
              setPage(0);
              setExpandedIds(new Set());
            }}
            disabled={page === 0}
            aria-label="第一页"
          >
            <ChevronsLeft className="h-4 w-4" />
          </Button>
          <Button
            variant="outline"
            size="icon"
            className="h-8 w-8"
            onClick={() => {
              setPage((p) => Math.max(0, p - 1));
              setExpandedIds(new Set());
            }}
            disabled={page === 0}
            aria-label="上一页"
          >
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <span className="px-2 text-sm tabular-nums">第 {page + 1} 页</span>
          <Button
            variant="outline"
            size="icon"
            className="h-8 w-8"
            onClick={() => {
              setPage((p) => p + 1);
              setExpandedIds(new Set());
            }}
            disabled={!hasMore}
            aria-label="下一页"
          >
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
      )}
    </div>
  );
}
