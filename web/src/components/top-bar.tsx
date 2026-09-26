// TopBar — 顶栏：面包屑（= 空间开关）+ 一级接入动作 + 配置 + 身份。
//
// 这一排刻意不再放任何"视图 tab"：过去「我的抓包 / 会话 / 协议数据」并列在
// 一起，三者却分属身份级 / 会话级 / 会话级，用户无法从导航判断看的是谁的数据。
// 层级交给面包屑，视图交给会话空间的二级条，顶栏只留跨层级都成立的动作。
//
// 接入设备与 MCP 接入必须一级可见（第一次换机器时不该去抽屉里找），
// 其余低频配置收进右侧抽屉。
import { BookOpen, Download, KeyRound, PanelLeft, SlidersHorizontal } from "lucide-react";
import { Breadcrumb } from "@/components/breadcrumb";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { DrawerAction } from "@/components/config-drawer";
import type { Identity } from "@/lib/auth";
import type { Route } from "@/lib/routes";
import type { SessionInfo } from "@/types/session";

interface TopBarProps {
  route: Route;
  projectName: string | null;
  projectId: string | null;
  /** 当前会话（会话空间用；面包屑实时点与身份芯片共用）。 */
  session: SessionInfo | null;
  /** 窄窗口下左栏收成抽屉，由这里的目标按钮展开。 */
  contextOpen: boolean;
  onToggleContext: () => void;
  onSwitchSession: () => void;
  onDevices: () => void;
  onMcpAccess: () => void;
  onDrawer: (action: DrawerAction) => void;
  identity: Identity | null;
}

export function TopBar({
  route,
  projectName,
  projectId,
  session,
  contextOpen,
  onToggleContext,
  onSwitchSession,
  onDevices,
  onMcpAccess,
  onDrawer,
  identity,
}: TopBarProps) {
  return (
    <header className="flex items-center gap-2 border-b border-border bg-card/80 px-3 py-2 backdrop-blur">
      <button
        type="button"
        onClick={onToggleContext}
        aria-expanded={contextOpen}
        aria-label={contextOpen ? "收起侧栏" : "展开侧栏"}
        title={contextOpen ? "收起侧栏" : "展开侧栏"}
        className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <PanelLeft className="h-4 w-4" />
      </button>

      <div className="min-w-0 flex-1">
        <Breadcrumb
          route={route}
          projectName={projectName}
          projectId={projectId}
          sessionRunning={session?.status === "running"}
          onSwitchSession={onSwitchSession}
        />
      </div>

      {/* 会话身份由面包屑最后一段承担：这里再放一枚 id 芯片就是同一排两个真相。 */}

      <Button variant="outline" size="sm" className="h-7 shrink-0" onClick={onDevices}>
        <Download className="h-3.5 w-3.5" />
        <span className="hidden lg:inline">接入设备</span>
      </Button>
      <Button variant="outline" size="sm" className="h-7 shrink-0" onClick={onMcpAccess}>
        <BookOpen className="h-3.5 w-3.5" />
        <span className="hidden lg:inline">MCP 接入</span>
      </Button>

      <div className={cn("h-5 w-px shrink-0 bg-border")} role="presentation" />

      <Button
        variant="ghost"
        size="sm"
        className="h-7 shrink-0"
        onClick={() => onDrawer("overview")}
        title="配置（插件 / 探针 / 成员 / 令牌）"
      >
        <SlidersHorizontal className="h-3.5 w-3.5" />
        <span className="hidden lg:inline">配置</span>
      </Button>
      <Button
        variant="ghost"
        size="sm"
        className="h-7 max-w-[180px] shrink-0"
        onClick={() => onDrawer("account")}
        title={
          identity
            ? `当前身份：${identity.owner}${identity.isAdmin ? "（管理员）" : ""}；点击打开账号与访问令牌`
            : "未登录：点击配置访问令牌或注册身份"
        }
      >
        <KeyRound className="h-3.5 w-3.5" />
        <span className="truncate font-mono text-xs">{identity?.owner ?? "未登录"}</span>
        {identity?.isAdmin && (
          <span className="rounded bg-primary/10 px-1 py-0.5 text-2xs text-primary">admin</span>
        )}
      </Button>
    </header>
  );
}
