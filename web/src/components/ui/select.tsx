import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

/**
 * Select — 项目统一的下拉选择原语（原生 <select> 的样式收敛层）。
 *
 * 刻意保留原生控件：键盘/读屏/移动端滚轮选择全部免费，
 * 页面侧不要再手写 `border border-input text-xs` 这类散装样式，用 size 变体。
 */
const selectVariants = cva(
  "rounded-md border border-input bg-background shadow-sm transition-[border-color,box-shadow] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:border-ring disabled:cursor-not-allowed disabled:opacity-50 [&>option]:bg-popover [&>option]:text-popover-foreground",
  {
    variants: {
      size: {
        default: "h-9 px-3 text-sm",
        sm: "h-8 px-2.5 text-xs",
        micro: "h-7 px-1.5 text-xs",
      },
    },
    defaultVariants: {
      size: "default",
    },
  },
);

export interface SelectProps
  extends
    Omit<React.SelectHTMLAttributes<HTMLSelectElement>, "size">,
    VariantProps<typeof selectVariants> {}

const Select = React.forwardRef<HTMLSelectElement, SelectProps>(
  ({ className, size, ...props }, ref) => (
    <select
      ref={ref}
      className={cn(selectVariants({ size }), className)}
      {...props}
    />
  ),
);
Select.displayName = "Select";

export { Select, selectVariants };
