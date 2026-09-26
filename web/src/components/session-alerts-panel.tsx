// SessionAlertsPanel — 会话空间的「命中提醒」视图。
//
// 回答的是一个问题：这条通知是被哪些数据触发的。每次命中在 pipeline 侧就把
// 「触发记录 + 触发前各方向最近 N 条」落库（见 cmd/gt-pipeline/check_hook.go），
// 「触发后 N 条」由后端按触发时刻现查（list_session_alerts 的 after 参数）。
//
// 两级展开：命中行展开看上下文，单条记录再展开看 payload。默认只看骨架信息，
// 否则一屏全是 JSON。
import { useState } from "react";
import { BellRing, ChevronDown, ChevronRight, Inbox } from "lucide-react";
import { useSessionAlertDetail, useSessionAlerts } from "@/hooks/use-mcp";
import { DirectionIcon, HighlightedJson, extractMeta, formatTimestamp } from "@/lib/event-display";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/skeleton";
import { directionBucketText, type AlertRecord, type SessionAlert } from "@/types/session-alert";

/** 列表一次最多取这么多条命中。 */
const LIST_LIMIT = 100;
/** 展开一条命中时补多少条「触发后」记录。 */
const AFTER_RECORDS = 5;

function recordLabel(rec: AlertRecord): string {
  const data = rec.data && typeof rec.data === "object" ? (rec.data as Record<string, unknown>) : {};
  const { msgName } = extractMeta(data, rec.meta);
  return msgName || rec.type || "(未命名消息)";
}

/** 一条解码记录：一行摘要 + 按需展开 payload。 */
function RecordRow({ rec, onOpenEvent }: { rec: AlertRecord; onOpenEvent?: (id: string) => void }) {
  const [open, setOpen] = useState(false);
  const hasDetail = rec.data != null || rec.meta != null;
  return (
    <li className="rounded-md border border-border/60 bg-background/40">
      <div className="flex items-center gap-2 px-2 py-1">
        {hasDetail ? (
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
            className="inline-flex shrink-0 items-center rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
          </button>
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <DirectionIcon direction={rec.direction} />
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
          {recordLabel(rec)}
        </span>
        <span className="shrink-0 font-mono text-2xs text-muted-foreground tabular-nums">
          {formatTimestamp(rec.timestamp)}
        </span>
        {onOpenEvent && rec.id && (
          <Button
            size="sm"
            variant="ghost"
            className="h-6 shrink-0 px-1.5 text-2xs"
            onClick={() => onOpenEvent(rec.id)}
          >
            在事件流中查看
          </Button>
        )}
      </div>
      {open && hasDetail && (
        <div className="border-t border-border/60 px-2 py-1.5">
          {rec.data != null && (
            <div className="max-h-64 overflow-auto gt-scroll">
              <HighlightedJson data={rec.data} />
            </div>
          )}
          {rec.meta != null && (
            <p className="mt-1 font-mono text-2xs text-muted-foreground break-all">
              meta: {JSON.stringify(rec.meta)}
            </p>
          )}
        </div>
      )}
    </li>
  );
}

/** 展开态：触发记录 + 触发前（按方向）+ 触发后（现查）。 */
function AlertDetail({
  sessionId,
  alert,
  onOpenEvent,
}: {
  sessionId: string;
  alert: SessionAlert;
  onOpenEvent?: (id: string) => void;
}) {
  const { data, isFetching } = useSessionAlertDetail(sessionId, alert.alert_id, AFTER_RECORDS);
  const after = data?.alerts?.[0]?.after ?? [];
  const buckets = alert.context ? Object.entries(alert.context) : [];

  return (
    <div className="space-y-2.5 border-t border-border/70 bg-muted/20 px-2.5 py-2">
      {alert.trigger && (
        <section>
          <h4 className="mb-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
            触发数据
          </h4>
          <ul className="space-y-1">
            <RecordRow rec={alert.trigger} onOpenEvent={onOpenEvent} />
          </ul>
        </section>
      )}

      {buckets.length > 0 && (
        <section>
          <h4 className="mb-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
            触发前上下文
          </h4>
          <div className="space-y-2">
            {buckets.map(([bucket, recs]) => (
              <div key={bucket}>
                <p className="mb-0.5 text-2xs text-muted-foreground">
                  {directionBucketText(bucket)} · {recs.length} 条
                </p>
                <ul className="space-y-1">
                  {recs.map((rec, i) => (
                    <RecordRow key={`${bucket}-${rec.id}-${i}`} rec={rec} onOpenEvent={onOpenEvent} />
                  ))}
                </ul>
              </div>
            ))}
          </div>
        </section>
      )}

      <section>
        <h4 className="mb-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
          触发后 {AFTER_RECORDS} 条
          {isFetching && <span className="ml-1 normal-case text-muted-foreground">加载中…</span>}
        </h4>
        {after.length === 0 ? (
          <p className="text-2xs text-muted-foreground">
            {isFetching ? "正在查询…" : "命中之后没有更多解码记录（会话在此结束或没有后续流量）。"}
          </p>
        ) : (
          <ul className="space-y-1">
            {after.map((rec, i) => (
              <RecordRow key={`${rec.id}-${i}`} rec={rec} onOpenEvent={onOpenEvent} />
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function AlertCard({
  sessionId,
  alert,
  expanded,
  onToggle,
  onOpenEvent,
}: {
  sessionId: string;
  alert: SessionAlert;
  expanded: boolean;
  onToggle: () => void;
  onOpenEvent?: (id: string) => void;
}) {
  return (
    <li className="overflow-hidden rounded-lg border border-border/70 bg-background/40">
      <button
        type="button"
        aria-expanded={expanded}
        onClick={onToggle}
        className="flex w-full items-start gap-2 px-2.5 py-2 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {expanded ? (
          <ChevronDown className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        )}
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-1.5">
            <Badge variant="warning" size="micro">
              {alert.rule_name || alert.rule_id}
            </Badge>
            <span className="text-xs font-medium text-foreground">{alert.title}</span>
            <span className="shrink-0 font-mono text-2xs text-muted-foreground tabular-nums">
              {formatTimestamp(alert.timestamp)}
            </span>
          </span>
          {alert.message && (
            <span className="mt-0.5 block truncate text-xs text-muted-foreground">
              {alert.message}
            </span>
          )}
        </span>
      </button>
      {expanded && <AlertDetail sessionId={sessionId} alert={alert} onOpenEvent={onOpenEvent} />}
    </li>
  );
}

interface SessionAlertsPanelProps {
  sessionId: string;
  running: boolean;
  /** 跳到「协议事件」视图并聚焦这条记录（复用会话视图间已有的下钻通道）。 */
  onOpenEvent?: (eventId: string) => void;
}

export function SessionAlertsPanel({ sessionId, running, onOpenEvent }: SessionAlertsPanelProps) {
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const { data, isLoading, isError } = useSessionAlerts(sessionId, { limit: LIST_LIMIT, running });
  const alerts = data?.alerts ?? [];

  if (isLoading) {
    return (
      <div className="space-y-1.5">
        <Skeleton className="h-12 w-full rounded-lg" />
        <Skeleton className="h-12 w-full rounded-lg" />
      </div>
    );
  }
  if (isError) {
    return (
      <EmptyState
        icon={<BellRing className="h-5 w-5" />}
        title="命中记录加载失败"
        hint="服务端可能未支持 list_session_alerts（版本过旧），或会话数据库尚未生成。"
      />
    );
  }
  if (alerts.length === 0) {
    return (
      <EmptyState
        icon={<Inbox className="h-5 w-5" />}
        title="本次会话没有命中提醒"
        hint="项目「分析规则」里的检查规则命中后会在这里留痕；没有命中说明规则条件未被满足，或本会话尚未配置规则。"
      />
    );
  }

  return (
    <div>
      <p className="mb-2 text-xs text-muted-foreground">
        共 {data?.count?.toLocaleString()} 次命中 · 展开任意一条可看到触发它的那条数据与前后上下文
      </p>
      <ul className="space-y-1.5">
        {alerts.map((alert) => (
          <AlertCard
            key={alert.alert_id}
            sessionId={sessionId}
            alert={alert}
            expanded={expandedId === alert.alert_id}
            onToggle={() => setExpandedId((cur) => (cur === alert.alert_id ? null : alert.alert_id))}
            onOpenEvent={onOpenEvent}
          />
        ))}
      </ul>
    </div>
  );
}
