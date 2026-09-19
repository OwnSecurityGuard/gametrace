// RelationshipView — 会话级「关系」子视图（协议数据页内）。
//
// 把整会话的 pair 请求/响应配对可视化，弥补单事件展开行只能看局部的不足：
// 在这里用一组卡片看配对，满足"关系在协议数据也展示，而不仅仅只有事件页面"。
//
// 历史：曾同时展示 extract 父子层级树。extract 效果已于 2026-09-18 整体删除
// （含 parent_id 列与事件表父子面板），依赖它的树与「只看有关系的事件」开关
// 一并移除——那个开关的唯一作用是避免森林退化成第二份事件列表。
import { useMemo } from "react";
import { useDecodedData } from "@/hooks/use-mcp";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Button } from "@/components/ui/button";
import { RotateCw, UserRound, Link2, Network, Inbox } from "lucide-react";
import type { DecodedEvent } from "@/types/event";
import type { ConnectionSummary } from "@/types/connection";
import { extractMeta, formatTimestamp, DirectionIcon, MessageCell } from "@/lib/event-display";
import {
  eventMatchesQuery,
  eventMatchesDirection,
  eventMatchesConnection,
  type DirectionFilter,
  type SemanticFilter,
} from "@/lib/fuzzy";

interface RelationshipViewProps {
  sessionId: string | null;
  query: string;
  /** 消息方向过滤（C→S / S→C，空 = 全部）；与 query 叠加为 AND。 */
  direction: DirectionFilter;
  /** 语义标签过滤（annotate：request/response/notification/error，空 = 全部）；
   *  服务端 list_decoded_data 过滤，前端仅透传。 */
  semantic: SemanticFilter;
  /** 连接过滤（null = 全部连接）；按捕获上下文 conn_id 匹配，与 query/direction 叠加为 AND。 */
  connFilter: ConnectionSummary | null;
}

// 加载上限：关系视图需要整段事件来建配对组，取一次较大的批次。
const RELATION_FETCH_LIMIT = 1000;

export function RelationshipView({ sessionId, query, direction, semantic, connFilter }: RelationshipViewProps) {
  const { data, isLoading, isError, error, refetch } = useDecodedData(sessionId, {
    limit: RELATION_FETCH_LIMIT,
    offset: 0,
    // 语义标签由服务端过滤（meta.semantic 数组成员匹配），前端仅透传。
    semantic: semantic || undefined,
  });

  const events = data?.events ?? [];
  const totalMatched = data?.total_matched ?? 0;
  const truncated = totalMatched > events.length;

  // 前端过滤：在已加载的整段事件上按 query + direction + 连接过滤（AND），再据此建配对组。
  const filteredEvents = useMemo(() => {
    if (!query && !direction && !connFilter) return events;
    return events.filter((e) => {
      if (query && !eventMatchesQuery(e, query)) return false;
      if (direction && !eventMatchesDirection(e, direction)) return false;
      if (connFilter && !eventMatchesConnection(e, connFilter)) return false;
      return true;
    });
  }, [events, query, direction, connFilter]);

  const hasFilter = !!query || !!direction || !!connFilter;

  // 配对索引：请求事件 id → 其响应（causation_id 指回请求）。
  const responsesOf = useMemo(() => {
    const m = new Map<string, DecodedEvent[]>();
    for (const ev of filteredEvents) {
      if (!ev.causation_id) continue;
      const arr = m.get(ev.causation_id);
      if (arr) arr.push(ev);
      else m.set(ev.causation_id, [ev]);
    }
    return m;
  }, [filteredEvents]);
  const requestWithResponses = filteredEvents.filter((ev) => (responsesOf.get(ev.id)?.length ?? 0) > 0);

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          已载入 {filteredEvents.length} 条事件{hasFilter ? "（命中过滤条件）" : ""}
          {truncated ? `（会话共 ${totalMatched} 条，关系视图最多取前 ${RELATION_FETCH_LIMIT} 条）` : ""}
          {" "}· 配对 {requestWithResponses.length} 组
        </p>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : isError ? (
        <div
          role="alert"
          className="flex items-center gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
        >
          <span className="flex-1">关系加载失败：{error?.message ?? "未知错误"}</span>
          <Button variant="outline" size="sm" onClick={() => refetch()} className="h-7">
            <RotateCw className="h-3.5 w-3.5" />
            重试
          </Button>
        </div>
      ) : filteredEvents.length === 0 ? (
        <EmptyState
          icon={<Inbox className="h-5 w-5" />}
          title={hasFilter ? "无匹配关系" : "暂无关系数据"}
          hint={
            hasFilter
              ? "没有事件命中当前过滤条件。尝试更换或清除模糊搜索条件 / 方向过滤。"
              : "未解码到 pair 配对。尝试清除模糊搜索条件，或确认解码插件声明了 pair 语义规则。"
          }
          className="h-64 justify-center"
        />
      ) : (
        /* 配对关系 */
        <section>
          <div className="mb-2 flex items-center gap-1.5 text-sm font-medium text-foreground">
            <Link2 className="h-4 w-4 text-primary" />
            pair 请求/响应配对
            <span className="text-[11px] font-normal text-muted-foreground/70">
              {requestWithResponses.length} 组请求
            </span>
          </div>
          <div className="rounded-2xl border border-border bg-card/60 p-3">
            {requestWithResponses.length === 0 ? (
              <p className="px-1 py-2 text-sm text-muted-foreground">无已配对的请求/响应对。</p>
            ) : (
              <ul className="space-y-2">
                {requestWithResponses.map((req) => {
                  const rMeta = extractMeta(req.data, req.meta);
                  const responses = responsesOf.get(req.id) ?? [];
                  return (
                    <li key={req.id} className="rounded-lg border border-border bg-background p-2">
                      <div className="flex items-center gap-2">
                        <Network className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                        <MessageCell msgName={rMeta.msgName} semantic={rMeta.semantic} />
                        <UserRound className="h-3 w-3 text-muted-foreground/60" />
                        <span className="font-mono text-[10px] text-muted-foreground/70">req</span>
                        <span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground">
                          {formatTimestamp(req.timestamp)}
                        </span>
                      </div>
                      <ul className="mt-1 space-y-0.5 pl-6">
                        {responses.map((res) => {
                          const sMeta = extractMeta(res.data, res.meta);
                          return (
                            <li key={res.id} className="flex items-center gap-2">
                              <span className="text-[10px] text-muted-foreground/50">↳</span>
                              <DirectionIcon direction={sMeta.direction} />
                              <MessageCell msgName={sMeta.msgName} semantic={sMeta.semantic} />
                              <span className="font-mono text-[10px] text-muted-foreground/70">rsp</span>
                              <span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground">
                                {formatTimestamp(res.timestamp)}
                              </span>
                            </li>
                          );
                        })}
                      </ul>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </section>
      )}
    </div>
  );
}
