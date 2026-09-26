// App — 应用外壳：一切都由路由派生，没有 activeTab。
//
// 三个空间（工作台 / 项目 / 会话）由 URL 深度决定，左栏、面包屑、视图条与主区域
// 都是 route 的纯函数（见 lib/routes.ts）。这是本次 IA 重构的落地点：过去顶栏那条
// 「我的抓包 / 会话 / 协议数据」把身份级、会话级、会话级三样东西并列，
// 现在层级只由地址决定，刷新/后退/新标签打开都能恢复到原位。
import { useCallback, useEffect, useRef, useState } from "react";
import { KeyRound, Plug } from "lucide-react";
import { SpaceRail } from "@/components/space-rail";
import { ContextPanel } from "@/components/context-panel";
import { TopBar } from "@/components/top-bar";
import { SessionSubbar } from "@/components/session-subbar";
import { FilterBar } from "@/components/filter-bar";
import { ConfigDrawer, type DrawerAction, type DrawerSection } from "@/components/config-drawer";
import { WorkspacePage } from "@/components/workspace-page";
import { ProjectPage } from "@/components/project-page";
import { UnassignedPage } from "@/components/unassigned-page";
import { AuthorizePage } from "@/components/authorize-page";
import { SessionOverviewPage } from "@/components/session-overview-page";
import { ConnectionsPage } from "@/components/connections-page";
import { EventTable } from "@/components/event-table";
import { StateChangeExplorer } from "@/components/state-change-explorer";
import { RawPacketTable } from "@/components/raw-packet-table";
import { SessionAlertsPanel } from "@/components/session-alerts-panel";
import { PluginPanel } from "@/components/plugin-panel";
import { SettingsDialog } from "@/components/settings-dialog";
import { StartCaptureDialog } from "@/components/start-capture-dialog";
import { ProxyConfigDialog } from "@/components/proxy-config-dialog";
import { AgentDownloadDialog } from "@/components/agent-download-dialog";
import { McpAccessDialog } from "@/components/mcp-access-dialog";
import { MembersAdminDialog } from "@/components/members-admin-dialog";
import { ProbeAdminDialog } from "@/components/probe-admin-dialog";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { toast } from "@/components/ui/toast";
import { useAuthError, useIdentity } from "@/hooks/use-auth";
import { usePluginEventStream, useSessions, useProjects, useStopCapture } from "@/hooks/use-mcp";
import { navigate, useRoute } from "@/lib/router";
import { WORKSPACE_HREF, projectSpaceHref, sessionHref, type SessionView } from "@/lib/routes";
import { cn } from "@/lib/utils";
import type { ConnectionSummary } from "@/types/connection";
import type { DirectionFilter, SemanticFilter } from "@/lib/fuzzy";

export default function App() {
  // OAuth 授权深链接（agent 拉起的浏览器页，后端 /oauth/authorize 返回的 SPA）：
  // 不渲染主应用，只渲染授权页。无路由库，沿用单文件路径判断。
  if (window.location.pathname === "/oauth/authorize") {
    return <AuthorizePage />;
  }
  return <Workbench />;
}

function Workbench() {
  const route = useRoute();
  const { data: sessionsData } = useSessions();
  const { data: projectsData } = useProjects();
  const sessions = sessionsData?.sessions ?? [];
  const projects = projectsData?.projects ?? [];

  // —— 视图内状态（不进 URL：筛选条件属于一次查看，不是位置）——
  const [query, setQuery] = useState("");
  // 剔除关键词：与 query 同一份字段包，但多值「或」——命中即隐藏。
  const [exclude, setExclude] = useState("");
  const [direction, setDirection] = useState<DirectionFilter>("");
  const [semantics, setSemantics] = useState<SemanticFilter>([]);
  const [connFilter, setConnFilter] = useState<ConnectionSummary | null>(null);
  // 跨视图落点：状态变更视图跳来时只看那一条消息。与 connFilter 同类——
  // 一次查看的状态，不进 URL（刷新后回到正常列表是正确行为）。
  const [eventFocus, setEventFocus] = useState<string | null>(null);

  // —— 外壳与弹窗开关 ——
  const [ctxOpen, setCtxOpen] = useState(() => window.innerWidth >= 1024);
  const [drawer, setDrawer] = useState(false);
  const [pluginsOpen, setPluginsOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [proxyConfigOpen, setProxyConfigOpen] = useState(false);
  const [agentDownloadOpen, setAgentDownloadOpen] = useState(false);
  const [mcpAccessOpen, setMcpAccessOpen] = useState(false);
  const [membersOpen, setMembersOpen] = useState(false);
  const [probesOpen, setProbesOpen] = useState(false);
  const [startOpen, setStartOpen] = useState(false);
  const [projectPrefill, setProjectPrefill] = useState<{
    port?: number;
    plugin?: string;
    projectId?: string;
  }>({});
  const [startCaptureProbeId, setStartCaptureProbeId] = useState<string | null>(null);
  const [isDark, setIsDark] = useState(() => {
    const stored = localStorage.getItem("gt-theme");
    if (stored) return stored === "dark";
    return window.matchMedia("(prefers-color-scheme: dark)").matches;
  });

  const authError = useAuthError();
  const identity = useIdentity();
  const stopCapture = useStopCapture();
  usePluginEventStream();

  const filterInputRef = useRef<HTMLInputElement>(null);
  const contextSearchRef = useRef<HTMLInputElement>(null);
  // 会话清单的搜索框在内容区（项目 / 未归属），不在左栏。
  const listSearchRef = useRef<HTMLInputElement>(null);

  // —— 由路由派生的上下文 ——
  const session = route.sessionId ? (sessions.find((s) => s.session_id === route.sessionId) ?? null) : null;
  const projectId = route.space === "session" ? session?.project_id || null : route.projectId;
  const projectName = projectId ? (projects.find((p) => p.id === projectId)?.name ?? null) : null;
  const runningCount = sessions.filter((s) => s.status === "running").length;

  // —— 副作用：主题、URL 归一、换会话清过滤 ——
  useEffect(() => {
    document.documentElement.classList.toggle("dark", isDark);
    localStorage.setItem("gt-theme", isDark ? "dark" : "light");
  }, [isDark]);

  // 空 hash（首次打开）落到工作台，保证「后退」永远有明确的起点。
  useEffect(() => {
    if (!window.location.hash || window.location.hash === "#" || window.location.hash === "#/") {
      navigate(WORKSPACE_HREF, { replace: true });
    }
  }, []);

  // 窄窗口下左栏是遮罩抽屉：导航后必须收起，否则点了列表条目仍被抽屉挡住内容。
  // route 快照按 hash 缓存，所以这个副作用只在真正换页时触发。
  useEffect(() => {
    if (window.innerWidth < 1024) setCtxOpen(false);
  }, [route]);

  // 跨视图定位是一次性落点：离开协议事件视图就作废，再回来看到的是完整列表。
  // 从状态变更视图跳过来时这个判断看到的是「已经到达 events」，所以不会误杀。
  useEffect(() => {
    if (route.view !== "events") setEventFocus(null);
  }, [route.view]);

  // 连接/关键词/剔除/方向/语义都是会话内的东西：换会话必须清，换视图不能清。
  // 依赖用「会话身份键」而不是整个 route，正是为了把这两种切换区分开。
  const sessionKey = route.space === "session" ? route.sessionId : null;
  useEffect(() => {
    setConnFilter(null);
    setQuery("");
    setExclude("");
    setDirection("");
    setSemantics([]);
    setEventFocus(null);
  }, [sessionKey]);

  // 401 发生时自动打开设置弹窗，引导填入访问令牌（横幅常驻直至保存新 token）。
  // 其它弹窗打开时暂不抢占（避免遮挡下抢焦点/一次 ESC 关两个），关掉后仍会自动补开。
  useEffect(() => {
    if (authError && !startOpen && !proxyConfigOpen) setSettingsOpen(true);
  }, [authError, startOpen, proxyConfigOpen]);

  // 快捷键：Ctrl/Cmd+K 聚焦当前空间左栏搜索；"/" 聚焦协议事件的过滤输入框。
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const t = e.target as HTMLElement | null;
      const editable = t?.tagName === "INPUT" || t?.tagName === "TEXTAREA" || t?.isContentEditable;
      if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) {
        e.preventDefault();
        // 当前空间有会话清单就搜清单，否则展开左栏搜它自己的框（工作台的项目搜索）。
        if (listSearchRef.current) {
          listSearchRef.current.focus();
          listSearchRef.current.select();
          return;
        }
        setCtxOpen(true);
        contextSearchRef.current?.focus();
        return;
      }
      if (
        e.key === "/" &&
        !editable &&
        !e.metaKey &&
        !e.ctrlKey &&
        !e.altKey &&
        route.space === "session" &&
        route.view === "events"
      ) {
        e.preventDefault();
        filterInputRef.current?.focus();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [route.space, route.view]);

  // —— 导航动作 ——
  const openSession = useCallback((sessionId: string, view: SessionView = "overview") => {
    navigate(sessionHref(sessionId, view));
  }, []);

  // 包装过滤栏回调：人一开始自己过滤，就说明他在找别的东西，定位模式必须让位。
  function exitFocusOn<T>(setter: (value: T) => void) {
    return (value: T) => {
      setEventFocus(null);
      setter(value);
    };
  }

  function jumpToEvent(sessionId: string, eventId: string) {
    // 定位与过滤是两个问题：进定位时把上一轮过滤清掉，否则屏幕上会留一排
    // 「看着生效、其实被定位模式暂停」的条件芯片。
    setQuery("");
    setDirection("");
    setSemantics([]);
    setConnFilter(null);
    setEventFocus(eventId);
    openSession(sessionId, "events");
  }

  const handleDeletedSession = useCallback(
    (sessionId: string) => {
      if (route.space === "session" && route.sessionId === sessionId) {
        navigate(projectSpaceHref(session?.project_id));
      }
    },
    [route.space, route.sessionId, session?.project_id],
  );

  const openCapture = useCallback(
    (projectId: string | null) => {
      const p = projectId ? projects.find((x) => x.id === projectId) : undefined;
      setProjectPrefill(
        p
          ? {
              projectId: p.id,
              port: p.default_port && p.default_port > 0 ? p.default_port : undefined,
              plugin: p.default_plugin || undefined,
            }
          : {},
      );
      setStartOpen(true);
    },
    [projects],
  );

  function handleStop() {
    if (!session) return;
    const id = session.session_id;
    stopCapture.mutate(
      { sessionId: id },
      {
        onSuccess: () => toast.success("抓包已停止", `会话 ${id}`),
        onError: (err) => toast.error("停止失败", err.message),
      },
    );
  }

  function handleDrawer(action: DrawerAction) {
    if (action === "overview") {
      setDrawer(true);
      return;
    }
    setDrawer(false);
    if (action === "plugins") setPluginsOpen(true);
    else if (action === "probes") setProbesOpen(true);
    else if (action === "members") setMembersOpen(true);
    else setSettingsOpen(true);
  }

  function handleDrawerSection(section: DrawerSection) {
    handleDrawer(section);
  }

  const subbarCounts: Partial<Record<SessionView, number>> = session
    ? { events: session.events, raw: session.raw_packets }
    : {};

  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      <SpaceRail
        space={route.space}
        homeProjectId={projects[0]?.id ?? null}
        runningCount={runningCount}
        isDark={isDark}
        onToggleTheme={() => setIsDark((v) => !v)}
        onDrawer={handleDrawer}
      />

      {/* 左栏：宽屏常驻，窄窗口改为遮罩抽屉（原型「窄窗口」边界态） */}
      {ctxOpen && (
        <button
          type="button"
          aria-label="收起侧栏"
          onClick={() => setCtxOpen(false)}
          className="fixed inset-0 z-30 bg-slate-950/40 lg:hidden"
        />
      )}
      <div
        className={cn(
          "shrink-0",
          "max-lg:fixed max-lg:inset-y-0 max-lg:left-14 max-lg:z-40 max-lg:shadow-xl",
          !ctxOpen && "max-lg:hidden",
        )}
      >
        <ContextPanel
          route={route}
          projectName={projectName}
          selectedSessionId={route.sessionId}
          onSelectSession={(id) => {
            openSession(id);
            if (window.innerWidth < 1024) setCtxOpen(false);
          }}
          onDeletedSession={handleDeletedSession}
          onStartCapture={openCapture}
          searchInputRef={contextSearchRef}
        />
      </div>

      <main className="flex min-w-0 flex-1 flex-col">
        {/* 401 横幅：服务器要求访问令牌而本地未配置/已失效 */}
        {authError && (
          <div className="flex items-center gap-2 border-b border-warning/30 bg-warning/10 px-4 py-2 text-sm text-warning dark:border-warning/40 dark:bg-warning/10">
            <KeyRound className="h-4 w-4 shrink-0" />
            <span className="flex-1">服务器开启了访问令牌校验，请在设置中填入访问令牌后重试。</span>
            <Button size="sm" variant="outline" className="h-7" onClick={() => setSettingsOpen(true)}>
              打开设置
            </Button>
          </div>
        )}

        <TopBar
          route={route}
          projectName={projectName}
          projectId={projectId}
          session={session}
          contextOpen={ctxOpen}
          onToggleContext={() => setCtxOpen((v) => !v)}
          onSwitchSession={() => {
            // 不展开抽屉：项目空间的主区本身就是会话清单。
            navigate(projectSpaceHref(session?.project_id));
          }}
          onDevices={() => setAgentDownloadOpen(true)}
          onMcpAccess={() => setMcpAccessOpen(true)}
          onDrawer={handleDrawer}
          identity={identity}
        />

        {route.space === "session" && route.sessionId && (
          <SessionSubbar
            sessionId={route.sessionId}
            view={route.view}
            counts={subbarCounts}
            running={session?.status === "running"}
            stopping={stopCapture.isPending}
            onStop={handleStop}
          />
        )}

        {route.space === "session" && route.view === "events" && session && (
          <div className="border-b border-border bg-card/40 px-4 py-3">
            <FilterBar
              sessionId={session.session_id}
              query={query}
              onQueryChange={exitFocusOn(setQuery)}
              exclude={exclude}
              onExcludeChange={exitFocusOn(setExclude)}
              direction={direction}
              onDirectionChange={exitFocusOn(setDirection)}
              semantic={semantics}
              onSemanticChange={exitFocusOn(setSemantics)}
              connFilter={connFilter}
              onConnFilterChange={exitFocusOn(setConnFilter)}
              inputRef={filterInputRef}
            />
          </div>
        )}

        <div className="min-h-0 flex-1 overflow-hidden">
          {route.space === "workspace" && (
            <WorkspacePage
              onStartCapture={openCapture}
              onAgentDownload={() => setAgentDownloadOpen(true)}
              onProxy={() => setProxyConfigOpen(true)}
              onSelectSession={openSession}
            />
          )}

          {route.space === "project" && route.projectId && (
            <ProjectPage
              projectId={route.projectId}
              section={route.projectSection}
              configTab={route.configTab}
              selectedSessionId={route.sessionId}
              onDeletedSession={handleDeletedSession}
              searchInputRef={listSearchRef}
              onSelectSession={openSession}
              onStartCapture={openCapture}
            />
          )}

          {route.space === "project" && !route.projectId && (
            <UnassignedPage
              selectedSessionId={route.sessionId}
              onDeletedSession={handleDeletedSession}
              searchInputRef={listSearchRef}
              onSelectSession={openSession}
              onStartCapture={openCapture}
            />
          )}

          {route.space === "session" && !session && (
            <div className="flex h-full items-center justify-center p-6">
              <EmptyState
                icon={<KeyRound className="h-5 w-5" />}
                title={route.sessionId ? "会话不存在" : "还没有选择会话"}
                hint={
                  route.sessionId
                    ? "该会话可能已被删除。回到工作台重新选择一个会话。"
                    : "在工作台或项目空间里点一个会话，就能看到它的连接、协议事件与状态变更。"
                }
                action={
                  <Button size="sm" variant="outline" onClick={() => navigate("#/workspace")}>
                    回到工作台
                  </Button>
                }
              />
            </div>
          )}

          {route.space === "session" && session && (
            <SessionViewBody
              sessionId={session.session_id}
              view={route.view}
              query={query}
              exclude={exclude}
              direction={direction}
              semantics={semantics}
              connFilter={connFilter}
              running={session.status === "running"}
              onConnFilter={(conn) => {
                setEventFocus(null);
                setConnFilter(conn);
              }}
              onOpenSession={openSession}
              eventFocus={eventFocus}
              onExitEventFocus={() => setEventFocus(null)}
              onJumpToEvent={(eventId) => jumpToEvent(session.session_id, eventId)}
            />
          )}
        </div>
      </main>

      <ConfigDrawer open={drawer} onClose={() => setDrawer(false)} onOpen={handleDrawerSection} />

      {/* 解码插件面板仍是自己那套完整表格，抽屉只负责把它带出来 */}
      <Dialog
        open={pluginsOpen}
        onClose={() => setPluginsOpen(false)}
        icon={<Plug className="h-5 w-5" />}
        title="解码插件"
        description="插件注册表按 owner 隔离；离线插件不可绑定。"
        className="max-w-5xl"
      >
        <PluginPanel />
      </Dialog>

      <SettingsDialog open={settingsOpen} onClose={() => setSettingsOpen(false)} />
      <ProxyConfigDialog
        open={proxyConfigOpen}
        onClose={() => setProxyConfigOpen(false)}
        onNavigateToSession={(sessionId) => {
          setProxyConfigOpen(false);
          openSession(sessionId, "connections");
        }}
      />
      <AgentDownloadDialog
        open={agentDownloadOpen}
        onClose={() => setAgentDownloadOpen(false)}
        onStartCapture={(probeId) => {
          setStartCaptureProbeId(probeId);
          setAgentDownloadOpen(false);
          setStartOpen(true);
        }}
      />
      <McpAccessDialog open={mcpAccessOpen} onClose={() => setMcpAccessOpen(false)} />
      <MembersAdminDialog open={membersOpen} onClose={() => setMembersOpen(false)} />
      <ProbeAdminDialog
        open={probesOpen}
        onClose={() => setProbesOpen(false)}
        onImported={(sessionId) => {
          setProbesOpen(false);
          openSession(sessionId);
        }}
      />
      <StartCaptureDialog
        open={startOpen}
        onClose={() => {
          setStartOpen(false);
          setStartCaptureProbeId(null);
        }}
        initialPort={projectPrefill.port}
        initialPlugin={projectPrefill.plugin}
        initialProjectId={projectPrefill.projectId}
        initialProbeId={startCaptureProbeId ?? undefined}
        onOpenProxyConfig={() => setProxyConfigOpen(true)}
        onStarted={(sessionId) => {
          setStartOpen(false);
          setStartCaptureProbeId(null);
          openSession(sessionId);
        }}
      />
    </div>
  );
}

/** 会话空间的六个视图主体。 */
function SessionViewBody({
  sessionId,
  view,
  query,
  exclude,
  direction,
  semantics,
  connFilter,
  running,
  onConnFilter,
  onOpenSession,
  eventFocus,
  onExitEventFocus,
  onJumpToEvent,
}: {
  sessionId: string;
  view: SessionView;
  query: string;
  /** 剔除关键词，只作用于协议事件视图（状态变更视图有它自己的筛选）。 */
  exclude: string;
  direction: DirectionFilter;
  semantics: SemanticFilter;
  connFilter: ConnectionSummary | null;
  running: boolean;
  onConnFilter: (conn: ConnectionSummary | null) => void;
  onOpenSession: (sessionId: string, view?: SessionView) => void;
  eventFocus: string | null;
  onExitEventFocus: () => void;
  onJumpToEvent: (eventId: string) => void;
}) {
  if (view === "overview") {
    return (
      <SessionOverviewPage
        sessionId={sessionId}
        onSelectConn={(conn) => {
          // 概览里的连接行与连接页同一条路径：设为事件过滤条件，再换视图。
          onConnFilter(conn);
          onOpenSession(sessionId, "events");
        }}
      />
    );
  }

  if (view === "connections") {
    return (
      <div className="h-full overflow-auto gt-scroll p-4">
        <ConnectionsPage
          sessionId={sessionId}
          onSelectConn={(conn) => {
            // 连接 → 事件：把这条连接写成事件视图的过滤条件，再换视图。
            onConnFilter(conn);
            onOpenSession(sessionId, "events");
          }}
        />
      </div>
    );
  }

  if (view === "events") {
    return (
      <div className="h-full overflow-auto gt-scroll p-4">
        <EventTable
          sessionId={sessionId}
          query={query}
          exclude={exclude}
          direction={direction}
          semantics={semantics}
          connFilter={connFilter}
          focusEventId={eventFocus}
          onExitFocus={onExitEventFocus}
        />
      </div>
    );
  }

  if (view === "states") {
    return (
      <div className="h-full overflow-auto gt-scroll p-4">
        <StateChangeExplorer
          sessionId={sessionId}
          query={query}
          direction={direction}
          connFilter={connFilter}
          onOpenEvent={onJumpToEvent}
        />
      </div>
    );
  }

  if (view === "alerts") {
    return (
      <div className="h-full overflow-auto gt-scroll p-4">
        <SessionAlertsPanel sessionId={sessionId} running={running} onOpenEvent={onJumpToEvent} />
      </div>
    );
  }

  return (
    <div className="h-full overflow-auto gt-scroll p-4">
      <RawPacketTable sessionId={sessionId} onDecoded={() => onOpenSession(sessionId, "events")} />
    </div>
  );
}
