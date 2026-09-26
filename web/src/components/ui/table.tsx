import * as React from "react";
import { cn } from "@/lib/utils";

/**
 * containerClassName 让调用方能摘掉外层滚动容器（默认 `overflow-auto`）。
 *
 * 原因：`.gt-table thead th` 的吸顶是 sticky，而 sticky 只相对「最近的滚动容器」生效。
 * 外层 div 一旦自己 overflow-auto，高度又被内容撑开（永不纵向滚动），表头就永远吸不住。
 * 想让表头吸顶，要么给这个容器限高，要么把它交给页面级滚动容器（传 containerClassName）。
 */
const Table = React.forwardRef<
  HTMLTableElement,
  React.HTMLAttributes<HTMLTableElement> & { containerClassName?: string }
>(({ className, containerClassName, ...props }, ref) => (
  <div className={cn("relative w-full overflow-auto", containerClassName)}>
    <table ref={ref} className={cn("w-full caption-bottom text-sm", className)} {...props} />
  </div>
));
Table.displayName = "Table";

const TableHeader = React.forwardRef<HTMLTableSectionElement, React.HTMLAttributes<HTMLTableSectionElement>>(
  ({ className, ...props }, ref) => (
    <thead ref={ref} className={cn("[&_tr]:border-b", className)} {...props} />
  ),
);
TableHeader.displayName = "TableHeader";

const TableBody = React.forwardRef<HTMLTableSectionElement, React.HTMLAttributes<HTMLTableSectionElement>>(
  ({ className, ...props }, ref) => (
    <tbody ref={ref} className={cn("[&_tr:last-child]:border-0", className)} {...props} />
  ),
);
TableBody.displayName = "TableBody";

/**
 * 可点击行原语：只要传了 onClick，TableRow 自动补齐键盘可达性——
 * tabIndex=0、Enter/Space 触发、焦点环（inset，避免被相邻行裁掉）。
 * 调用方无需（也不应）再手写 tabIndex/onKeyDown，修一次到处生效。
 */
const TableRow = React.forwardRef<HTMLTableRowElement, React.HTMLAttributes<HTMLTableRowElement>>(
  ({ className, onClick, ...props }, ref) => {
    const interactive = !!onClick;
    return (
      <tr
        ref={ref}
        className={cn(
          "border-b transition-[background-color,color] hover:bg-muted/60 data-[state=selected]:bg-primary-muted",
          interactive &&
            "cursor-pointer select-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
          className,
        )}
        onClick={onClick}
        onKeyDown={
          interactive
            ? (e) => {
                if (e.target !== e.currentTarget) return;
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  onClick(e as unknown as React.MouseEvent<HTMLTableRowElement>);
                }
              }
            : undefined
        }
        {...(interactive ? { tabIndex: 0 } : {})}
        {...props}
      />
    );
  },
);
TableRow.displayName = "TableRow";

const TableHead = React.forwardRef<HTMLTableCellElement, React.ThHTMLAttributes<HTMLTableCellElement>>(
  ({ className, ...props }, ref) => (
    <th
      ref={ref}
      className={cn(
        "h-10 px-3 text-left align-middle text-xs font-semibold uppercase tracking-wide text-muted-foreground whitespace-nowrap select-none [&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
        className,
      )}
      {...props}
    />
  ),
);
TableHead.displayName = "TableHead";

const TableCell = React.forwardRef<HTMLTableCellElement, React.TdHTMLAttributes<HTMLTableCellElement>>(
  ({ className, ...props }, ref) => (
    <td
      ref={ref}
      className={cn(
        "px-3 py-2.5 align-middle [&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
        className,
      )}
      {...props}
    />
  ),
);
TableCell.displayName = "TableCell";

export { Table, TableHeader, TableBody, TableRow, TableHead, TableCell };
