// DecodedView — 「协议数据」页的多视图容器。
//
// 首个子视图为「连接」（连接列表）：点击某连接会选中该连接为事件过滤条件并
// 直接切到「事件」。其余子视图为事件（表格）/ 关系（父子树+配对）/ 状态变更
// （三视图分析）/ 原始数据（原始帧 hex）。
//
// query（模糊关键词）、direction（消息方向过滤）与 connFilter（连接过滤）均由
// App 顶层过滤栏持有，统一传给所有子视图——每一个子视图都能依据连接/方向/
// 关键词被过滤。
import { useState } from "react";
import { EventTable } from "@/components/event-table";
import { RelationshipView } from "@/components/relationship-view";
import { StateChangeExplorer } from "@/components/state-change-explorer";
import { RawDataView } from "@/components/raw-data-view";
import { ConnectionsPage } from "@/components/connections-page";
import { Network, Table2, GitFork, TableProperties, FileJson2 } from "lucide-react";
import type { DirectionFilter } from "@/lib/fuzzy";
import type { ConnectionSummary } from "@/types/connection";

interface DecodedViewProps {
  sessionId: string | null;
  query: string;
  direction: DirectionFilter;
  /** 连接过滤：全部子视图共享，由顶层过滤栏切换。 */
  connFilter: ConnectionSummary | null;
  onConnFilterChange: (conn: ConnectionSummary | null) => void;
}

type DecodedSubview = "connections" | "events" | "relations" | "state" | "raw";

/** 子视图顺序：连接（首）→ 事件 → 关系 → 状态变更 → 原始数据。 */
const SUBVIEWS: { id: DecodedSubview; label: string; icon: typeof Table2 }[] = [
  { id: "connections", label: "连接", icon: Network },
  { id: "events", label: "事件", icon: Table2 },
  { id: "relations", label: "关系", icon: GitFork },
  { id: "state", label: "状态变更", icon: TableProperties },
  { id: "raw", label: "原始数据", icon: FileJson2 },
];

export function DecodedView({
  sessionId,
  query,
  direction,
  connFilter,
  onConnFilterChange,
}: DecodedViewProps) {
  // 默认落在「连接」子视图（用户从顶部「协议数据」进入首先看到的是连接聚合）。
  const [subview, setSubview] = useState<DecodedSubview>("connections");

  /** 连接列表点击：写入连接过滤（App 持有）并切到「事件」子视图。 */
  const handleSelectConn = (conn: ConnectionSummary) => {
    onConnFilterChange(conn);
    setSubview("events");
  };

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

      {subview === "connections" && (
        <ConnectionsPage sessionId={sessionId} onSelectConn={handleSelectConn} />
      )}
      {subview === "events" && (
        <EventTable sessionId={sessionId} query={query} direction={direction} connFilter={connFilter} />
      )}
      {subview === "relations" && (
        <RelationshipView sessionId={sessionId} query={query} direction={direction} connFilter={connFilter} />
      )}
      {subview === "state" && (
        <StateChangeExplorer sessionId={sessionId} query={query} direction={direction} connFilter={connFilter} />
      )}
      {subview === "raw" && <RawDataView sessionId={sessionId} connFilter={connFilter} />}
    </div>
  );
}