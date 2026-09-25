/**
 * 路由词表：空间层级、会话视图、项目配置页签与 href 生成。
 *
 * 路径即层级 —— workspace（身份级）→ project（项目级，含 #/unassigned 这个
 * 未归属桶）→ session（会话级），
 * 空间由 URL 深度派生，应用内不存在 activeTab 这类"作用域状态"。
 * 左栏内容、面包屑与二级 tab 因此都是 route 的纯函数，任何一层都能单独刷新恢复。
 *
 * 会话空间不携带 projectId：会话归属是后端元数据（SessionInfo.project_id），
 * 写进 URL 就会有两个真相来源（改名/改归属后 URL 说谎）。需要项目上下文时
 * 由会话元数据反查。
 */
import { RAW_DEBUG_ENABLED } from "@/lib/env";

export type Space = "workspace" | "project" | "session";

/** 会话空间的视图（二级 tab）。 */
export type SessionView = "overview" | "connections" | "events" | "states" | "raw";

/** 项目空间主体：会话矩阵 / 配置。 */
export type ProjectSection = "sessions" | "config";

/** 项目配置页签。 */
export type ProjectConfigTab = "members" | "plugins" | "rules";

export interface Route {
  space: Space;
  projectId: string | null;
  sessionId: string | null;
  view: SessionView;
  projectSection: ProjectSection;
  configTab: ProjectConfigTab;
}

/** 视图元数据（图标在 UI 层按 id 映射，此处保持无依赖）。 */
export interface SessionViewMeta {
  id: SessionView;
  label: string;
  hint: string;
}

export const SESSION_VIEWS: readonly SessionViewMeta[] = [
  { id: "overview", label: "概览", hint: "会话状态、抓包阶段与最近数据" },
  { id: "connections", label: "连接", hint: "本次抓包的连接聚合" },
  { id: "events", label: "协议事件", hint: "解码事件与 payload" },
  { id: "states", label: "状态变更", hint: "字段级状态机推演" },
  { id: "raw", label: "原始包", hint: "原始帧 hex（解码调试）" },
];

export const PROJECT_CONFIG_TABS: readonly { id: ProjectConfigTab; label: string }[] = [
  { id: "members", label: "成员" },
  { id: "plugins", label: "解码插件" },
  { id: "rules", label: "分析规则" },
];

/** 「原始包」是解码调试视图，后端未开启 raw-debug 时整个视图不存在。 */
export function isViewAvailable(view: SessionView): boolean {
  return view !== "raw" || RAW_DEBUG_ENABLED;
}

export function visibleSessionViews(): SessionViewMeta[] {
  return SESSION_VIEWS.filter((v) => isViewAvailable(v.id));
}

export const DEFAULT_VIEW: SessionView = "overview";
export const DEFAULT_CONFIG_TAB: ProjectConfigTab = "members";

/** 未知/不可用视图（含关闭状态下的 raw、手改 URL 的垃圾段）回落概览。 */
export function normalizeView(raw: string | undefined | null): SessionView {
  if (raw && SESSION_VIEWS.some((v) => v.id === raw) && isViewAvailable(raw as SessionView)) {
    return raw as SessionView;
  }
  return DEFAULT_VIEW;
}

export function normalizeConfigTab(raw: string | undefined | null): ProjectConfigTab {
  if (raw && PROJECT_CONFIG_TABS.some((t) => t.id === raw)) return raw as ProjectConfigTab;
  return DEFAULT_CONFIG_TAB;
}

export const HOME_ROUTE: Route = {
  space: "workspace",
  projectId: null,
  sessionId: null,
  view: DEFAULT_VIEW,
  projectSection: "sessions",
  configTab: DEFAULT_CONFIG_TAB,
};

export const WORKSPACE_HREF = "#/workspace";

/**
 * 未归属会话桶。它不是项目，但需要与项目同构的空间：
 * 没有它，project_id 为空的会话就没有任何带管理动作（删除/批量删除）的清单可去。
 */
export const UNASSIGNED_HREF = "#/unassigned";

export function projectHref(
  projectId: string,
  section: ProjectSection = "sessions",
  configTab: ProjectConfigTab = DEFAULT_CONFIG_TAB,
): string {
  return section === "config"
    ? `#/project/${encodeURIComponent(projectId)}/config/${configTab}`
    : `#/project/${encodeURIComponent(projectId)}`;
}

/** 项目空间 href；无归属（projectId 为空）落到未归属桶。 */
export function projectSpaceHref(
  projectId: string | null | undefined,
  section: ProjectSection = "sessions",
  configTab: ProjectConfigTab = DEFAULT_CONFIG_TAB,
): string {
  return projectId ? projectHref(projectId, section, configTab) : UNASSIGNED_HREF;
}

export function sessionHref(sessionId: string, view: SessionView = DEFAULT_VIEW): string {
  return `#/session/${encodeURIComponent(sessionId)}/${view}`;
}

/** 路由恒等键：作为副作用依赖（换会话要清过滤，换视图不能清）。 */
export function routeKey(route: Route): string {
  if (route.space === "session") return `session:${route.sessionId}`;
  if (route.space === "project") return `project:${route.projectId}:${route.projectSection}:${route.configTab}`;
  return "workspace";
}
