import { useMemo } from "react";
import { useSessions } from "@/hooks/use-mcp";
import { CheckCircle2 } from "lucide-react";
import { captureSourceName } from "@/lib/session-source";
import { describeSessionPhase } from "@/lib/session-phase";
import { SessionPhaseTracker } from "@/components/session-phase-tracker";
import { Badge } from "@/components/ui/badge";

/**
 * 「本次抓包」结果摘要：回答用户最关心的问题——这次抓包到底成功没有。
 * 只消费已加载的 session 元数据 + 连接数，不额外拉时间线，保持轻量。
 */
export function CaptureSummary({
  sessionId,
  connectionCount,
}: {
  sessionId: string;
  connectionCount: number;
}) {
  const { data } = useSessions();
  const session = data?.sessions?.find((s) => s.session_id === sessionId);
  // 摘要卡片只拿元数据（拿不到实时态），阶段派生对此有降级处理。
  const phaseInput = useMemo(() => ({ meta: session, status: null }), [session]);
  if (!session) return null;

  const packets = session.raw_packets ?? 0;
  const events = session.events ?? 0;
  const decodeErrors = session.decode_errors ?? 0;
  const hasData = packets > 0 || events > 0 || connectionCount > 0;
  const running = session.status === "running";
  const phase = describeSessionPhase(phaseInput);

  // 零数据是最需要「说人话」的场景：不是丢一句"没有有效数据"就完事，
  // 而是给出连上没 / 抓到没 / 解析出没三行事实 + 具体该做什么。
  if (!hasData) {
    return (
      <div className="rounded-xl border border-border bg-card/60 px-4 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium text-foreground">
            {running ? "抓包进行中" : "本次抓包"}
          </span>
          <span className="text-xs text-muted-foreground">{phase.title}</span>
          {session.port ? (
            <span className="text-xs text-muted-foreground">端口 {session.port}</span>
          ) : null}
        </div>
        <div className="mt-2">
          <SessionPhaseTracker input={phaseInput} compact />
        </div>
      </div>
    );
  }

  const items: { label: string; value: string; warn?: boolean }[] = [
    { label: "来源", value: captureSourceName(session.source) },
    { label: "Packets", value: packets.toLocaleString() },
    { label: "Events", value: events.toLocaleString() },
    { label: "连接", value: connectionCount.toLocaleString() },
    { label: "解码错误", value: decodeErrors.toLocaleString(), warn: decodeErrors > 0 },
  ];

  return (
    <div className="rounded-xl border border-border bg-card/60 px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex items-center gap-1.5 text-sm font-medium text-foreground">
          <CheckCircle2 className="h-4 w-4 text-success" />
          本次抓包
        </span>
        {session.port ? (
          <span className="text-xs text-muted-foreground">端口 {session.port}</span>
        ) : null}
        {session.plugin ? (
          <Badge variant="muted" size="micro">{session.plugin}</Badge>
        ) : null}
        {/* 有数据：阶段标题直接说明"现在能干嘛"（抓包中 / 正在解析 / 可分析） */}
        <Badge variant="success" size="micro">
          {phase.title}
        </Badge>
      </div>
      <div className="mt-2 flex flex-wrap gap-x-5 gap-y-1.5">
        {items.map((it) => (
          <div key={it.label} className="flex items-baseline gap-1.5">
            <span className="text-xs text-muted-foreground">{it.label}</span>
            <span
              className={`font-mono text-sm tabular-nums ${
                it.warn ? "text-destructive" : "text-foreground"
              }`}
            >
              {it.value}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}
