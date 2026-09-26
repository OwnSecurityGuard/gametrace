// DecodeErrorPanel — 把「解码失败 N 次」还原成「解不开的是什么、为什么」。
//
// 后端在采集时就把失败按归一化错误模板聚合（见 pkg/decode/errorcol.go），所以
// 无论失败十万次还是三次，这里都只会有有限几组；组件只负责画，不做聚合。
//
// 一屏原则：默认只展开前几组、其余折叠。错误种类多的时候，用户先看的是主因，
// 而不是被一屏错误糊住。
import { useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react";
import { useDecodeErrors } from "@/hooks/use-mcp";
import {
  DECODE_ERROR_VISIBLE,
  describeErrorKind,
  errorKindTone,
  summarizeDecodeErrors,
} from "@/lib/decode-error-display";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import type { DecodeErrorGroup } from "@/types/decode-error";

interface DecodeErrorPanelProps {
  sessionId: string | null;
  /** 会话上报的失败次数（0 时不查询、不渲染）。 */
  total?: number;
  className?: string;
}

/** 来源徽标的配色：插件问题待排查（琥珀），链路中断是故障（红）。 */
function kindBadgeClass(kind: string): string {
  switch (errorKindTone(kind)) {
    case "warn":
      return "border-warning/30 text-warning";
    case "error":
      return "border-destructive/40 text-destructive";
    default:
      return "border-border text-muted-foreground";
  }
}

function GroupRow({ group }: { group: DecodeErrorGroup }) {
  return (
    <li className="rounded-lg border border-border/70 bg-background/40 p-2.5">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant="outline" className={`text-2xs ${kindBadgeClass(group.kind)}`}>
              {describeErrorKind(group.kind)}
            </Badge>
            <span className="font-mono text-xs text-foreground break-all">{group.template}</span>
          </div>
          {group.sample && group.sample !== group.template && (
            <p className="mt-1 font-mono text-micro text-muted-foreground break-all">
              样本：{group.sample}
            </p>
          )}
          {(group.sample_raw_packet_id || group.sample_src) && (
            <p className="mt-0.5 font-mono text-2xs text-muted-foreground">
              代表包 {group.sample_raw_packet_id}
              {group.sample_src ? ` · ${group.sample_src} → ${group.sample_dst}` : ""}
            </p>
          )}
        </div>
        <span className="shrink-0 font-mono text-xs font-medium text-foreground">
          {group.count.toLocaleString()} 次
        </span>
      </div>
    </li>
  );
}

export function DecodeErrorPanel({ sessionId, total = 0, className }: DecodeErrorPanelProps) {
  const [showAll, setShowAll] = useState(false);
  const { data, isLoading, isError } = useDecodeErrors(sessionId, total > 0);

  if (total <= 0) return null;

  const groups = data?.groups ?? [];
  const visible = showAll ? groups : groups.slice(0, DECODE_ERROR_VISIBLE);
  const hidden = groups.length - visible.length;

  return (
    <section className={`rounded-2xl border border-border bg-card/60 p-3.5 ${className ?? ""}`}>
      <div className="flex flex-wrap items-center gap-2">
        <AlertTriangle className="h-4 w-4 shrink-0 text-destructive" />
        <h2 className="text-sm font-medium text-foreground">
          解码失败 {total.toLocaleString()} 次
        </h2>
        {groups.length > 0 && (
          <span className="text-xs text-muted-foreground">
            · {groups.length} 类原因 · {summarizeDecodeErrors(groups, data?.total_failures ?? total)}
          </span>
        )}
      </div>

      {isLoading && groups.length === 0 ? (
        <div className="mt-2.5 space-y-1.5">
          <Skeleton className="h-12 w-full rounded-lg" />
          <Skeleton className="h-12 w-full rounded-lg" />
        </div>
      ) : isError ? (
        <p className="mt-2 text-xs text-muted-foreground">
          失败原因加载失败（流式连接可能已断开），失败次数不受影响。
        </p>
      ) : groups.length === 0 ? (
        // 会话确实失败过，但库里没有分组 —— 说明是未记录原因的旧版本抓的，
        // 不能显示成「没有失败」。note 由后端给出更准确的表述。
        <p className="mt-2 text-xs text-muted-foreground">
          {data?.note ?? "该会话没有记录失败原因（可能由旧版本抓取）。"}
        </p>
      ) : (
        <>
          <ul className="mt-2.5 space-y-1.5">
            {visible.map((g) => (
              <GroupRow key={`${g.kind}:${g.template}`} group={g} />
            ))}
          </ul>
          {hidden > 0 && (
            <button
              type="button"
              onClick={() => setShowAll(true)}
              className="mt-2 inline-flex items-center gap-0.5 text-xs text-muted-foreground hover:text-foreground"
            >
              <ChevronRight className="h-3 w-3" />
              还有 {hidden} 类原因，展开全部
            </button>
          )}
          {showAll && groups.length > DECODE_ERROR_VISIBLE && (
            <button
              type="button"
              onClick={() => setShowAll(false)}
              className="mt-2 inline-flex items-center gap-0.5 text-xs text-muted-foreground hover:text-foreground"
            >
              <ChevronDown className="h-3 w-3" />
              收起
            </button>
          )}
        </>
      )}
    </section>
  );
}
