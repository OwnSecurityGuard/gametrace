// SessionOverviewPage — 会话工作区的「概览」入口（P0-5 Session 收口）。
//
// 把 Session 从「一次抓包记录」提升为「一次调试工作单元」的默认落地页：
// 回答用户点进一个会话时最关心的四件事——会话什么状态、抓了多久/多少、
// 最近产生了什么（连接 / 协议事件）、下一步去哪分析。
// 不做新的数据模型，仅聚合既有查询（get_session_status / list_all_sessions /
// list_connections / list_decoded_data）。
// 第三件事（状态）由 lib/session-phase 翻译成人话阶段：running/stopped 只说明
// 进程在不在，用户要的是「现在到哪一步、我该不该动手」。
//
// 这里刻意没有「连接 / 协议数据 / 原始包」那排快捷按钮：视图切换由会话二级条
// 独占，同一屏出现第二组入口就是两个真相。归属项目也不在这里重复——面包屑的
// 中段就是项目名，而它比这里能拿到的 project_id 更早一步说明白。
import { useState } from "react";
import { ArrowRight, FolderInput } from "lucide-react";
import {
  useSessionStatus,
  useSessions,
  useConnections,
  useDecodedData,
  useProjects,
  useMoveSessionToProject,
} from "@/hooks/use-mcp";
import { navigate } from "@/lib/router";
import { sessionHref } from "@/lib/routes";
import { describeSessionPhase } from "@/lib/session-phase";
import { captureSourceName } from "@/lib/session-source";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { toast } from "@/components/ui/toast";
import { PhaseBadge, SessionPhaseTracker } from "@/components/session-phase-tracker";
import { DecodeErrorPanel } from "@/components/decode-error-panel";
import type { ConnectionSummary } from "@/types/connection";
import { Select } from "@/components/ui/select";

interface SessionOverviewPageProps {
  sessionId: string;
  /** 点连接行：把这条连接设为事件视图的过滤条件后跳转（与连接页同一动作）。 */
  onSelectConn: (conn: ConnectionSummary) => void;
}

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  return `${d.getMonth() + 1}月${d.getDate()}日 ${hh}:${mm}`;
}

function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  if (h > 0) return `${h}时${m}分`;
  if (m > 0) return `${m}分${s}秒`;
  return `${s}秒`;
}

function fmtNum(n?: number): string {
  return (n ?? 0).toLocaleString();
}

/** 单个统计卡片。 */
function StatCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-xl border border-border bg-card/60 px-3 py-2.5">
      <p className="text-micro text-muted-foreground">{label}</p>
      <p className="mt-0.5 truncate font-mono text-sm font-medium text-foreground" title={hint ?? value}>
        {value}
      </p>
    </div>
  );
}

/**
 * 未归属会话的归位入口。
 *
 * move_session_to_project 后端早就有，前端一直没有消费方，于是「开始抓包时忘了选项目」
 * 就成了既成事实：会话只能留在未归属桶里。概览页是唯一会主动提醒这件事的地方。
 */
function AssignProjectRow({ sessionId }: { sessionId: string }) {
  const { data } = useProjects();
  const move = useMoveSessionToProject();
  const projects = data?.projects ?? [];
  const [target, setTarget] = useState("");

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-xl border border-dashed border-border bg-card/40 px-3 py-2">
      <span className="text-xs text-muted-foreground">这次抓包还没有归属项目</span>
      {projects.length === 0 ? (
        <span className="text-xs text-muted-foreground">
          你还没有项目 —— 在工作台新建一个后再回来归位。
        </span>
      ) : (
        <>
          <Select
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            aria-label="选择要归入的项目"
            size="micro" className="max-w-[200px]"
          >
            <option value="">选择项目…</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </Select>
          <Button
            size="sm"
            className="h-7"
            disabled={!target || move.isPending}
            onClick={() =>
              move.mutate(
                { session_id: sessionId, project_id: target },
                {
                  onSuccess: () => {
                    const p = projects.find((x) => x.id === target);
                    toast.success("已归入项目", p ? `会话现在属于「${p.name}」` : sessionId);
                    setTarget("");
                  },
                  onError: (err) => toast.error("归位失败", err.message),
                },
              )
            }
          >
            <FolderInput className="h-3.5 w-3.5" />归入项目
          </Button>
        </>
      )}
    </div>
  );
}

export function SessionOverviewPage({ sessionId, onSelectConn }: SessionOverviewPageProps) {
  const { data: sessionsData, isLoading: sessionsLoading } = useSessions();
  const meta = sessionsData?.sessions?.find((s) => s.session_id === sessionId);
  const { data: status } = useSessionStatus(sessionId);
  const { data: connectionsData } = useConnections(sessionId, { limit: 5 });
  const { data: eventsData } = useDecodedData(sessionId, { limit: 5 });

  if (sessionsLoading && !meta) {
    return (
      <div className="mx-auto max-w-4xl space-y-4 p-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  // —— 派生状态（gRPC 实时态优先，降级用会话元数据）——
  const running = (status?.state ?? meta?.status) === "running";
  const packetsIn = (status?.packets_in ?? 0) + (status?.raw_count ?? 0);
  const rawCount = packetsIn > 0 ? packetsIn : (status?.raw_packets ?? meta?.raw_packets ?? 0);
  const eventCount = status?.event_count ?? status?.events ?? meta?.events ?? 0;
  const decodeErrors = status?.decode_errors ?? meta?.decode_errors ?? 0;
  // 人话阶段：进度条 / 事实核对表 / 排查指引全部由它驱动。
  // running/stopped 只说明进程在不在，这里回答的是「到哪一步了、要不要动手」。
  const phaseInput = { meta, status };
  const phase = describeSessionPhase(phaseInput);
  const decodeTotal = eventCount + decodeErrors;
  const decodeRate =
    decodeTotal > 0 ? `${Math.round((eventCount / decodeTotal) * 100)}%` : "—";
  // 运行中的会话实时计算持续时间（每次轮询重渲染会刷新）。
  const liveDuration =
    running && meta?.started_at
      ? Math.max(
          0,
          Math.floor((Date.now() - new Date(meta.started_at).getTime()) / 1000),
        )
      : (status?.duration_sec ?? meta?.duration_sec ?? 0);
  const connectionCount = connectionsData?.count ?? 0;
  const sourceLabel = captureSourceName(meta?.source);

  const recentConnections = connectionsData?.connections ?? [];
  const recentEvents = eventsData?.events ?? [];

  return (
    <div className="h-full overflow-auto gt-scroll">
      <div className="mx-auto max-w-4xl space-y-5 p-6">
        {/* 头部：会话身份 + 状态。视图入口在二级条，这里不放。 */}
        <header>
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-mono text-sm font-semibold text-foreground">{sessionId}</h1>
            {meta?.plugin && <Badge variant="outline">{meta.plugin}</Badge>}
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            {sourceLabel}
            {meta?.port ? ` · 端口 ${meta.port}` : ""}
            {meta?.owner ? ` · ${meta.owner}` : ""}
          </p>
        </header>

        {/* 归属动作只在没归属时出现：已归属的会话由面包屑说明，不需要第二条路径 */}
        {meta && !meta.project_id && <AssignProjectRow sessionId={sessionId} />}

        {/* 阶段追踪：进度条 + 事实核对表 + 排查指引。
            取代原先单一的「等待 Agent 接入」横幅——现在零流量、未连接、
            解码失败、服务重启中断各有各的说法和动作。 */}
        <section className="rounded-2xl border border-border bg-card/60 p-3.5">
          <div className="mb-2.5 flex flex-wrap items-baseline justify-between gap-2">
            <PhaseBadge input={phaseInput} />
            <p className="text-xs text-muted-foreground">{phase.detail}</p>
          </div>
          <SessionPhaseTracker input={phaseInput} />
        </section>

        {/* 解码失败原因：阶段追踪器只说「有失败」，这里回答「失败的是什么、为什么」。
            错误已在后端按归一化模板聚合，所以无论失败多少次，种类数都是有限的。
            任何阶段都显示——排查时最需要它的时刻，恰恰是失败还在增加的时候。 */}
        <DecodeErrorPanel sessionId={sessionId} total={decodeErrors} />

        {/* 统计卡片 */}
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <StatCard label="开始时间" value={fmtTime(meta?.started_at)} />
          <StatCard label="持续时间" value={fmtDuration(liveDuration)} />
          <StatCard label="原始包" value={fmtNum(rawCount)} />
          <StatCard label="解码事件" value={fmtNum(eventCount)} />
          <StatCard label="连接数" value={fmtNum(connectionCount)} />
          <StatCard label="解析成功率" value={decodeRate} hint="解码事件 / (解码事件 + 解码错误)" />
          <StatCard label="解码错误" value={fmtNum(decodeErrors)} />
          <StatCard label="抓包源" value={sourceLabel} />
        </div>

        {/* 最近连接：行点击 = 用这条连接过滤事件视图（与连接页一致的动作） */}
        <section>
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-medium text-foreground">最近连接</h2>
            {recentConnections.length > 0 && (
              <button
                type="button"
                onClick={() => navigate(sessionHref(sessionId, "connections"))}
                className="inline-flex items-center gap-0.5 text-xs text-muted-foreground hover:text-foreground"
              >
                全部 {fmtNum(connectionCount)} 条
                <ArrowRight className="h-3 w-3" />
              </button>
            )}
          </div>
          <div className="mt-2 rounded-2xl border border-border bg-card/60 p-3">
            {recentConnections.length === 0 ? (
              <p className="px-1 py-2 text-sm text-muted-foreground">暂无连接数据。</p>
            ) : (
              <ul className="space-y-1">
                {recentConnections.map((c) => (
                  <li key={c.conn_id}>
                    <button
                      type="button"
                      onClick={() => onSelectConn(c)}
                      title="按这条连接查看协议事件"
                      className="flex w-full items-center gap-3 rounded-lg px-2 py-1.5 text-left hover:bg-muted/40"
                    >
                      <div className="min-w-0 flex-1">
                        <p className="truncate font-mono text-xs text-foreground">
                          {c.client} → {c.server}
                        </p>
                        <p className="truncate text-micro text-muted-foreground">
                          {c.protocol} · {fmtTime(c.start_time)} · {fmtDuration(c.duration_sec)}
                        </p>
                      </div>
                      <span className="shrink-0 font-mono text-xs text-muted-foreground">
                        {fmtNum(c.event_count)} events
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </section>

        {/* 最近协议事件 */}
        <section>
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-medium text-foreground">最近协议事件</h2>
            {recentEvents.length > 0 && (
              <button
                type="button"
                onClick={() => navigate(sessionHref(sessionId, "events"))}
                className="inline-flex items-center gap-0.5 text-xs text-muted-foreground hover:text-foreground"
              >
                查看全部
                <ArrowRight className="h-3 w-3" />
              </button>
            )}
          </div>
          <div className="mt-2 rounded-2xl border border-border bg-card/60 p-3">
            {recentEvents.length === 0 ? (
              <p className="px-1 py-2 text-sm text-muted-foreground">
                暂无解码事件{meta?.plugin ? "" : "（未指定解析插件，仅抓包不解码）"}。
              </p>
            ) : (
              <ul className="space-y-1">
                {recentEvents.map((ev) => (
                  <li key={ev.id}>
                    <button
                      type="button"
                      onClick={() => navigate(sessionHref(sessionId, "events"))}
                      className="flex w-full items-center gap-3 rounded-lg px-2 py-1.5 text-left hover:bg-muted/40"
                    >
                      <Badge variant="outline" className="shrink-0 font-mono text-2xs">
                        {ev.protocol}
                      </Badge>
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
                        {ev.id}
                      </span>
                      <span className="shrink-0 text-micro text-muted-foreground">
                        {fmtTime(ev.timestamp)}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
