// DecodedView — 「协议数据」页的三视图容器。
//
// 从单一事件表格升级为内部三分段：事件（表格）/ 关系（父子树+配对）/ 状态变更（三视图分析）。
// 沿用 App 顶部 segmented tab 样式，filter 仅影响事件与关系两个基于事件的子视图。
import { useState } from "react";
import { EventTable } from "@/components/event-table";
import { RelationshipView } from "@/components/relationship-view";
import { StateChangeExplorer } from "@/components/state-change-explorer";
import { Table2, GitFork, TableProperties } from "lucide-react";

interface DecodedViewProps {
  sessionId: string | null;
  filter: string;
  onFilterChange: (filter: string) => void;
}

type DecodedSubview = "events" | "relations" | "state";

const SUBVIEWS: { id: DecodedSubview; label: string; icon: typeof Table2 }[] = [
  { id: "events", label: "事件", icon: Table2 },
  { id: "relations", label: "关系", icon: GitFork },
  { id: "state", label: "状态变更", icon: TableProperties },
];

export function DecodedView({ sessionId, filter, onFilterChange }: DecodedViewProps) {
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

      {subview === "events" && (
        <EventTable sessionId={sessionId} filter={filter} onFilterChange={onFilterChange} />
      )}
      {subview === "relations" && <RelationshipView sessionId={sessionId} filter={filter} />}
      {subview === "state" && <StateChangeExplorer sessionId={sessionId} />}
    </div>
  );
}