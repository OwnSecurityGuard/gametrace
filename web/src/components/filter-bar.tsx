// FilterBar — 协议数据页的过滤栏（连接 / 事件 / 关系 / 状态变更 / 原始数据五视图共享）。
//
// 一行内提供三个过滤条件（叠加 AND）：
//   - 连接下拉框：依据连接过滤（代理抓包的连接，按 conn_id 命中）；五个子视图
//     全共享该过滤，即便是「原始数据」也据此选择连接、展示其原始帧；
//   - 消息方向下拉框：全部 / C→S / S→C；
//   - 模糊搜索输入框：输入过程中防抖（300ms）提交到 App 状态，再分流给各
//     子视图做纯前端过滤；Enter 立即提交、Esc 清空（不提供「清除」按钮，
//     避免这一行被按钮撑长）。
// 不调用后端 filter 表达式，无 schema/语法提示。
import { type ChangeEvent, type KeyboardEvent, type Ref, useEffect, useMemo, useRef, useState } from "react";
import { Input } from "@/components/ui/input";
import { Search } from "lucide-react";
import { useConnections } from "@/hooks/use-mcp";
import { type DirectionFilter } from "@/lib/fuzzy";
import type { ConnectionSummary } from "@/types/connection";

interface FilterBarProps {
  sessionId: string | null;
  query: string;
  onQueryChange: (query: string) => void;
  direction: DirectionFilter;
  onDirectionChange: (direction: DirectionFilter) => void;
  /** 连接过滤（null = 全部连接）；协议数据全部子视图共享。 */
  connFilter: ConnectionSummary | null;
  onConnFilterChange: (conn: ConnectionSummary | null) => void;
  /** 由父组件传入，用于 "/" 快捷键聚焦输入框 */
  inputRef?: Ref<HTMLInputElement>;
}

const DIRECTION_OPTIONS: { value: DirectionFilter; label: string }[] = [
  { value: "", label: "全部方向" },
  { value: "client_to_server", label: "C→S" },
  { value: "server_to_client", label: "S→C" },
];

export function FilterBar({
  sessionId,
  query,
  onQueryChange,
  direction,
  onDirectionChange,
  connFilter,
  onConnFilterChange,
  inputRef,
}: FilterBarProps) {
  const [local, setLocal] = useState(query);
  const debounceRef = useRef<number | null>(null);

  // 连接下拉候选：来自 list_connections（仅代理抓包有连接）。
  const { data: connsData } = useConnections(sessionId, { limit: 100 });
  const connOptions = useMemo(() => connsData?.connections ?? [], [connsData]);

  // 外部 query 变化（切换会话清空）同步回本地输入
  useEffect(() => {
    setLocal(query);
  }, [query]);

  // 本地输入变化后防抖提交（300ms），避免每次按键都触发过滤
  useEffect(() => {
    if (local === query) return;
    if (debounceRef.current) window.clearTimeout(debounceRef.current);
    debounceRef.current = window.setTimeout(() => {
      onQueryChange(local);
    }, 300);
    return () => {
      if (debounceRef.current) window.clearTimeout(debounceRef.current);
    };
  }, [local, query, onQueryChange]);

  function flush() {
    if (debounceRef.current) window.clearTimeout(debounceRef.current);
    onQueryChange(local);
  }

  function handleKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter") {
      flush();
    } else if (e.key === "Escape") {
      if (debounceRef.current) window.clearTimeout(debounceRef.current);
      setLocal("");
      onQueryChange("");
    }
  }

  function handleInputChange(e: ChangeEvent<HTMLInputElement>) {
    setLocal(e.target.value);
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      {/* 连接过滤下拉：仅存在代理连接时显示，五个子视图共享 */}
      {connOptions.length > 0 && (
        <select
          value={connFilter?.conn_id ?? ""}
          onChange={(e) => {
            const id = e.target.value;
            onConnFilterChange(connOptions.find((c) => c.conn_id === id) ?? null);
          }}
          className="h-9 shrink-0 max-w-[40vw] rounded-md border border-input bg-background px-2 text-sm sm:max-w-56"
          aria-label="连接过滤"
          title={connFilter ? `${connFilter.client || "-"} → ${connFilter.server || "-"}` : "全部连接"}
        >
          <option value="">全部连接</option>
          {connOptions.map((c) => (
            <option key={c.conn_id} value={c.conn_id}>
              {c.client || "-"} → {c.server || "-"}
            </option>
          ))}
        </select>
      )}
      <select
        value={direction}
        onChange={(e) => onDirectionChange(e.target.value as DirectionFilter)}
        className="h-9 shrink-0 rounded-md border border-input bg-background px-2 text-sm"
        aria-label="消息方向过滤"
      >
        {DIRECTION_OPTIONS.map((opt) => (
          <option key={opt.value} value={opt.value}>
            {opt.label}
          </option>
        ))}
      </select>
      <div className="relative w-full max-w-md min-w-40">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          ref={inputRef}
          value={local}
          onChange={handleInputChange}
          onKeyDown={handleKeyDown}
          aria-label="模糊搜索"
          placeholder="模糊搜索：关键词 / 消息名 / id（Esc 清空）"
          className="pl-9 font-mono"
        />
      </div>
    </div>
  );
}
