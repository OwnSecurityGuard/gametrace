// ProjectPage — 项目空间的主体（项目作为一等组织单元）。
//
// 章节由 URL 决定，而不是页面内部状态：
//   · `#/project/:id`            → 项目身份 + 会话清单
//   · `#/project/:id/config/:tab` → 项目身份 + 成员 / 解码插件 / 规则 中的一栏
// 清单只有这一份：搜索、只看我的、批量删除、逐条删除都在这里，左栏只做空间导航。
import { useState, type Ref } from "react";
import { Play, Plus, ShieldAlert, X } from "lucide-react";
import {
  useProject,
  useAddProjectMember,
  useRemoveProjectMember,
  useAddProjectPlugin,
  useRemoveProjectPlugin,
  useSetProjectRules,
  useRegisteredPlugins,
} from "@/hooks/use-mcp";
import { useIdentity } from "@/hooks/use-auth";
import { toast } from "@/components/ui/toast";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { Badge } from "@/components/ui/badge";
import { SessionList } from "@/components/session-list";
import { navigate } from "@/lib/router";
import { WORKSPACE_HREF, type ProjectConfigTab, type ProjectSection } from "@/lib/routes";
import type { ProjectDetail, ProjectRecentSession, ProjectRole } from "@/types/project";

interface ProjectPageProps {
  projectId: string;
  /** 当前选中的会话（项目空间一般为空，用于高亮与实时统计）。 */
  selectedSessionId?: string | null;
  /** 会话删除后回调，用于清空上层选中态 */
  onDeletedSession?: (sessionId: string) => void;
  /** Ctrl/Cmd+K 聚焦这里的会话搜索框 */
  searchInputRef?: Ref<HTMLInputElement>;
  section: ProjectSection;
  configTab: ProjectConfigTab;
  onSelectSession: (sessionId: string) => void;
  /** 以该项目开始抓包（自动带入默认端口/插件，会话归属该项目） */
  onStartCapture: (projectId: string) => void;
}

function statusDot(status?: string) {
  return status === "running" ? "bg-success" : "bg-muted-foreground/40";
}

function uid(): string {
  // 给新增的插件/规则一行分配一个前端唯一 id，兼做 React key。
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return `id-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

export function ProjectPage({
  projectId,
  section,
  configTab,
  selectedSessionId,
  onDeletedSession,
  searchInputRef,
  onSelectSession,
  onStartCapture,
}: ProjectPageProps) {
  const { data, isLoading, isError, error } = useProject(projectId);
  const project: ProjectDetail | undefined = data?.project;
  const recentSessions: ProjectRecentSession[] = data?.recent_sessions ?? project?.recent_sessions ?? [];

  // 权限入口由后端下发（get_project.capabilities 是"当前调用者被放行的管理动作"，
  // authz.Action 列表），前端不再自行判权（2026-09-05：权限判定统一收口在 pkg/authz）。
  // 注意 capabilities 只含写动作：读权限由 get_project 本身把关，被拒时整个调用报错。
  // capabilities 缺失时（旧后端）回退到本地启发式判断。
  const identity = useIdentity();
  const caps = data?.capabilities;
  const isProjectAdmin = caps
    ? caps.includes("project:manage_members") || caps.includes("project:manage_plugins")
    : (project?.created_by != null && project.created_by === identity?.owner) ||
      identity?.isAdmin === true ||
      (project?.members?.some((m) => m.user === identity?.owner && m.role === "admin") ?? false);
  // 项目成员即可添加/移除「自己」的插件（后端按 owner 校验归属）；admin 全权。
  // 匿名/本地模式由 caps 走 admin 分支，此处仅覆盖真实身份的成员场景。
  const isProjectMember =
    (project?.owner != null && project.owner === identity?.owner) ||
    (project?.members?.some((m) => m.user === identity?.owner) ?? false);
  const canManagePlugins = isProjectAdmin || isProjectMember;

  // —— 增删成员 / 插件 / 规则 ——
  const addMember = useAddProjectMember(projectId);
  const removeMember = useRemoveProjectMember(projectId);
  const addPlugin = useAddProjectPlugin(projectId);
  const removePlugin = useRemoveProjectPlugin(projectId);
  const setRules = useSetProjectRules(projectId);
  const members = project?.members ?? [];
  const plugins = project?.plugins ?? [];
  const rules = project?.rules ?? [];
  const running = recentSessions.some((s) => s.status === "running");

  // 已注册插件（真实资源）供关联选择；按名称去重，已关联的不再出现在候选里。
  const { data: registeredPluginsData } = useRegisteredPlugins();
  const registeredPluginNames: string[] = [];
  for (const rp of registeredPluginsData?.plugins ?? []) {
    if (!registeredPluginNames.includes(rp.name)) registeredPluginNames.push(rp.name);
  }
  const addedPluginNames = new Set(plugins.map((pl) => pl.name));
  const candidatePlugins = registeredPluginNames.filter((n) => !addedPluginNames.has(n));

  const [memberUser, setMemberUser] = useState("");
  const [memberRole, setMemberRole] = useState<ProjectRole>("member");
  const [pluginName, setPluginName] = useState("");
  const [ruleName, setRuleName] = useState("");

  // 与后端 validOwnerName 同规则的即时预校验。
  const OWNER_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/;

  async function handleAddMember() {
    const user = memberUser.trim();
    if (!user) return;
    if (!OWNER_NAME_RE.test(user)) {
      toast.error("用户名格式不正确", "字母或数字开头，可含 . _ -，最长 64 字符");
      return;
    }
    try {
      const res = await addMember.mutateAsync({ user, role: memberRole });
      setMemberUser("");
      if (res.pending) {
        // 待注册：对方还没注册这个用户名。
        toast.success(
          "已加入（待注册）",
          `${user} 尚未注册：对方在「设置 → 没有令牌？快速开始」注册同名身份后自动生效`,
        );
      } else {
        toast.success("已添加成员", user);
      }
    } catch (err) {
      toast.error("添加失败", err instanceof Error ? err.message : String(err));
    }
  }

  function handleRemoveMember(user: string) {
    removeMember.mutate(
      { user },
      {
        onSuccess: () => toast.success("已移除成员", user),
        onError: (err) => toast.error("移除失败", err.message),
      },
    );
  }

  function handleAddPlugin() {
    const name = pluginName.trim();
    if (!name) return;
    // 增量关联已注册插件（后端按 name 校验：成员仅限自己注册的，admin 任意）。
    addPlugin.mutate(
      { name },
      {
        onSuccess: () => {
          setPluginName("");
          toast.success("已添加插件", name);
        },
        onError: (err) => toast.error("添加失败", err.message),
      },
    );
  }

  function handleRemovePlugin(id: string) {
    removePlugin.mutate(
      { id },
      {
        onError: (err) => toast.error("移除失败", err.message),
      },
    );
  }

  function handleAddRule() {
    const name = ruleName.trim();
    if (!name) return;
    setRules.mutate(
      { rules: [...rules, { id: uid(), name }] },
      {
        onSuccess: () => {
          setRuleName("");
          toast.success("已添加规则", name);
        },
        onError: (err) => toast.error("添加失败", err.message),
      },
    );
  }

  function handleRemoveRule(id: string) {
    setRules.mutate(
      { rules: rules.filter((r) => r.id !== id) },
      {
        onError: (err) => toast.error("移除失败", err.message),
      },
    );
  }

  // —— 加载中 / 未找到 ——
  if (isLoading) {
    return (
      <div className="mx-auto max-w-4xl space-y-6 p-6">
        <Skeleton className="h-6 w-40" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  if (isError || !project) {
    // 读权限没有独立信号：非成员时 get_project 整个调用被 authz 拒掉（forbidden）。
    const msg = error instanceof Error ? error.message : "";
    const forbidden = /forbidden/i.test(msg);
    return (
      <div className="mx-auto max-w-4xl p-6">
        <EmptyState
          icon={forbidden ? <ShieldAlert className="h-5 w-5" /> : <Play className="h-5 w-5" />}
          title={forbidden ? "无权访问该项目" : isError ? "项目加载失败" : "项目不存在"}
          hint={
            forbidden
              ? "你不是这个项目的成员，项目内容按成员边界隐藏。让项目 Owner 在成员里加上你的用户名。"
              : (msg || "该项目可能已被删除。")
          }
          action={
            <Button variant="outline" onClick={() => navigate(WORKSPACE_HREF)}>
              回到工作台
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div className="h-full overflow-auto gt-scroll">
      <div className="mx-auto max-w-4xl space-y-6 p-6">
        <header className="rounded-2xl border border-border bg-card/60 p-5">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <h1 className="text-lg font-semibold gt-gradient-text">{project.name}</h1>
                {project.game && <Badge variant="outline">{project.game}</Badge>}
                <Badge variant="secondary">
                  <span
                    className={`mr-1 h-1.5 w-1.5 rounded-full ${statusDot(running ? "running" : undefined)}`}
                  />
                  {running ? "在线" : "离线"}
                </Badge>
              </div>
              {project.description && (
                <p className="mt-1.5 text-sm text-muted-foreground">{project.description}</p>
              )}
              <p className="mt-2 text-[11px] text-muted-foreground">
                {project.created_by ? `由 ${project.created_by} 创建` : "匿名创建"} ·{" "}
                {project.default_port ? `端口 ${project.default_port}` : "未设默认端口"}
                {isProjectAdmin ? " · 你是管理员" : ""}
              </p>
            </div>
            <Button
              variant="default"
              size="sm"
              className="h-8 shrink-0"
              onClick={() => onStartCapture(project.id)}
              title="以该项目开始抓包（自动带入默认端口/插件，会话归属本项目）"
            >
              <Play className="h-4 w-4" />
              开始抓包
            </Button>
          </div>
        </header>

        {/* 配置：一次一栏，页签在左栏，位置在 URL */}
        {section === "config" && configTab === "members" && (
          <section>
            <h2 className="text-sm font-medium text-foreground">成员</h2>
            <div className="mt-2 rounded-2xl border border-border bg-card/60 p-4">
              {/* Owner 不在 members 表（SSOT 是 projects.owner），单独成行。 */}
              {project.owner ? (
                <ul className="mb-1.5 space-y-1.5">
                  <li className="flex items-center justify-between gap-2 rounded-lg px-2 py-1.5">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-sm">{project.owner}</span>
                      <Badge variant="default">Owner</Badge>
                    </div>
                  </li>
                </ul>
              ) : null}
              {members.length === 0 ? (
                <p className="text-sm text-muted-foreground">暂无其他成员。</p>
              ) : (
                <ul className="space-y-1.5">
                  {members.map((m) => {
                    const isProjectOwner = project.owner === m.user;
                    // 「待注册」仅在 token 多用户模式下有意义（匿名单机 identity=local）。
                    const showPending =
                      !isProjectOwner && !m.registered && identity !== null && identity.owner !== "local";
                    return (
                      <li
                        key={m.user}
                        className="flex items-center justify-between gap-2 rounded-lg px-2 py-1.5 hover:bg-muted/40"
                      >
                        <div className="flex min-w-0 items-center gap-2">
                          <span className="truncate text-sm">{m.user}</span>
                          <Badge variant={isProjectOwner ? "default" : "outline"}>
                            {isProjectOwner ? "Owner" : m.role === "admin" ? "管理员" : "成员"}
                          </Badge>
                          {showPending && (
                            <Badge
                              variant="outline"
                              className="text-muted-foreground"
                              title="该用户名尚未注册：对方在「设置 → 快速开始」注册同名身份后自动生效"
                            >
                              待注册
                            </Badge>
                          )}
                        </div>
                        {isProjectAdmin && !isProjectOwner && (
                          <button
                            type="button"
                            onClick={() => handleRemoveMember(m.user)}
                            title="移除成员"
                            className="rounded-md p-1 text-muted-foreground hover:bg-muted/50 hover:text-destructive"
                          >
                            <X className="h-3.5 w-3.5" />
                          </button>
                        )}
                      </li>
                    );
                  })}
                </ul>
              )}

              {isProjectAdmin && (
                <div className="mt-3 grid gap-2 border-t border-border pt-3 sm:grid-cols-[1fr_120px_auto]">
                  <input
                    value={memberUser}
                    onChange={(e) => setMemberUser(e.target.value)}
                    placeholder="用户名"
                    className="h-9 rounded-md border border-input bg-background px-2.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
                  />
                  <select
                    value={memberRole}
                    onChange={(e) => setMemberRole(e.target.value as ProjectRole)}
                    className="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
                  >
                    <option value="member">成员</option>
                    <option value="admin">管理员</option>
                  </select>
                  <button
                    type="button"
                    onClick={handleAddMember}
                    disabled={addMember.isPending || !memberUser.trim()}
                    className="inline-flex h-9 items-center justify-center gap-1 rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground disabled:opacity-50"
                  >
                    <Plus className="h-3.5 w-3.5" />
                    添加
                  </button>
                </div>
              )}
              {isProjectAdmin && (
                <p className="mt-2 text-xs text-muted-foreground">
                  成员以用户名标识：对方若尚未注册，会以「待注册」状态加入；对方在 「设置 →
                  没有令牌？快速开始」注册同名身份后自动生效，即可看到本项目并使用项目插件。
                </p>
              )}
            </div>
          </section>
        )}

        {section === "config" && configTab === "plugins" && (
          <section>
            <h2 className="text-sm font-medium text-foreground">解码插件</h2>
            <div className="mt-2 rounded-2xl border border-border bg-card/60 p-4">
              {plugins.length === 0 ? (
                <p className="text-sm text-muted-foreground">未配置解码插件。</p>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {plugins.map((pl) => (
                    <span
                      key={pl.id}
                      className="inline-flex items-center gap-1 rounded-md border border-border bg-muted px-2 py-0.5 text-xs text-foreground"
                    >
                      {pl.name}
                      {(isProjectAdmin || pl.owner === identity?.owner) && (
                        <button
                          type="button"
                          onClick={() => handleRemovePlugin(pl.id)}
                          title="移除插件"
                          className="text-muted-foreground hover:text-destructive"
                        >
                          <X className="h-3 w-3" />
                        </button>
                      )}
                    </span>
                  ))}
                </div>
              )}

              {canManagePlugins && (
                <div className="mt-3 border-t border-border pt-3">
                  {candidatePlugins.length === 0 ? (
                    <p className="text-xs text-muted-foreground">
                      {registeredPluginNames.length === 0
                        ? "当前没有已注册的插件。先在「插件」页启动解析器，使其注册到 Pipeline 后再关联。"
                        : isProjectAdmin
                          ? "所有已注册插件均已关联到本项目。"
                          : "你名下所有已注册插件均已关联到本项目。"}
                    </p>
                  ) : (
                    <div className="grid gap-2 sm:grid-cols-[1fr_auto]">
                      <select
                        value={pluginName}
                        onChange={(e) => setPluginName(e.target.value)}
                        aria-label="选择已注册插件"
                        className="h-9 rounded-md border border-input bg-background px-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
                      >
                        <option value="">选择已注册插件…</option>
                        {candidatePlugins.map((name) => (
                          <option key={name} value={name}>
                            {name}
                          </option>
                        ))}
                      </select>
                      <button
                        type="button"
                        onClick={handleAddPlugin}
                        disabled={addPlugin.isPending || !pluginName.trim()}
                        className="inline-flex h-9 items-center justify-center gap-1 rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground disabled:opacity-50"
                      >
                        <Plus className="h-3.5 w-3.5" />
                        添加
                      </button>
                    </div>
                  )}
                  <p className="mt-2 text-xs text-muted-foreground">
                    {isProjectAdmin
                      ? "可添加任意已注册插件；添加后项目内所有成员均可使用。"
                      : "只能添加你自己注册的插件（候选列表已按你的身份过滤）；添加后项目内所有成员均可使用。"}
                  </p>
                </div>
              )}
            </div>
          </section>
        )}

        {section === "config" && configTab === "rules" && (
          <section>
            <h2 className="text-sm font-medium text-foreground">规则</h2>
            <div className="mt-2 rounded-2xl border border-border bg-card/60 p-4">
              {rules.length === 0 ? (
                <p className="text-sm text-muted-foreground">未配置规则。</p>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {rules.map((r) => (
                    <span
                      key={r.id}
                      className="inline-flex items-center gap-1 rounded-md border border-border bg-muted px-2 py-0.5 text-xs text-foreground"
                    >
                      {r.name}
                      {isProjectAdmin && (
                        <button
                          type="button"
                          onClick={() => handleRemoveRule(r.id)}
                          title="移除规则"
                          className="text-muted-foreground hover:text-destructive"
                        >
                          <X className="h-3 w-3" />
                        </button>
                      )}
                    </span>
                  ))}
                </div>
              )}

              {isProjectAdmin && (
                <div className="mt-3 grid gap-2 border-t border-border pt-3 sm:grid-cols-[1fr_auto]">
                  <input
                    value={ruleName}
                    onChange={(e) => setRuleName(e.target.value)}
                    placeholder="规则名称"
                    className="h-9 rounded-md border border-input bg-background px-2.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
                  />
                  <button
                    type="button"
                    onClick={handleAddRule}
                    disabled={setRules.isPending || !ruleName.trim()}
                    className="inline-flex h-9 items-center justify-center gap-1 rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground disabled:opacity-50"
                  >
                    <Plus className="h-3.5 w-3.5" />
                    添加
                  </button>
                </div>
              )}
            </div>
          </section>
        )}

        {/* 会话：项目空间的主体清单，与未归属共用同一份 SessionList */}
        {section === "sessions" && (
          <section>
            <h2 className="text-sm font-medium text-foreground">会话</h2>
            <div className="mt-2">
              <SessionList
                projectId={project.id}
                selectedSessionId={selectedSessionId ?? null}
                onSelectSession={onSelectSession}
                onDeleted={onDeletedSession}
                searchInputRef={searchInputRef}
              />
            </div>
          </section>
        )}
      </div>
    </div>
  );
}
