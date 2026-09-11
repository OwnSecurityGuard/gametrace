// RelationshipView — 会话级「关系」子视图（协议数据页内）。
//
// 把整会话的 extract 父子层级与 pair 请求/响应配对可视化，弥补单事件展开行
// 只能看局部的不足：在这里用一棵树看父子、一组卡片看配对，满足"关系在协议数据
// 也展示，而不仅仅只有事件页面"。
//
// 默认「只看有关系的事件」：孤立事件在事件表里已经能逐条看，全量铺进树里
// 会让这棵树退化成第二份事件列表，反而找不到重点。
import { useState, useMemo, Fragment } from "react";
import { useDecodedData } from "@/hooks/use-mcp";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Button } from "@/components/ui/button";
import {
  RotateCw,
  UserRound,
  GitFork,
  Link2,
  Network,
  ChevronRight,
  ChevronDown,
  Inbox,
} from "lucide-react";
import type { DecodedEvent } from "@/types/event";
import {
  extractMeta,
  formatTimestamp,
  DirectionIcon,
  MessageCell,
  HighlightedJson,
} from "@/lib/event-display";
import { eventMatchesQuery } from "@/lib/fuzzy";

interface RelationshipViewProps {
  sessionId: string | null;
  query: string;
}

// 加载上限：关系视图需要整段事件来建父子森林与配对组，取一次较大的批次。
const RELATION_FETCH_LIMIT = 1000;

export function RelationshipView({ sessionId, query }: RelationshipViewProps) {
  const { data, isLoading, isError, error, refetch } = useDecodedData(sessionId, {
    limit: RELATION_FETCH_LIMIT,
    offset: 0,
  });

  const [onlyRelated, setOnlyRelated] = useState(true);

  const events = data?.events ?? [];
  const totalMatched = data?.total_matched ?? 0;
  const truncated = totalMatched > events.length;

  // 前端模糊过滤：在已加载的整段事件上按 query 过滤，再据此建父子森林与配对组。
  const filteredEvents = useMemo(() => {
    if (!query) return events;
    return events.filter((e) => eventMatchesQuery(e, query));
  }, [events, query]);

  const hasQuery = !!query;

  // 父子索引：parent_id → 子事件；roots = 没有父事件在已载入集合内的顶层事件。
  const { childrenOf, roots } = useMemo(() => {
    const byId = new Map<string, DecodedEvent>();
    const children = new Map<string, DecodedEvent[]>();
    for (const ev of filteredEvents) byId.set(ev.id, ev);
    for (const ev of filteredEvents) {
      if (!ev.parent_id) continue;
      const arr = children.get(ev.parent_id);
      if (arr) arr.push(ev);
      else children.set(ev.parent_id, [ev]);
    }
    const top = filteredEvents.filter((ev) => !ev.parent_id || !byId.has(ev.parent_id));
    return { childrenOf: children, roots: top };
  }, [filteredEvents]);

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

  // 有关系 = 参与过 extract 父子（任一侧）或 pair 配对（任一侧）。
  const relatedIds = useMemo(() => {
    const s = new Set<string>();
    for (const ev of filteredEvents) {
      if (ev.parent_id) {
        s.add(ev.id);
        s.add(ev.parent_id);
      }
      if (ev.causation_id) {
        s.add(ev.id);
        s.add(ev.causation_id);
      }
    }
    return s;
  }, [filteredEvents]);

  const visibleRoots = useMemo(
    () => (onlyRelated ? roots.filter((ev) => relatedIds.has(ev.id)) : roots),
    [roots, onlyRelated, relatedIds],
  );

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          已载入 {filteredEvents.length} 条事件{hasQuery ? "（命中模糊搜索）" : ""}
          {truncated ? `（会话共 ${totalMatched} 条，关系视图最多取前 ${RELATION_FETCH_LIMIT} 条）` : ""}
          {" "}· 有关系 {relatedIds.size} 条
        </p>
        <label className="flex cursor-pointer select-none items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground">
          <input
            type="checkbox"
            checked={onlyRelated}
            onChange={(e) => setOnlyRelated(e.target.checked)}
            className="h-3.5 w-3.5 rounded border-input accent-primary"
          />
          只看有关系的事件
        </label>
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
          title={hasQuery ? "无匹配关系" : "暂无关系数据"}
          hint={
            hasQuery
              ? "没有事件命中当前关键词。尝试更换或清除模糊搜索条件。"
              : "未解码到 extract 父子或 pair 配对。尝试清除模糊搜索条件，或确认解码插件声明了对应语义规则。"
          }
          className="h-64 justify-center"
        />
      ) : (
        <>
          {/* 父子层级树 */}
          <section>
            <div className="mb-2 flex items-center gap-1.5 text-sm font-medium text-foreground">
              <GitFork className="h-4 w-4 text-primary" />
              extract 父子层级
              <span className="text-[11px] font-normal text-muted-foreground/70">
                {visibleRoots.length} 个顶层事件
              </span>
            </div>
            <div className="rounded-2xl border border-border bg-card/60 p-3">
              {visibleRoots.length === 0 ? (
                <p className="px-1 py-2 text-sm text-muted-foreground">
                  没有参与 extract 父子关系的顶层事件。取消勾选「只看有关系的事件」可查看全部。
                </p>
              ) : (
                <ul className="space-y-0.5">
                  {visibleRoots.map((ev) => (
                    <EventTreeNode key={ev.id} event={ev} childrenOf={childrenOf} depth={0} />
                  ))}
                </ul>
              )}
            </div>
          </section>

          {/* 配对关系 */}
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
        </>
      )}
    </div>
  );
}

// ─── 递归父子树节点 ────────────────────────────────────────────

function EventTreeNode({
  event,
  childrenOf,
  depth,
}: {
  event: DecodedEvent;
  childrenOf: Map<string, DecodedEvent[]>;
  depth: number;
}) {
  const [collapsed, setCollapsed] = useState(false);
  const [showJson, setShowJson] = useState(false);
  const meta = useMemo(() => extractMeta(event.data, event.meta), [event.data, event.meta]);
  const children = childrenOf.get(event.id) ?? [];
  const hasChildren = children.length > 0;

  return (
    <li>
      <div className="flex w-full items-center gap-1.5 rounded-md px-1.5 py-1 hover:bg-muted/40">
        <button
          type="button"
          onClick={() => setCollapsed((v) => !v)}
          disabled={!hasChildren}
          aria-expanded={!collapsed}
          aria-label={hasChildren ? "折叠子事件" : "无子事件"}
          className="flex h-4 w-4 shrink-0 items-center justify-center disabled:opacity-30"
        >
          <ChevronRight
            className={`h-3.5 w-3.5 transition-transform ${!collapsed && hasChildren ? "rotate-90" : ""}`}
          />
        </button>
        <span
          className="font-mono text-[11px] text-muted-foreground/70"
          style={{ minWidth: `${Math.max(0, depth) * 14}px` }}
        />
        <DirectionIcon direction={meta.direction} />
        <MessageCell msgName={meta.msgName} semantic={meta.semantic} />
        <span className="text-[10px] text-muted-foreground/70">{event.protocol}</span>
        {hasChildren && (
          <span className="rounded bg-muted px-1 py-px font-mono text-[10px] text-muted-foreground">
            {children.length}
          </span>
        )}
        {/* 树里只有名字，看不到内容就得回事件表翻——这里直接就地展开 JSON。 */}
        <button
          type="button"
          onClick={() => setShowJson((v) => !v)}
          aria-expanded={showJson}
          className="inline-flex shrink-0 items-center gap-0.5 text-[10px] text-muted-foreground/70 hover:text-foreground"
          title={showJson ? "收起 JSON" : "查看 JSON"}
        >
          <ChevronDown className={`h-3 w-3 transition-transform ${showJson ? "rotate-180" : ""}`} />
          JSON
        </button>
        <span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground">
          {formatTimestamp(event.timestamp)}
        </span>
      </div>
      {showJson && (
        <div className="gt-json-view ml-6 mb-1">
          <HighlightedJson data={event.data} />
        </div>
      )}
      {hasChildren && !collapsed && (
        <ul className="space-y-0.5 pl-4">
          {children.map((child) => (
            <Fragment key={child.id}>
              <EventTreeNode event={child} childrenOf={childrenOf} depth={depth + 1} />
            </Fragment>
          ))}
        </ul>
      )}
    </li>
  );
}
