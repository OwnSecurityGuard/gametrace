// MyCapturePage — 「我的抓包」首页（Web First · P1）。
//
// 首页按用户阶段分两种形态，而不是一张静态功能目录：
//   · 首次使用（既无项目也无会话）：一条 onboarding 步骤条，回答「先做什么」。
//   · 已抓过包（老用户日常）：项目卡片为主体，一键抓包是唯一主操作；设备收成一行 chip，会话退到最后。
// 首页只保留一个视觉重心：新用户是步骤条，老用户是项目墙。
import { useState } from "react";
import {
  Laptop,
  Smartphone,
  Server,
  Plus,
  Trash2,
  Play,
  ChevronRight,
  ChevronDown,
  Activity,
  Check,
} from "lucide-react";
import { DeviceStatusList } from "@/components/device-status";
import { useSessions, useProjects, useCreateProject, useDeleteProject } from "@/hooks/use-mcp";
import { useMyDevices } from "@/hooks/use-devices";
import type { ProjectInfo } from "@/types/project";
import type { SessionInfo } from "@/types/session";
import { toast } from "@/components/ui/toast";

function sourceLabel(source: string) {
  switch (source) {
    case "proxy":
      return "手机代理";
    case "agent":
      return "抓包探针";
    default:
      return "服务器抓包";
  }
}

function fmtTime(iso: string) {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const now = new Date();
  const diffDay = (now.getTime() - d.getTime()) / 86_400_000;
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  if (diffDay < 1 && d.getDate() === now.getDate()) return `今天 ${hh}:${mm}`;
  if (diffDay < 2) return `昨天 ${hh}:${mm}`;
  return `${d.getMonth() + 1}月${d.getDate()}日 ${hh}:${mm}`;
}

interface MyCapturePageProps {
  onStartDefault: () => void;
  onStartProject: (p: ProjectInfo) => void;
  onAgentDownload: () => void;
  onProxy: () => void;
  onSelectSession: (sessionId: string) => void;
  /** 进入项目详情页（项目作为一等组织单元） */
  onOpenProject: (projectId: string) => void;
}

export function MyCapturePage({
  onStartDefault,
  onStartProject,
  onAgentDownload,
  onProxy,
  onSelectSession,
  onOpenProject,
}: MyCapturePageProps) {
  const { data: sessionsData } = useSessions();
  const sessions = sessionsData?.sessions ?? [];
  const { data: projectsData } = useProjects();
  const projects = projectsData?.projects ?? [];
  const devices = useMyDevices();
  const createProject = useCreateProject();
  const deleteProject = useDeleteProject();

  const [creating, setCreating] = useState(false);
  const [devicesOpen, setDevicesOpen] = useState(false);

  const onlineDevices = devices.filter(
    (d) => d.state === "capturing" || d.state === "connected",
  ).length;
  // 启动码只代表"等待接入"，真的接进来以后是一台探针。
  const hasProbe = devices.some((d) => d.kind === "probe");

  const recent = [...sessions]
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
    .slice(0, 6);

  // 首跑 = 既没项目也没会话：这时候给用户一条路径，而不是三个空 section。
  const firstRun = projects.length === 0 && sessions.length === 0;

  function handleCreate(name: string, port: string) {
    const p = parseInt(port || "0", 10);
    createProject.mutate(
      { name: name.trim(), port: p > 0 ? p : 0 },
      {
        onSuccess: () => {
          setCreating(false);
          toast.success("项目已创建", name.trim());
        },
        onError: (err) => toast.error("创建失败", err.message),
      },
    );
  }

  function handleDelete(p: ProjectInfo) {
    if (!window.confirm(`删除项目「${p.name}」？`)) return;
    deleteProject.mutate(
      { id: p.id },
      {
        onSuccess: () => toast.success("已删除", p.name),
        onError: (err) => toast.error("删除失败", err.message),
      },
    );
  }

  return (
    <div className="h-full overflow-auto gt-scroll">
      <div className="mx-auto max-w-5xl space-y-8 p-6">
        <header className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h1 className="text-xl font-semibold gt-gradient-text">我的抓包</h1>
            {firstRun ? (
              <p className="mt-1 text-sm text-muted-foreground">
                三步开始：接入设备 → 建项目 → 开始抓包。端口与插件由 GameTrace 记住。
              </p>
            ) : (
              projects.length > 0 && (
                <p className="mt-1 text-sm text-muted-foreground">
                  从项目一键开始抓包，端口与插件不用再填第二遍。
                </p>
              )
            )}
          </div>
          {devices.length > 0 && (
            <button
              type="button"
              onClick={() => setDevicesOpen((v) => !v)}
              aria-expanded={devicesOpen}
              className="inline-flex shrink-0 items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-xs text-muted-foreground hover:border-primary/30 hover:text-foreground"
            >
              <Laptop className="h-3.5 w-3.5" />
              {devices.length} 台设备 · {onlineDevices} 在线
              <ChevronDown
                className={`h-3.5 w-3.5 transition-transform ${devicesOpen ? "rotate-180" : ""}`}
              />
            </button>
          )}
        </header>

        {devicesOpen && devices.length > 0 && (
          <div>
            <DeviceStatusList onSelectSession={onSelectSession} />
          </div>
        )}

        {firstRun ? (
          <section className="rounded-2xl border border-border bg-card/60 p-5">
            <h2 className="text-sm font-medium text-foreground">开始使用 GameTrace</h2>
            <div className="mt-3 space-y-2">
              <OnboardingStep
                index={1}
                title="接入一台设备"
                desc="在我的电脑上运行 GameTrace 探针，或让手机通过代理接入"
                done={hasProbe}
              >
                <button
                  type="button"
                  onClick={onAgentDownload}
                  className="inline-flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-xs text-foreground hover:border-primary/40 hover:bg-muted/40"
                >
                  <Laptop className="h-3.5 w-3.5 text-primary" />
                  我的电脑
                </button>
                <button
                  type="button"
                  onClick={onProxy}
                  className="inline-flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-xs text-foreground hover:border-primary/40 hover:bg-muted/40"
                >
                  <Smartphone className="h-3.5 w-3.5 text-primary" />
                  手机代理
                </button>
              </OnboardingStep>

              <OnboardingStep
                index={2}
                title="建一个项目（可选）"
                desc="记住默认端口与插件，之后每次从这里一键开始"
                done={projects.length > 0}
              >
                <button
                  type="button"
                  onClick={() => setCreating((v) => !v)}
                  className="inline-flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-xs text-foreground hover:border-primary/40 hover:bg-muted/40"
                >
                  <Plus className="h-3.5 w-3.5" />
                  新建项目
                </button>
                {creating && (
                  <div className="w-full">
                    <ProjectCreateForm
                      pending={createProject.isPending}
                      onCreate={handleCreate}
                      onCancel={() => setCreating(false)}
                    />
                  </div>
                )}
              </OnboardingStep>

              <OnboardingStep
                index={3}
                title="开始第一次抓包"
                desc="选择端口与解码插件，抓到的数据会自动落进会话"
                done={sessions.length > 0}
              >
                <button
                  type="button"
                  onClick={onStartDefault}
                  className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground"
                >
                  <Play className="h-3 w-3" />
                  开始抓包
                </button>
              </OnboardingStep>
            </div>
          </section>
        ) : (
          <section>
            <div className="flex items-center justify-between">
              <h2 className="text-sm font-medium text-foreground">我的项目</h2>
              {projects.length > 0 && (
                <span className="text-[11px] text-muted-foreground">{projects.length} 个项目</span>
              )}
            </div>
            <div className="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {projects.map((p) => {
                // 从已加载的 sessions 中按 project_id 派生在线/离线状态，避免在循环里调用 Hook。
                const list = sessions.filter((s) => s.session_id && s.project_id === p.id);
                const latest = list[0];
                const running = list.some((s) => s.status === "running");
                return (
                  <ProjectCard
                    key={p.id}
                    project={p}
                    latest={latest}
                    running={running}
                    onOpen={() => onOpenProject(p.id)}
                    onStart={() => onStartProject(p)}
                    onView={() => latest && onSelectSession(latest.session_id)}
                    onDelete={() => handleDelete(p)}
                  />
                );
              })}
              {creating ? (
                <ProjectCreateForm
                  pending={createProject.isPending}
                  onCreate={handleCreate}
                  onCancel={() => setCreating(false)}
                />
              ) : (
                <button
                  type="button"
                  onClick={() => setCreating(true)}
                  className="flex min-h-[132px] items-center justify-center gap-1.5 rounded-xl border border-dashed border-border text-sm text-muted-foreground hover:border-primary/40 hover:text-foreground"
                >
                  <Plus className="h-4 w-4" />
                  新建项目
                </button>
              )}
            </div>
            {projects.length === 0 && !creating && (
              <p className="mt-2 text-xs text-muted-foreground">
                把常用的端口与插件存成项目，下次从这里一键开始抓包。
              </p>
            )}
          </section>
        )}

        {recent.length > 0 && (
          <section>
            <h2 className="text-sm font-medium text-foreground">最近会话</h2>
            <div className="mt-3 space-y-2">
              {recent.map((s) => {
                const projectName = s.project_id
                  ? projects.find((p) => p.id === s.project_id)?.name
                  : undefined;
                return (
                  <button
                    key={s.session_id}
                    type="button"
                    onClick={() => onSelectSession(s.session_id)}
                    className="flex w-full items-center gap-3 rounded-xl border border-border bg-card/60 p-3 text-left transition-colors hover:border-primary/30 hover:bg-muted/40"
                  >
                    <span
                      className={`h-2 w-2 shrink-0 rounded-full ${
                        s.status === "running" ? "bg-emerald-500" : "bg-muted-foreground/40"
                      }`}
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <p className="truncate text-sm font-medium">
                          {projectName || s.plugin || "未命名会话"}
                        </p>
                        {s.status === "running" && (
                          <span className="rounded bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-medium text-emerald-600 dark:text-emerald-400">
                            抓包中
                          </span>
                        )}
                      </div>
                      <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
                        {fmtTime(s.started_at)} · {sourceLabel(s.source)}
                        {s.port ? ` · 端口 ${s.port}` : ""}
                        {projectName && s.plugin ? ` · ${s.plugin}` : ""}
                      </p>
                    </div>
                    <div className="shrink-0 text-right">
                      <p className="font-mono text-sm text-foreground">
                        {s.events?.toLocaleString() ?? 0}{" "}
                        <span className="text-[10px] text-muted-foreground">events</span>
                      </p>
                      <p className="font-mono text-[11px] text-muted-foreground">
                        {s.raw_packets?.toLocaleString() ?? 0} packets
                      </p>
                    </div>
                    <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
                  </button>
                );
              })}
            </div>
          </section>
        )}

        {!firstRun && (
          <button
            type="button"
            onClick={onStartDefault}
            className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground hover:text-foreground"
          >
            <Server className="h-3.5 w-3.5" />
            在服务器网卡上直接抓包
          </button>
        )}
      </div>
    </div>
  );
}

function OnboardingStep({
  index,
  title,
  desc,
  done,
  children,
}: {
  index: number;
  title: string;
  desc: string;
  done: boolean;
  children?: React.ReactNode;
}) {
  return (
    <div
      className={`flex items-start gap-3 rounded-xl border border-border p-3 ${
        done ? "bg-muted/30" : "bg-background"
      }`}
    >
      <span
        className={`mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center text-[11px] ${
          done
            ? "rounded-full bg-emerald-500/15 text-emerald-600 dark:text-emerald-400"
            : "rounded-full border border-border text-muted-foreground"
        }`}
      >
        {done ? <Check className="h-3 w-3" /> : index}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <p className="text-sm font-medium">{title}</p>
          {done && (
            <span className="text-[11px] text-emerald-600 dark:text-emerald-400">已完成</span>
          )}
        </div>
        <p className="mt-0.5 text-xs text-muted-foreground">{desc}</p>
        {!done && children && <div className="mt-2 flex flex-wrap items-start gap-2">{children}</div>}
      </div>
    </div>
  );
}

function ProjectCreateForm({
  pending,
  onCreate,
  onCancel,
}: {
  pending: boolean;
  onCreate: (name: string, port: string) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [port, setPort] = useState("");
  return (
    <div className="rounded-xl border border-primary/40 bg-card/60 p-3">
      <input
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && name.trim()) onCreate(name, port);
        }}
        placeholder="项目名称，如 Godot Game"
        className="h-9 w-full rounded-md border border-input bg-background px-2.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
      />
      <input
        value={port}
        onChange={(e) => setPort(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && name.trim()) onCreate(name, port);
        }}
        placeholder="默认端口（可选）"
        inputMode="numeric"
        className="mt-2 h-9 w-full rounded-md border border-input bg-background px-2.5 font-mono text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
      />
      <div className="mt-2 flex gap-2">
        <button
          type="button"
          onClick={() => onCreate(name, port)}
          disabled={pending || !name.trim()}
          className="h-8 flex-1 rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground disabled:opacity-50"
        >
          {pending ? "创建中…" : "创建"}
        </button>
        <button
          type="button"
          onClick={onCancel}
          className="h-8 rounded-md border border-border px-3 text-xs text-muted-foreground hover:bg-muted/50"
        >
          取消
        </button>
      </div>
    </div>
  );
}

function ProjectCard({
  project,
  latest,
  running,
  onOpen,
  onStart,
  onView,
  onDelete,
}: {
  project: ProjectInfo;
  latest?: SessionInfo;
  running: boolean;
  onOpen: () => void;
  onStart: () => void;
  onView: () => void;
  onDelete: () => void;
}) {
  const meta = [
    project.default_plugin ? `插件 ${project.default_plugin}` : "默认插件未设置",
    project.default_port ? `端口 ${project.default_port}` : "",
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <div className="group rounded-xl border border-border bg-card/60 p-3.5 transition-colors hover:border-primary/40">
      <div className="flex items-start gap-2">
        <span
          className={`mt-1.5 h-2 w-2 shrink-0 rounded-full ${
            running ? "bg-emerald-500" : "bg-muted-foreground/40"
          }`}
          title={running ? "抓包中" : "未抓包"}
        />
        <button type="button" onClick={onOpen} title="进入项目详情" className="min-w-0 flex-1 text-left">
          <span className="flex items-center gap-1">
            <span className="truncate text-sm font-medium group-hover:underline">
              {project.name}
            </span>
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          </span>
          <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">{meta}</span>
          <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
            {running
              ? "抓包中"
              : latest
                ? `最近 ${fmtTime(latest.started_at)} · ${latest.events?.toLocaleString() ?? 0} events`
                : "暂无会话"}
          </span>
        </button>
        <button
          type="button"
          onClick={onDelete}
          title="删除项目"
          className="rounded-md p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-muted/50 hover:text-destructive group-hover:opacity-100"
        >
          <Trash2 className="h-3.5 w-3.5" />
        </button>
      </div>
      <button
        type="button"
        onClick={running ? onView : onStart}
        className={`mt-3 inline-flex w-full items-center justify-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium ${
          running
            ? "border border-border text-foreground hover:bg-muted/50"
            : "bg-primary text-primary-foreground"
        }`}
      >
        {running ? (
          <>
            <Activity className="h-3.5 w-3.5" />
            查看会话
          </>
        ) : (
          <>
            <Play className="h-3 w-3" />
            开始抓包
          </>
        )}
      </button>
    </div>
  );
}
