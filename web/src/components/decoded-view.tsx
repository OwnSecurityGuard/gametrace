// DecodedView — 「协议数据」页的四视图容器。
//
// 从单一事件表格升级为内部分段：事件（表格）/ 关系（父子树+配对）/ 状态变更
// （三视图分析）/ 原始数据（完整 JSON）。query（模糊关键词）与 direction（消息
// 方向过滤）由 App 传入并在四个子视图间共享：事件 / 关系 / 原始数据在已加载
// 事件上做内存过滤，状态变更在 changes 流上做内存过滤。
import { useState } from "react";
import { EventTable } from "@/components/event-table";
import { RelationshipView } from "@/components/relationship-view";
import { StateChangeExplorer } from "@/components/state-change-explorer";
import { RawDataView } from "@/components/raw-data-view";
import { Table2, GitFork, TableProperties, FileJson2 } from "lucide-react";
import type { DirectionFilter } from "@/lib/fuzzy";

interface DecodedViewProps {
  sessionId: string | null;
  query: string;
  direction: DirectionFilter;
}

type DecodedSubview = "events" | "relations" | "state" | "raw";

const SUBVIEWS: { id: DecodedSubview; label: string; icon: typeof Table2 }[] = [
  { id: "events", label: "事件", icon: Table2 },
  { id: "relations", label: "关系", icon: GitFork },
  { id: "state", label: "状态变更", icon: TableProperties },
  { id: "raw", label: "原始数据", icon: FileJson2 },
];

export function DecodedView({ sessionId, query, direction }: DecodedViewProps) {
  const [subview, setSubview] = useState<DecodedSubview>("events");

  return (
    <div className="flex h-full flex-col">
      {/* 内部分段切换 */}
      <div
        role="tablist"
        aria-label="协议数据视图"
        className="mb-3 inline-flex w-fit items-center gap-1 rounded-lg bg-muted p-1"
      >
        {SUBVIEWS.map(({ id, label, icon: Icon }) => {
          const selected = subview === id;
          return (
            <button
              key={id}
              role="tab"
              aria-selected={selected}
              onClick={() => setSubview(id)}
              className={
                "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                (selected
                  ? "bg-card text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground")
              }
            >
              <Icon className="h-3.5 w-3.5" />
              {label}
            </button>
          );
        })}
      </div>

      {subview === "events" && <EventTable sessionId={sessionId} query={query} direction={direction} />}
      {subview === "relations" && <RelationshipView sessionId={sessionId} query={query} direction={direction} />}
      {subview === "state" && <StateChangeExplorer sessionId={sessionId} query={query} direction={direction} />}
      {subview === "raw" && <RawDataView sessionId={sessionId} query={query} direction={direction} />}
    </div>
  );
}