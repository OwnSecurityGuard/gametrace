// SessionSubbar — 会话空间的二级视图切换条。
//
// 全平台只有这一条视图 tab：它的条目作用域完全一致（同一个会话内的几种
// 看法），因此可以并列。旧版把「我的抓包 / 会话 / 协议数据」并列在同一排，
// 三者却分属身份级 / 会话级 / 会话级，是本次重构要消除的那个错位。
import { Activity, BellRing, Box, Cable, Compass, Square, Table2 } from "lucide-react";
import { navigate } from "@/lib/router";
import { sessionHref, visibleSessionViews, type SessionView } from "@/lib/routes";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const ICONS: Record<SessionView, typeof Table2> = {
  overview: Compass,
  connections: Cable,
  events: Table2,
  states: Activity,
  alerts: BellRing,
  raw: Box,
};

interface SessionSubbarProps {
  sessionId: string;
  view: SessionView;
  /** 视图右侧计数徽标（概览/状态变更无稳定计数时省略）。 */
  counts: Partial<Record<SessionView, number>>;
  running: boolean;
  stopping: boolean;
  onStop: () => void;
}

export function SessionSubbar({
  sessionId,
  view,
  counts,
  running,
  stopping,
  onStop,
}: SessionSubbarProps) {
  return (
    <div className="flex items-center gap-2 border-b border-border bg-card/60 px-4 py-2">
      <div role="tablist" aria-label="会话视图" className="flex min-w-0 items-center gap-1 overflow-x-auto gt-scroll">
        {visibleSessionViews().map((v) => {
          const Icon = ICONS[v.id];
          const selected = v.id === view;
          const n = counts[v.id];
          return (
            <button
              key={v.id}
              role="tab"
              type="button"
              aria-selected={selected}
              title={v.hint}
              onClick={() => navigate(sessionHref(sessionId, v.id))}
              className={cn(
                "inline-flex shrink-0 items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                selected
                  ? "bg-card text-foreground shadow-sm ring-1 ring-border"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              {v.label}
              {n != null && n > 0 && (
                <span className="font-mono text-micro tabular-nums text-muted-foreground">
                  {n.toLocaleString()}
                </span>
              )}
            </button>
          );
        })}
      </div>

      <div className="flex-1" />

      {running && (
        <Button
          variant="destructive"
          size="sm"
          className="h-7 shrink-0"
          onClick={onStop}
          disabled={stopping}
        >
          <Square className="h-3.5 w-3.5" />
          {stopping ? "停止中…" : "停止抓包"}
        </Button>
      )}
    </div>
  );
}
