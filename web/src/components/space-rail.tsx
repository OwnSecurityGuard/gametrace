// SpaceRail — 最左侧全局控制台。
//
// 只放两样东西：空间跳转（工作台 / 项目）与全局开关（主题 / 配置抽屉）。
// 它不属于任何作用域，所以左栏随空间换内容时它是恒定不变的锚点 ——
// 用户靠它确认"我还在同一个平台里"，靠面包屑确认"我在哪一层"。
import { Folder, LayoutGrid, Plug, SlidersHorizontal, Sun, Moon, Users } from "lucide-react";
import { navigate } from "@/lib/router";
import { WORKSPACE_HREF, projectHref, type Space } from "@/lib/routes";
import type { DrawerAction } from "@/components/config-drawer";
import { cn } from "@/lib/utils";

interface SpaceRailProps {
  space: Space;
  /** 「项目」按钮的去向：最近一个项目；没有项目时回工作台。 */
  homeProjectId: string | null;
  runningCount: number;
  isDark: boolean;
  onToggleTheme: () => void;
  onDrawer: (action: DrawerAction) => void;
}

const BTN =
  "relative flex h-9 w-9 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40";

export function SpaceRail({
  space,
  homeProjectId,
  runningCount,
  isDark,
  onToggleTheme,
  onDrawer,
}: SpaceRailProps) {
  const inWorkspace = space === "workspace";
  return (
    <nav
      aria-label="全局控制台"
      className="flex w-14 shrink-0 flex-col items-center gap-1 border-r border-border bg-card py-3"
    >
      <button
        type="button"
        aria-label="GameTrace 工作台"
        onClick={() => navigate(WORKSPACE_HREF)}
        className={cn(BTN, "mb-1 h-10 w-10 overflow-hidden")}
      >
        <img src="/logo.png" alt="" className="h-full w-full object-contain" />
      </button>

      <button
        type="button"
        title="工作台"
        aria-current={inWorkspace ? "page" : undefined}
        onClick={() => navigate(WORKSPACE_HREF)}
        className={cn(BTN, inWorkspace && "bg-primary-muted text-primary")}
      >
        <LayoutGrid className="h-[18px] w-[18px]" />
        {runningCount > 0 && (
          <span className="absolute right-0.5 top-0.5 min-w-[16px] rounded-full bg-success px-1 text-center font-mono text-[10px] leading-[16px] text-success-foreground">
            {runningCount}
          </span>
        )}
      </button>

      <button
        type="button"
        title="项目"
        aria-current={!inWorkspace ? "page" : undefined}
        onClick={() => navigate(homeProjectId ? projectHref(homeProjectId) : WORKSPACE_HREF)}
        className={cn(BTN, !inWorkspace && "bg-primary-muted text-primary")}
      >
        <Folder className="h-[18px] w-[18px]" />
      </button>

      <div className="my-1 h-px w-6 bg-border" role="presentation" />

      <button
        type="button"
        title="解码插件"
        onClick={() => onDrawer("plugins")}
        className={BTN}
      >
        <Plug className="h-[18px] w-[18px]" />
      </button>
      <button
        type="button"
        title="成员管理"
        onClick={() => onDrawer("members")}
        className={BTN}
      >
        <Users className="h-[18px] w-[18px]" />
      </button>

      <div className="flex-1" />

      <button
        type="button"
        aria-label={isDark ? "切换为亮色模式" : "切换为暗色模式"}
        title={isDark ? "切换亮色" : "切换暗色"}
        onClick={onToggleTheme}
        className={BTN}
      >
        {isDark ? <Sun className="h-[18px] w-[18px]" /> : <Moon className="h-[18px] w-[18px]" />}
      </button>
      <button
        type="button"
        title="配置（插件 / 探针 / 成员 / 令牌）"
        aria-label="配置"
        onClick={() => onDrawer("overview")}
        className={BTN}
      >
        <SlidersHorizontal className="h-[18px] w-[18px]" />
      </button>
    </nav>
  );
}
