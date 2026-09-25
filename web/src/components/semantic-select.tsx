// SemanticSelect — 协议事件页的语义标签多选下拉。
//
// 为什么不是闭集下拉框：语义由每个项目的解码插件用 SDK annotate 声明，
// 词表和数量都不固定（同一个插件改版就会变），写死 request/response/... 的
// 选项列表迟早和真实数据脱节。所以候选词来自 get_protocol_catalog 的实际聚合
// 结果（useSemanticVocabulary），插件声明什么就出现什么。
//
// 过滤在后端 list_decoded_data 的 semantics 参数完成（多值按"或"合并）：
// meta.semantic 藏在 msgpack payload 里，SQL 下推不了，只能服务端流式过滤。
import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown } from "lucide-react";
import { useSemanticVocabulary } from "@/hooks/use-semantic-vocabulary";
import { cn } from "@/lib/utils";

interface SemanticSelectProps {
  sessionId: string | null;
  value: string[];
  onChange: (value: string[]) => void;
}

export function SemanticSelect({ sessionId, value, onChange }: SemanticSelectProps) {
  const { labels, counts, isLoading } = useSemanticVocabulary(sessionId);
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: PointerEvent) {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") setOpen(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const selected = new Set(value);
  const toggle = (label: string) => {
    const next = new Set(selected);
    if (next.has(label)) next.delete(label);
    else next.add(label);
    onChange(labels.filter((l) => next.has(l)));
  };

  const summary =
    value.length === 0 ? "全部语义" : value.length === 1 ? value[0] : `语义 · ${value.length} 项`;

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label="语义标签过滤"
        onClick={() => setOpen((v) => !v)}
        className={cn(
          "inline-flex h-9 items-center gap-1.5 rounded-md border border-input bg-background px-2 text-sm transition-colors hover:border-ring/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
          value.length > 0 && "border-primary/50 bg-primary-muted text-primary",
        )}
      >
        <span className="max-w-[10rem] truncate">{summary}</span>
        <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
      </button>

      {open && (
        <div
          role="listbox"
          aria-multiselectable
          className="absolute left-0 top-[calc(100%+4px)] z-30 w-64 rounded-lg border border-border bg-popover p-1 shadow-lg gt-pop-in"
        >
          <div className="flex items-center justify-between gap-2 px-1.5 py-1">
            <span className="text-[11px] text-muted-foreground">
              {isLoading ? "加载词表…" : `${labels.length} 个标签`}
            </span>
            <div className="flex items-center gap-1">
              <button
                type="button"
                className="rounded px-1.5 py-0.5 text-[11px] text-primary hover:bg-accent disabled:opacity-40"
                disabled={labels.length === 0}
                onClick={() => onChange([...labels])}
              >
                全选
              </button>
              <button
                type="button"
                className="rounded px-1.5 py-0.5 text-[11px] text-muted-foreground hover:bg-muted disabled:opacity-40"
                disabled={value.length === 0}
                onClick={() => onChange([])}
              >
                清空
              </button>
            </div>
          </div>

          <div className="max-h-64 overflow-auto gt-scroll">
            {labels.length === 0 && !isLoading && (
              <p className="px-1.5 py-2 text-[11px] leading-relaxed text-muted-foreground">
                本次抓包的插件没有声明语义标签（annotate），因此无可选项。
              </p>
            )}
            {labels.map((label) => {
              const on = selected.has(label);
              return (
                <button
                  key={label}
                  type="button"
                  role="option"
                  aria-selected={on}
                  onClick={() => toggle(label)}
                  className="flex w-full items-center gap-2 rounded px-1.5 py-1 text-left text-sm transition-colors hover:bg-muted"
                >
                  <span
                    className={cn(
                      "flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded border",
                      on ? "border-primary bg-primary text-primary-foreground" : "border-input",
                    )}
                  >
                    {on && <Check className="h-2.5 w-2.5" />}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-mono text-xs">{label}</span>
                  {counts[label] != null && (
                    <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                      {counts[label].toLocaleString()}
                    </span>
                  )}
                </button>
              );
            })}
          </div>

          <p className="border-t border-border px-1.5 pb-0.5 pt-1 text-[11px] text-muted-foreground">
            未选中即不过滤；选中多项按「或」合并。
          </p>
        </div>
      )}
    </div>
  );
}
