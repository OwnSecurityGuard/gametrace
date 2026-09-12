// RawDataView — 协议数据页「原始数据」子视图。
//
// 展示所选连接的原始帧（hex dump），与事件 / 关系 / 状态变更不同：这里不做
// 三段分离（payload/meta/analysis）、不做提炼与配对分析，把抓到的原始帧原样
// 铺开（时间 / 方向 / 源→目标 / 字节数 / 完整 hex），用于排查解码器产出。
//
// 原始帧按连接聚合，选中哪条连接由顶部过滤栏的「连接」下拉（与其余子视图
// 共享的 connFilter）决定；本视图不再自带连接下拉。未选择连接时展示空态引导。
import { useMemo } from "react";
import { useConnectionFrames } from "@/hooks/use-mcp";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Button } from "@/components/ui/button";
import { Network, RotateCw, ListTree, Inbox, Radio } from "lucide-react";
import type { ConnectionFrame, ConnectionSummary } from "@/types/connection";
import { DirectionIcon, formatTimestamp } from "@/lib/event-display";
import { base64ToBytes, hexDump } from "@/lib/hex";

const FRAME_LIMIT = 500;

/** base64 → 字节数（非法输入返回 0）。 */
function byteLen(b64: string): number {
  try {
    return base64ToBytes(b64).length;
  } catch {
    return 0;
  }
}

/** 帧 → 完整 hex dump（超长截断在 hexDump 内处理，避免大 frame 撑爆内存）。 */
function frameHex(frame: ConnectionFrame): string {
  if (!frame.payload) return "(empty)";
  try {
    return hexDump(base64ToBytes(frame.payload), 8192);
  } catch {
    return "(decode error)";
  }
}

interface RawDataViewProps {
  sessionId: string | null;
  /** 连接过滤（null = 未选择）；原始帧按连接聚合，用顶部「连接」下拉选择。 */
  connFilter: ConnectionSummary | null;
}

export function RawDataView({ sessionId, connFilter }: RawDataViewProps) {
  const {
    data,
    isLoading,
    isError,
    error,
    refetch,
  } = useConnectionFrames(sessionId, connFilter?.conn_id ?? null, {
    limit: FRAME_LIMIT,
    offset: 0,
  });
  const frames = useMemo(() => data?.frames ?? [], [data]);
  const truncated = frames.length >= FRAME_LIMIT;

  // 帧 hex 计算 memo 化，避免重渲染重复计算。
  const hexes = useMemo(() => {
    const map: Record<string, string> = {};
    for (const f of frames) map[f.id] = frameHex(f);
    return map;
  }, [frames]);

  if (!sessionId) {
    return (
      <EmptyState
        icon={<Network className="h-5 w-5" />}
        title="未选择会话"
        hint="在左侧会话列表中选择一个会话以查看原始帧。"
        className="h-64 justify-center"
      />
    );
  }

  return (
    <div className="space-y-3">
      {/* 当前连接的简要说明（连接本身由顶部过滤栏的「连接」下拉切换） */}
      {connFilter && (
        <div className="flex flex-wrap items-center gap-2 px-1 text-xs text-muted-foreground" aria-live="polite">
          <span className="inline-flex items-center gap-1.5">
            <Radio className="h-3.5 w-3.5" />
            当前连接
          </span>
          <span className="font-mono">{connFilter.client || "-"} → {connFilter.server || "-"}</span>
        </div>
      )}

      {/* 未选择连接 */}
      {!connFilter && (
        <EmptyState
          icon={<Inbox className="h-5 w-5" />}
          title="未选择连接"
          hint="在顶部过滤栏的「连接」下拉中选择一条连接，此处展示其原始帧（hex dump）。"
          className="h-48 justify-center"
        />
      )}

      {/* 已选择连接：加载 / 错误 / 空 / 帧列表 */}
      {connFilter && isLoading && (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-24 w-full" />
          ))}
        </div>
      )}

      {connFilter && isError && (
        <div
          role="alert"
          className="flex items-center gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
        >
          <span className="flex-1">原始帧加载失败：{error?.message ?? "未知错误"}</span>
          <Button variant="outline" size="sm" onClick={() => refetch()} className="h-7">
            <RotateCw className="h-3 w-3" />
            重试
          </Button>
        </div>
      )}

      {connFilter && !isLoading && !isError && frames.length === 0 && (
        <EmptyState
          icon={<ListTree className="h-5 w-5" />}
          title="暂无原始帧"
          hint="该连接尚无原始帧记录。（非代理抓包不产出 connection frames）"
          className="h-48 justify-center"
        />
      )}

      {connFilter && !isLoading && !isError && frames.length > 0 && (
        <>
          <div className="px-1 text-xs text-muted-foreground" aria-live="polite">
            <span className="tabular-nums">
              {truncated ? `最多展示前 ${FRAME_LIMIT} 帧` : `共 ${frames.length} 帧`}
            </span>
            <span className="ml-2 text-muted-foreground/60">点击暂无交互，仅只读查看 hex dump</span>
          </div>
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
                <pre className="max-h-[300px] overflow-auto whitespace-pre text-xs font-mono">
                  {hexes[frame.id] ?? ""}
                </pre>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}