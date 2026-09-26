// FilterBar — 协议事件页的过滤栏（连接 / 方向 / 语义 / 关键词 / 剔除，叠加为 AND）。
//
// 一行内提供五个过滤条件：
//   - 连接下拉框：依据连接过滤（代理抓包的连接，按 conn_id 命中）；会话空间的
//     五个视图共享该过滤，即便是「原始包」也据此选择连接、展示其原始帧；
//   - 消息方向下拉框：全部 / C→S / S→C；
//   - 语义多选下拉：词表来自本次抓包的实际聚合结果，不是写死闭集（见 semantic-select.tsx）；
//   - 模糊搜索输入框：输入过程中防抖（300ms）提交，Enter 立即提交、Esc 清空
//     （不提供「清除」按钮，避免这一行被按钮撑长）；
//   - 剔除输入框：与搜索看同一份字段包，但多值按「或」——命中任一词即隐藏该条，
//     用来把刷屏的消息名整类剔掉。
// 不调用后端 filter 表达式，无 schema/语法提示。
import { type KeyboardEvent, type Ref, useEffect, useMemo, useRef, useState } from "react";
import { Input } from "@/components/ui/input";
import { SemanticSelect } from "@/components/semantic-select";
import { EyeOff, Search } from "lucide-react";
import { useConnections } from "@/hooks/use-mcp";
import { type DirectionFilter, type SemanticFilter } from "@/lib/fuzzy";
import type { ConnectionSummary } from "@/types/connection";
import { Select } from "@/components/ui/select";

interface FilterBarProps {
  sessionId: string | null;
  query: string;
  onQueryChange: (query: string) => void;
  /** 剔除关键词（逗号或空白分隔，命中任一即隐藏）；空串 = 不剔除。 */
  exclude: string;
  onExcludeChange: (exclude: string) => void;
  direction: DirectionFilter;
  onDirectionChange: (direction: DirectionFilter) => void;
  /** 语义标签过滤集合（SDK annotate 声明）；空数组 = 不过滤，多值按「或」合并。 */
  semantic: SemanticFilter;
  onSemanticChange: (semantic: SemanticFilter) => void;
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

/**
 * 单个文本过滤条件：本地输入 300ms 防抖提交，外部值变化（换会话清空）回流本地，
 * Enter 立即提交、Esc 清空。搜索框与剔除框共用，避免把这套时序写两遍。
 */
function useDebouncedField(value: string, onCommit: (next: string) => void) {
  const [local, setLocal] = useState(value);
  const timerRef = useRef<number | null>(null);

  function cancel() {
    if (timerRef.current) window.clearTimeout(timerRef.current);
    timerRef.current = null;
  }

  useEffect(() => {
    setLocal(value);
  }, [value]);

  useEffect(() => {
    if (local === value) return;
    cancel();
    timerRef.current = window.setTimeout(() => onCommit(local), 300);
    return cancel;
  }, [local, value, onCommit]);

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter") {
      cancel();
      onCommit(local);
    } else if (e.key === "Escape") {
      cancel();
      setLocal("");
      onCommit("");
    }
  }

  return { local, setLocal, onKeyDown };
}

export function FilterBar({
  sessionId,
  query,
  onQueryChange,
  exclude,
  onExcludeChange,
  direction,
  onDirectionChange,
  semantic,
  onSemanticChange,
  connFilter,
  onConnFilterChange,
  inputRef,
}: FilterBarProps) {
  // 连接下拉候选：来自 list_connections（仅代理抓包有连接）。
  const { data: connsData } = useConnections(sessionId, { limit: 100 });
  const connOptions = useMemo(() => connsData?.connections ?? [], [connsData]);

  const search = useDebouncedField(query, onQueryChange);
  const excludeField = useDebouncedField(exclude, onExcludeChange);

  return (
    <div className="flex flex-wrap items-center gap-2">
      {/* 连接过滤下拉：仅存在代理连接时显示，五个子视图共享 */}
      {connOptions.length > 0 && (
        <Select
          value={connFilter?.conn_id ?? ""}
          onChange={(e) => {
            const id = e.target.value;
            onConnFilterChange(connOptions.find((c) => c.conn_id === id) ?? null);
          }}
          size="default" className="shrink-0 max-w-[40vw] sm:max-w-56"
          aria-label="连接过滤"
          title={connFilter ? `${connFilter.client || "-"} → ${connFilter.server || "-"}` : "全部连接"}
        >
          <option value="">全部连接</option>
          {connOptions.map((c) => (
            <option key={c.conn_id} value={c.conn_id}>
              {c.client || "-"} → {c.server || "-"}
            </option>
          ))}
        </Select>
      )}
      <Select
        value={direction}
        onChange={(e) => onDirectionChange(e.target.value as DirectionFilter)}
        size="default" className="shrink-0"
        aria-label="消息方向过滤"
      >
        {DIRECTION_OPTIONS.map((opt) => (
          <option key={opt.value} value={opt.value}>
            {opt.label}
          </option>
        ))}
      </Select>
      <SemanticSelect sessionId={sessionId} value={semantic} onChange={onSemanticChange} />
      <div className="relative min-w-40 max-w-md flex-1 basis-56">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          ref={inputRef}
          value={search.local}
          onChange={(e) => search.setLocal(e.target.value)}
          onKeyDown={search.onKeyDown}
          aria-label="模糊搜索"
          placeholder="模糊搜索：关键词 / 消息名 / id（Esc 清空）"
          className="pl-9 font-mono"
        />
      </div>
      <div className="relative min-w-40 max-w-md flex-1 basis-56">
        <EyeOff className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={excludeField.local}
          onChange={(e) => excludeField.setLocal(e.target.value)}
          onKeyDown={excludeField.onKeyDown}
          aria-label="剔除关键词"
          placeholder="剔除：命中任一即隐藏（逗号或空格分隔）"
          className="pl-9 font-mono"
        />
      </div>
    </div>
  );
}
