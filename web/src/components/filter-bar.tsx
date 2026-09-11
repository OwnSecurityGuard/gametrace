// FilterBar — 协议数据页的模糊查询输入框（事件 / 关系 / 状态变更三视图共享）。
//
// 已从「筛选表达式 + 快捷字段标签」简化为单一模糊搜索框：
//   - 输入过程中防抖（300ms）提交到 App 状态，再分流给三个子视图做纯前端过滤；
//   - Enter 立即提交、Esc 清空、输入框右侧「清除」按钮一键清空；
//   - 不调用后端 filter 表达式，无 schema/语法提示。
import { type ChangeEvent, type KeyboardEvent, type Ref, useEffect, useRef, useState } from "react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Search, X } from "lucide-react";

interface FilterBarProps {
  query: string;
  onQueryChange: (query: string) => void;
  /** 由父组件传入，用于 "/" 快捷键聚焦输入框 */
  inputRef?: Ref<HTMLInputElement>;
}

export function FilterBar({ query, onQueryChange, inputRef }: FilterBarProps) {
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

  function handleClear() {
    if (debounceRef.current) window.clearTimeout(debounceRef.current);
    setLocal("");
    onQueryChange("");
  }

  return (
    <div className="space-y-2.5">
      <div className="flex items-center gap-2">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            ref={inputRef}
            value={local}
            onChange={handleInputChange}
            onKeyDown={handleKeyDown}
            aria-label="模糊搜索"
            placeholder="输入关键词模糊搜索，空格分隔多个词（AND），如 playerId 1001 / PlayerMove / server_to_client…"
            className="pl-9 font-mono"
          />
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={handleClear}
          disabled={!local}
          aria-label="清除搜索"
        >
          <X className="h-3 w-3" />
          清除
        </Button>
      </div>
    </div>
  );
}