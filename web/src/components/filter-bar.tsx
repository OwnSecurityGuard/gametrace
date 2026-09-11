// FilterBar — 协议数据页的过滤栏（事件 / 关系 / 状态变更 / 原始数据四视图共享）。
//
// 一行内提供两个过滤条件（叠加 AND）：
//   - 消息方向下拉框：全部 / C→S / S→C；
//   - 模糊搜索输入框：输入过程中防抖（300ms）提交到 App 状态，再分流给四个
//     子视图做纯前端过滤；Enter 立即提交、Esc 清空（不提供「清除」按钮，
//     避免这一行被按钮撑长）。
// 不调用后端 filter 表达式，无 schema/语法提示。
import { type ChangeEvent, type KeyboardEvent, type Ref, useEffect, useRef, useState } from "react";
import { Input } from "@/components/ui/input";
import { Search } from "lucide-react";
import { type DirectionFilter } from "@/lib/fuzzy";

interface FilterBarProps {
  query: string;
  onQueryChange: (query: string) => void;
  direction: DirectionFilter;
  onDirectionChange: (direction: DirectionFilter) => void;
  /** 由父组件传入，用于 "/" 快捷键聚焦输入框 */
  inputRef?: Ref<HTMLInputElement>;
}

const DIRECTION_OPTIONS: { value: DirectionFilter; label: string }[] = [
  { value: "", label: "全部方向" },
  { value: "client_to_server", label: "C→S" },
  { value: "server_to_client", label: "S→C" },
];

export function FilterBar({ query, onQueryChange, direction, onDirectionChange, inputRef }: FilterBarProps) {
  const [local, setLocal] = useState(query);
  const debounceRef = useRef<number | null>(null);

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
    <div className="flex items-center gap-2">
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
      <div className="relative w-full max-w-md">
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
