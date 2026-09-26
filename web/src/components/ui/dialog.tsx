import * as React from "react";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import { useFocusTrap } from "@/hooks/use-focus-trap";

interface DialogProps {
  open: boolean;
  onClose: () => void;
  title?: React.ReactNode;
  description?: React.ReactNode;
  icon?: React.ReactNode;
  children?: React.ReactNode;
  footer?: React.ReactNode;
  className?: string;
  /** 是否显示右上角关闭按钮，默认 true */
  showClose?: boolean;
}

/** 通用对话框：无障碍（role/aria/ESC/焦点陷阱/滚动锁定）+ 入场动画 + overscroll 约束。 */
export function Dialog({
  open,
  onClose,
  title,
  description,
  icon,
  children,
  footer,
  className,
  showClose = true,
}: DialogProps) {
  const titleId = React.useId();
  const descId = React.useId();
  const panelRef = useFocusTrap(open, onClose);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby={title ? titleId : undefined}
      aria-describedby={description ? descId : undefined}
    >
      {/* 遮罩 */}
      <div
        className="absolute inset-0 bg-slate-950/45 backdrop-blur-[2px] gt-fade-in"
        onClick={onClose}
      />

      {/* 对话框面板 */}
      <div
        ref={panelRef}
        tabIndex={-1}
        className={cn(
          "relative z-10 w-full max-w-md rounded-xl border border-border bg-popover text-popover-foreground shadow-xl outline-none gt-pop-in",
          "overscroll-contain max-h-[90vh] flex flex-col",
          className,
        )}
      >
        {(title || showClose) && (
          <div className="flex items-start gap-3 border-b border-border px-5 py-4">
            {icon && (
              <div className="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary-muted text-primary">
                {icon}
              </div>
            )}
            <div className="min-w-0 flex-1">
              {title && (
                <h2 id={titleId} className="text-base font-semibold text-balance">
                  {title}
                </h2>
              )}
              {description && (
                <p id={descId} className="mt-0.5 text-xs text-muted-foreground">
                  {description}
                </p>
              )}
            </div>
            {showClose && (
              <button
                type="button"
                aria-label="关闭"
                onClick={onClose}
                className="-mr-1 -mt-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <X className="h-4 w-4" />
              </button>
            )}
          </div>
        )}

        <div className="overflow-y-auto px-5 py-4 gt-scroll">{children}</div>

        {footer && (
          <div className="flex items-center justify-end gap-2 border-t border-border px-5 py-3.5">
            {footer}
          </div>
        )}
      </div>
    </div>
  );
}
