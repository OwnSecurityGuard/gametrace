// Breadcrumb — 顶栏面包屑，同时就是空间开关。
//
// 三段对应三个作用域层级（身份 / 项目 / 会话），当前所在层级以高亮区分。
// 它是唯一的层级入口：过去顶部那条「我的抓包 · 会话 · 协议数据」把三种层级
// 不同的东西并列在一起，用户无法从导航判断自己看到的到底是谁的数据。
import { ChevronRight, Folder, LayoutGrid, Radio } from "lucide-react";
import { navigate } from "@/lib/router";
import { WORKSPACE_HREF, projectSpaceHref, type Route } from "@/lib/routes";
import { cn } from "@/lib/utils";

interface BreadcrumbProps {
  route: Route;
  /** 会话所属项目名（会话空间用会话元数据反查；无归属时为空）。 */
  projectName: string | null;
  projectId: string | null;
  /** 当前会话是否在跑（面包屑会话段的实时点）。 */
  sessionRunning?: boolean;
  onSwitchSession: () => void;
}

// min-w-0 是让长项目名/会话 id 省略号截断而不是把按钮挤成一列竖排文字的前提。
const CRUMB =
  "inline-flex h-7 max-w-[220px] min-w-0 items-center gap-1.5 rounded-md px-2 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function Breadcrumb({
  route,
  projectName,
  projectId,
  sessionRunning,
  onSwitchSession,
}: BreadcrumbProps) {
  const { space } = route;
  return (
    <nav aria-label="当前位置" className="flex min-w-0 items-center gap-0.5">
      <button
        type="button"
        title="工作台 · 身份级"
        aria-current={space === "workspace" ? "page" : undefined}
        onClick={() => navigate(WORKSPACE_HREF)}
        className={cn(CRUMB, "shrink-0", space === "workspace" && "font-medium text-foreground")}
      >
        <LayoutGrid className="h-3.5 w-3.5" />
        工作台
      </button>

      {space !== "workspace" && (
        <>
          <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden />
          <button
            type="button"
            title="项目 · 项目级"
            aria-current={space === "project" ? "page" : undefined}
            onClick={() => navigate(projectSpaceHref(projectId))}
            className={cn(CRUMB, space === "project" && "font-medium text-foreground")}
          >
            <Folder className="h-3.5 w-3.5" />
            <span className="truncate">{projectName ?? "未归属抓包"}</span>
          </button>
        </>
      )}

      {space === "session" && route.sessionId && (
        <>
          <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden />
          <button
            type="button"
            title="会话 · 会话级 · 点击切换会话"
            aria-current="page"
            onClick={onSwitchSession}
            className={cn(CRUMB, "font-mono text-xs font-medium text-foreground")}
          >
            <Radio className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">{route.sessionId}</span>
            {sessionRunning && (
              <span className="gt-live-dot shrink-0">
                <span className="sr-only">运行中</span>
              </span>
            )}
          </button>
        </>
      )}
    </nav>
  );
}
