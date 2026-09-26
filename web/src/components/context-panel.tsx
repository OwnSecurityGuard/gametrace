// ContextPanel — 左栏，内容完全由当前空间决定。
//
// 这是本次 IA 重构的核心：过去左栏永远是「会话列表」（用户级全量），
// 而顶部 tab 又混着会话级和身份级视图，用户无法判断自己看的是谁的数据。
// 现在作用域由 URL 深度派生，左栏只是它的投影：
//   工作台 → 项目清单（+ 运行中 / 未归属抓包）
//   项目   → 该项目会话（或配置页签）
//   会话   → 当前会话身份 + 同项目会话（视图切换只在内容区的二级条，不做两处）
// 三种形态共用同一套行样式，切换空间时位置不动，只有内容换。
import { type Ref, useState } from "react";
import {
  Activity,
  ArrowRight,
  Compass,
  Folder,
  FolderOpen,
  Inbox,
  Play,
  Plug,
  Radio,
  ShieldAlert,
  Users,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { PhaseBadge } from "@/components/session-phase-tracker";
import { useProjects, useProject, useSessions } from "@/hooks/use-mcp";
import { navigate } from "@/lib/router";
import {
  PROJECT_CONFIG_TABS,
  UNASSIGNED_HREF,
  projectHref,
  projectSpaceHref,
  sessionHref,
  type Route,
} from "@/lib/routes";
import { cn } from "@/lib/utils";
import type { SessionInfo } from "@/types/session";

const CONFIG_ICONS: Record<string, typeof Users> = {
  members: Users,
  plugins: Plug,
  rules: Activity,
};

interface ContextPanelProps {
  route: Route;
  /** 当前项目名（面包屑同源，来自已加载的会话/项目数据；缺省时面板自行回落）。 */
  projectName?: string | null;
  selectedSessionId: string | null;
  onSelectSession: (sessionId: string) => void;
  onDeletedSession: (sessionId: string) => void;
  /** 打开抓包通道弹窗；projectId 为 null 时不预置归属。 */
  onStartCapture: (projectId: string | null) => void;
  /** 供 Ctrl/Cmd+K 聚焦当前空间里的搜索框。 */
  searchInputRef?: Ref<HTMLInputElement>;
}

export function ContextPanel(props: ContextPanelProps) {
  const { route } = props;
  return (
    <aside className="flex w-72 shrink-0 flex-col border-r border-border bg-card">
      {route.space === "workspace" && <WorkspacePanel {...props} />}
      {route.space === "project" && <ProjectPanel {...props} />}
      {route.space === "session" && <SessionPanel {...props} />}
    </aside>
  );
}

/* ---------- 共用外壳与行样式 ---------- */

function Head({ children }: { children: React.ReactNode }) {
  return <div className="border-b border-border px-3 py-3">{children}</div>;
}

function Body({ children }: { children: React.ReactNode }) {
  return <div className="min-h-0 flex-1 overflow-auto gt-scroll p-2">{children}</div>;
}

function Foot({ children }: { children: React.ReactNode }) {
  return <div className="flex items-center gap-2 border-t border-border px-3 py-2.5">{children}</div>;
}

function ScopePill({ icon: Icon, children }: { icon: typeof Compass; children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-md bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground">
      <Icon className="h-3 w-3" />
      {children}
    </span>
  );
}

function Group({ label, count }: { label: string; count?: number }) {
  return (
    <div className="flex items-center gap-1.5 px-1.5 pb-1 pt-3 first:pt-1">
      <span className="text-micro font-medium uppercase tracking-wide text-muted-foreground">{label}</span>
      {count != null && <span className="font-mono text-micro text-muted-foreground">{count}</span>}
    </div>
  );
}

const ROW =
  "flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** 项目行：名称 + 默认插件/端口 + 会话数。 */
function ProjectRow({
  id,
  name,
  meta,
  count,
  running,
}: {
  id: string;
  name: string;
  meta: string;
  count: number;
  running: boolean;
}) {
  return (
    <a href={projectHref(id)} className={cn(ROW, "group")}>
      {running ? (
        <span className="gt-live-dot mt-1.5">
          <span className="sr-only">运行中</span>
        </span>
      ) : (
        <Folder className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      )}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm group-hover:underline">{name}</span>
        {meta && <span className="block truncate text-micro text-muted-foreground">{meta}</span>}
      </span>
      <span className="shrink-0 font-mono text-micro text-muted-foreground">{count}</span>
    </a>
  );
}

/** 会话行：状态点 + 会话 id + 时间·项目 + 事件数。 */
function SessionRow({
  session,
  projectName,
  active,
}: {
  session: SessionInfo;
  projectName?: string;
  active: boolean;
}) {
  const running = session.status === "running";
  return (
    <a
      href={sessionHref(session.session_id)}
      className={cn(ROW, active && "bg-primary-muted")}
      aria-current={active ? "page" : undefined}
    >
      <span
        className={cn(
          "mt-1.5 inline-block h-2 w-2 shrink-0 rounded-full",
          running ? "gt-live-dot" : session.status === "error" ? "bg-destructive" : "bg-muted-foreground/40",
        )}
      >
        <span className="sr-only">{running ? "运行中" : session.status === "error" ? "出错" : "已停止"}</span>
      </span>
      <span className="min-w-0 flex-1">
        <span
          className={cn(
            "block truncate font-mono text-xs",
            active ? "font-medium text-foreground" : "text-foreground/90",
          )}
        >
          {session.session_id}
        </span>
        <span className="block truncate text-micro text-muted-foreground">
          {fmtTime(session.started_at)}
          {projectName ? ` · ${projectName}` : ""}
        </span>
      </span>
      <span className="shrink-0 font-mono text-micro text-muted-foreground">{fmtNum(session.events)}</span>
    </a>
  );
}

/** 左栏在项目/未归属节只报数：清单、搜索与批量删除都归内容区那一份。
 *  同一份清单在两层各列一次，就会一份能搜一份不能搜、一份是新一份是旧。 */
function SessionScopeStats({ projectId }: { projectId: string | null }) {
  const { data } = useSessions();
  const sessions = (data?.sessions ?? []).filter((s) => (s.project_id || "") === (projectId ?? ""));
  const running = sessions.filter((s) => s.status === "running").length;

  return (
    <Body>
      <div className="rounded-lg border border-border bg-muted/40 px-3 py-2.5 text-xs text-muted-foreground">
        <p>
          {sessions.length === 0
            ? "这个作用域下还没有会话"
            : `${sessions.length} 个会话${running > 0 ? ` · ${running} 个抓包中` : ""}`}
        </p>
        <p className="mt-1 leading-relaxed">会话清单在右侧内容区：可搜索、可批量删除。</p>
      </div>
    </Body>
  );
}

function fmtTime(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function fmtNum(n?: number): string {
  return !n ? "0" : n.toLocaleString();
}

/* ========== 工作台：列项目，不列全量会话清单 ========== */

function WorkspacePanel({ selectedSessionId, onStartCapture, searchInputRef }: ContextPanelProps) {
  const [query, setQuery] = useState("");
  const { data: sessionsData } = useSessions();
  const { data: projectsData } = useProjects();
  const sessions = sessionsData?.sessions ?? [];
  const projects = projectsData?.projects ?? [];

  const kw = query.trim().toLowerCase();
  const nameOf = (projectId?: string) =>
    projectId ? projects.find((p) => p.id === projectId)?.name : undefined;

  const visibleProjects = kw
    ? projects.filter(
        (p) =>
          p.name.toLowerCase().includes(kw) ||
          (p.default_plugin ?? "").toLowerCase().includes(kw) ||
          sessions.some((s) => s.project_id === p.id && s.session_id.toLowerCase().includes(kw)),
      )
    : projects;
  const running = sessions.filter((s) => s.status === "running");
  const orphans = kw
    ? sessions.filter((s) => !s.project_id && s.session_id.toLowerCase().includes(kw))
    : sessions.filter((s) => !s.project_id);

  return (
    <>
      <Head>
        <ScopePill icon={FolderOpen}>身份级作用域</ScopePill>
        <div className="mt-2">
          <Input
            ref={searchInputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="搜索项目或会话"
            placeholder="搜索项目 / 会话 ID"
          />
        </div>
      </Head>

      <Body>
        <Group label="项目" count={visibleProjects.length} />
        {visibleProjects.length === 0 && (
          <p className="px-2 py-1 text-xs text-muted-foreground">
            {projects.length === 0 ? "还没有项目。建一个项目来记住端口与插件。" : "没有匹配的项目。"}
          </p>
        )}
        {visibleProjects.map((p) => {
          const list = sessions.filter((s) => s.project_id === p.id);
          return (
            <ProjectRow
              key={p.id}
              id={p.id}
              name={p.name}
              meta={[p.default_plugin || "未设插件", p.default_port ? `:${p.default_port}` : ""]
                .filter(Boolean)
                .join(" · ")}
              count={list.length}
              running={list.some((s) => s.status === "running")}
            />
          );
        })}

        {running.length > 0 && (
          <>
            <Group label="运行中" count={running.length} />
            {running.map((s) => (
              <SessionRow
                key={s.session_id}
                session={s}
                projectName={nameOf(s.project_id)}
                active={s.session_id === selectedSessionId}
              />
            ))}
          </>
        )}

        {orphans.length > 0 && (
          <>
            <Group label="未归属抓包" count={orphans.length} />
            {orphans.slice(0, 8).map((s) => (
              <SessionRow key={s.session_id} session={s} active={s.session_id === selectedSessionId} />
            ))}
            <a href={UNASSIGNED_HREF} className={cn(ROW, "items-center")}>
              <ArrowRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate text-micro text-primary">
                全部 {orphans.length} 个未归属会话（可批量删除）
              </span>
            </a>
          </>
        )}

        {projects.length === 0 && sessions.length === 0 && (
          <EmptyState
            className="mt-6"
            icon={<Inbox className="h-5 w-5" />}
            title="从这里开始"
            hint="接入设备 → 建项目 → 开始第一次抓包。端口与插件由项目记住。"
          />
        )}
      </Body>

      <Foot>
        <Button className="w-full" size="sm" onClick={() => onStartCapture(null)}>
          <Play className="h-3.5 w-3.5" />
          开始抓包
        </Button>
      </Foot>
    </>
  );
}

/* ========== 项目：该项目会话 / 配置页签 ========== */

/* ========== 项目：该项目会话 / 配置页签；未归属桶复用同一套管理动作 ========== */

function ProjectPanel(props: ContextPanelProps) {
  const projectId = props.route.projectId;
  if (projectId === null) return <UnassignedPanel {...props} />;
  return <ScopedProjectPanel {...props} projectId={projectId} />;
}

function UnassignedPanel({ onStartCapture }: ContextPanelProps) {
  return (
    <>
      <Head>
        <ScopePill icon={Folder}>项目级作用域</ScopePill>
        <p className="mt-1.5 text-sm font-medium">未归属抓包</p>
        <p className="text-micro leading-relaxed text-muted-foreground">
          开始抓包时没有选择归属的会话都在这里。
        </p>
      </Head>
      <SessionScopeStats projectId={null} />
      <Foot>
        <Button className="w-full" size="sm" onClick={() => onStartCapture(null)}>
          <Play className="h-3.5 w-3.5" />
          开始抓包（不归属项目）
        </Button>
      </Foot>
    </>
  );
}

function ScopedProjectPanel({
  route,
  projectName,
  projectId,
  onStartCapture,
}: ContextPanelProps & { projectId: string }) {
  const { data, isError, error } = useProject(projectId);
  const project = data?.project;
  // 读权限没有独立信号：非成员时 get_project 整个调用被 authz 拒掉（forbidden），
  // 所以"被拒"只能从查询失败里认出来 —— 认出来就隐藏会话清单。
  const canRead = !isError || !/forbidden/i.test(error instanceof Error ? error.message : "");

  const displayName = projectName ?? project?.name ?? projectId;

  const head = (
    <Head>
      <ScopePill icon={Folder}>项目级作用域</ScopePill>
      <p className="mt-1.5 truncate text-sm font-medium" title={displayName}>
        {displayName}
      </p>
      <p className="truncate text-micro text-muted-foreground">
        {project
          ? project.owner
            ? `Owner ${project.owner}`
            : "匿名项目"
          : canRead
            ? "加载中…"
            : "详情按成员边界隐藏"}
        {project?.members ? ` · ${project.members.length} 名成员` : ""}
      </p>
      <div
        className="mt-2 flex items-center gap-1 rounded-lg bg-muted p-1"
        role="tablist"
        aria-label="项目内容"
      >
        {(
          [
            { id: "sessions", label: "会话" },
            { id: "config", label: "配置" },
          ] as const
        ).map((seg) => {
          const on = route.projectSection === seg.id;
          return (
            <button
              key={seg.id}
              type="button"
              role="tab"
              aria-selected={on}
              onClick={() =>
                navigate(
                  seg.id === "config"
                    ? projectHref(projectId, "config", route.configTab)
                    : projectHref(projectId),
                )
              }
              className={cn(
                "flex-1 rounded-md px-2 py-1 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                on ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {seg.label}
            </button>
          );
        })}
      </div>
    </Head>
  );

  if (!canRead) {
    return (
      <>
        {head}
        <Body>
          <p className="flex items-start gap-2 px-2 py-2 text-xs text-muted-foreground">
            <ShieldAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" />
            <span>你不是该项目的成员，会话清单与项目配置已按成员边界隐藏。</span>
          </p>
        </Body>
      </>
    );
  }

  if (route.projectSection === "config") {
    const counts: Record<string, number | undefined> = {
      members: project?.members?.length,
      plugins: project?.plugins?.length,
      rules: project?.rules?.length,
    };
    return (
      <>
        {head}
        <Body>
          <Group label="配置页签" />
          {PROJECT_CONFIG_TABS.map((t) => {
            const on = route.configTab === t.id;
            const Icon = CONFIG_ICONS[t.id] ?? Plug;
            return (
              <a
                key={t.id}
                href={projectHref(projectId, "config", t.id)}
                aria-current={on ? "page" : undefined}
                className={cn(ROW, on && "bg-primary-muted")}
              >
                <Icon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1 truncate text-sm">{t.label}</span>
                {counts[t.id] != null && (
                  <span className="shrink-0 font-mono text-micro text-muted-foreground">{counts[t.id]}</span>
                )}
              </a>
            );
          })}
        </Body>
        <Foot>
          <Button variant="outline" size="sm" className="w-full" asChild>
            <a href={projectHref(projectId)}>
              返回会话清单
              <ArrowRight className="h-3.5 w-3.5" />
            </a>
          </Button>
        </Foot>
      </>
    );
  }

  return (
    <>
      {head}
      <SessionScopeStats projectId={projectId} />
      <Foot>
        <Button className="w-full" size="sm" onClick={() => onStartCapture(projectId)}>
          <Play className="h-3.5 w-3.5" />
          在此项目开始抓包
        </Button>
      </Foot>
    </>
  );
}

/* ========== 会话：视图锚点 + 同项目会话 ========== */

function SessionPanel({ route, selectedSessionId, onStartCapture }: ContextPanelProps) {
  const { data: sessionsData } = useSessions();
  const { data: projectsData } = useProjects();
  const sessions = sessionsData?.sessions ?? [];
  const projects = projectsData?.projects ?? [];
  const sessionId = route.sessionId ?? selectedSessionId;
  const session = sessions.find((s) => s.session_id === sessionId);

  if (!session) {
    return (
      <>
        <Head>
          <ScopePill icon={Radio}>会话级</ScopePill>
        </Head>
        <Body>
          <p className="px-2 py-2 text-xs text-muted-foreground">
            {sessionId ? "会话不存在或已被删除。" : "还没有选择会话。在「工作台」或项目里挑一个会话。"}
          </p>
        </Body>
      </>
    );
  }

  const project = session.project_id ? projects.find((p) => p.id === session.project_id) : undefined;
  const siblings = project
    ? sessions.filter((s) => s.project_id === project.id && s.session_id !== session.session_id)
    : sessions.filter((s) => !s.project_id && s.session_id !== session.session_id);

  return (
    <>
      <Head>
        <div className="flex items-center gap-1.5">
          <ScopePill icon={Radio}>会话级</ScopePill>
          <PhaseBadge input={{ meta: session }} short className="min-w-0" />
        </div>
        <p className="mt-2 truncate font-mono text-xs font-medium" title={session.session_id}>
          {session.session_id}
        </p>
        <p className="mt-0.5 truncate text-micro text-muted-foreground">
          {fmtTime(session.started_at)} · {session.plugin || "未设插件"}
        </p>
        <a
          href={projectSpaceHref(session.project_id)}
          className="mt-2 flex w-full items-center gap-1.5 rounded-md border border-border px-2 py-1 text-xs text-muted-foreground transition-colors hover:border-primary/40 hover:text-foreground"
        >
          <Folder className="h-3.5 w-3.5 shrink-0" />
          <span className="min-w-0 flex-1 truncate">
            {project?.name ?? (session.project_id ? "归属项目" : "未归属抓包")}
          </span>
          <ArrowRight className="h-3.5 w-3.5 shrink-0" />
        </a>
      </Head>

      <Body>
        {siblings.length > 0 && (
          <>
            <Group label={project ? "同项目会话" : "其他未归属会话"} count={siblings.length} />
            {siblings.slice(0, 6).map((s) => (
              <SessionRow key={s.session_id} session={s} active={s.session_id === session.session_id} />
            ))}
          </>
        )}
      </Body>

      {/* 停止抓包在视图条上（会话级主操作）；这里只给"再来一次"，避免同一动作两处竞态。 */}
      <Foot>
        <Button
          variant="outline"
          size="sm"
          className="w-full"
          onClick={() => onStartCapture(session.project_id || null)}
        >
          <Play className="h-3.5 w-3.5" />
          {project ? "在此项目再抓一次" : "再抓一次（不归属项目）"}
        </Button>
      </Foot>
    </>
  );
}
