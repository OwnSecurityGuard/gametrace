// WorkspacePage — 工作台首页（用户级空间的主体）。
//
// 首页按用户阶段分两种形态，而不是一张静态功能目录：
//   · 首次使用（既无项目也无会话）：一条 onboarding 步骤条，回答「先做什么」。
//   · 已抓过包（老用户日常）：项目墙是唯一重心，会话收在项目卡片里就地展开。
//
// 「最近会话」不再作为独立 section 出现在这里 —— 会话属于项目 / 会话空间，
// 把它们提到用户级首页正是旧版空间错位的原因（见 lib/routes.ts 的层级说明）。
import { useState } from "react";
import {
  Laptop,
  Smartphone,
  Plus,
  Trash2,
  Play,
  ChevronRight,
  ChevronDown,
  Check,
  Folder,
} from "lucide-react";
import { Input } from "@/components/ui/input";
import { DeviceStatusList } from "@/components/device-status";
import { useSessions, useProjects, useCreateProject, useDeleteProject } from "@/hooks/use-mcp";
import { useMyDevices } from "@/hooks/use-devices";
import { navigate } from "@/lib/router";
import { UNASSIGNED_HREF, projectHref } from "@/lib/routes";
import type { ProjectInfo } from "@/types/project";
import type { SessionInfo } from "@/types/session";
import { toast } from "@/components/ui/toast";

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

interface WorkspacePageProps {
  /** 打开抓包通道弹窗；projectId 非空即预填该项目的端口与插件。 */
  onStartCapture: (projectId: string | null) => void;
  onAgentDownload: () => void;
  onProxy: () => void;
  onSelectSession: (sessionId: string) => void;
}

export function WorkspacePage({
  onStartCapture,
  onAgentDownload,
  onProxy,
  onSelectSession,
}: WorkspacePageProps) {
  const { data: sessionsData } = useSessions();
  // 后端按开始时间倒序返回，派生时保持这个顺序即可。
  const sessions = sessionsData?.sessions ?? [];
  const { data: projectsData } = useProjects();
  const projects = projectsData?.projects ?? [];
  const devices = useMyDevices();
  const createProject = useCreateProject();
  const deleteProject = useDeleteProject();

  const [creating, setCreating] = useState(false);
  const [devicesOpen, setDevicesOpen] = useState(false);

  const onlineDevices = devices.filter((d) => d.state === "capturing" || d.state === "connected").length;
  const hasProbe = devices.length > 0;

  const orphans = sessions.filter((s) => s.session_id && !s.project_id);
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
            <h1 className="text-xl font-semibold gt-gradient-text">工作台</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              {firstRun
                ? "三步开始：接入设备 → 建项目 → 开始抓包。端口与插件由 GameTrace 记住。"
                : "从项目一键开始抓包，端口与插件不用再填第二遍。"}
            </p>
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
          <FirstRun
            hasProbe={hasProbe}
            projectCount={projects.length}
            sessionCount={sessions.length}
            creating={creating}
            createPending={createProject.isPending}
            onToggleCreating={setCreating}
            onCreate={handleCreate}
            onAgentDownload={onAgentDownload}
            onProxy={onProxy}
            onStartCapture={() => onStartCapture(null)}
          />
        ) : (
          <section>
            <div className="flex items-center justify-between">
              <h2 className="text-sm font-medium text-foreground">我的项目</h2>
              {projects.length > 0 && (
                <span className="text-micro text-muted-foreground">{projects.length} 个项目</span>
              )}
            </div>
            <div className="mt-3 grid items-start gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {projects.map((p) => (
                <ProjectCard
                  key={p.id}
                  project={p}
                  sessions={sessions.filter((s) => s.session_id && s.project_id === p.id)}
                  onOpen={() => navigate(projectHref(p.id))}
                  onStart={() => onStartCapture(p.id)}
                  onSelectSession={onSelectSession}
                  onDelete={() => handleDelete(p)}
                />
              ))}
              {orphans.length > 0 && (
                <OrphanCard
                  count={orphans.length}
                  onOpen={() => navigate(UNASSIGNED_HREF)}
                  onStart={() => onStartCapture(null)}
                />
              )}
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
      </div>
    </div>
  );
}

function FirstRun({
  hasProbe,
  projectCount,
  sessionCount,
  creating,
  createPending,
  onToggleCreating,
  onCreate,
  onAgentDownload,
  onProxy,
  onStartCapture,
}: {
  hasProbe: boolean;
  projectCount: number;
  sessionCount: number;
  creating: boolean;
  createPending: boolean;
  onToggleCreating: (v: boolean) => void;
  onCreate: (name: string, port: string) => void;
  onAgentDownload: () => void;
  onProxy: () => void;
  onStartCapture: () => void;
}) {
  return (
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
          done={projectCount > 0}
        >
          <button
            type="button"
            onClick={() => onToggleCreating(!creating)}
            className="inline-flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-xs text-foreground hover:border-primary/40 hover:bg-muted/40"
          >
            <Plus className="h-3.5 w-3.5" />
            新建项目
          </button>
          {creating && (
            <div className="w-full">
              <ProjectCreateForm
                pending={createPending}
                onCreate={onCreate}
                onCancel={() => onToggleCreating(false)}
              />
            </div>
          )}
        </OnboardingStep>

        <OnboardingStep
          index={3}
          title="开始第一次抓包"
          desc="选择抓包通道与端口，抓到的数据会自动落进会话"
          done={sessionCount > 0}
        >
          <button
            type="button"
            onClick={onStartCapture}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground"
          >
            <Play className="h-3 w-3" />
            开始抓包
          </button>
        </OnboardingStep>
      </div>
    </section>
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
        className={`mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center text-micro ${
          done
            ? "rounded-full bg-success/15 text-success"
            : "rounded-full border border-border text-muted-foreground"
        }`}
      >
        {done ? <Check className="h-3 w-3" /> : index}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <p className="text-sm font-medium">{title}</p>
          {done && <span className="text-micro text-success">已完成</span>}
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
      <Input
        name="project-name"
        aria-label="项目名称"
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && name.trim()) onCreate(name, port);
        }}
        placeholder="项目名称，如 Godot Game"
      />
      <Input
        name="project-default-port"
        aria-label="默认端口"
        className="mt-2 font-mono"
        value={port}
        onChange={(e) => setPort(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && name.trim()) onCreate(name, port);
        }}
        placeholder="默认端口（可选）"
        inputMode="numeric"
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

/** 首页的项目卡片：项目名 + 一键抓包 + 就地展开的最近会话。 */
function ProjectCard({
  project,
  sessions,
  onOpen,
  onStart,
  onSelectSession,
  onDelete,
}: {
  project: ProjectInfo;
  sessions: SessionInfo[];
  onOpen: () => void;
  onStart: () => void;
  onSelectSession: (sessionId: string) => void;
  onDelete: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const running = sessions.some((s) => s.status === "running");
  const latest = sessions[0];
  const shown = expanded ? sessions.slice(0, 4) : [];

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
            running ? "bg-success" : "bg-muted-foreground/40"
          }`}
          title={running ? "抓包中" : "未抓包"}
        >
          <span className="sr-only">{running ? "抓包中" : "未抓包"}</span>
        </span>
        <button type="button" onClick={onOpen} title="进入项目" className="min-w-0 flex-1 text-left">
          <span className="flex items-center gap-1">
            <span className="truncate text-sm font-medium group-hover:underline">{project.name}</span>
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          </span>
          <span className="mt-0.5 block truncate text-micro text-muted-foreground">{meta}</span>
          <span className="mt-0.5 block truncate text-micro text-muted-foreground">
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

      <div className="mt-3 flex gap-2">
        <button
          type="button"
          onClick={onStart}
          className="inline-flex flex-1 items-center justify-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground"
        >
          <Play className="h-3 w-3" />
          开始抓包
        </button>
        {sessions.length > 0 && (
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            aria-expanded={expanded}
            title="展开最近会话"
            className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1.5 text-xs text-muted-foreground hover:bg-muted/50 hover:text-foreground"
          >
            {sessions.length}
            <ChevronDown className={`h-3.5 w-3.5 transition-transform ${expanded ? "rotate-180" : ""}`} />
          </button>
        )}
      </div>

      {expanded && (
        <div className="mt-2 space-y-1">
          {shown.map((s) => (
            <button
              key={s.session_id}
              type="button"
              onClick={() => onSelectSession(s.session_id)}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-muted/50"
            >
              <span
                className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                  s.status === "running" ? "bg-success" : "bg-muted-foreground/40"
                }`}
              />
              <span className="min-w-0 flex-1 truncate text-xs">
                {fmtTime(s.started_at)} · {s.plugin || "未设插件"}
              </span>
              <span className="shrink-0 font-mono text-micro text-muted-foreground">
                {s.events?.toLocaleString() ?? 0}
              </span>
            </button>
          ))}
          {sessions.length > shown.length && (
            <button
              type="button"
              onClick={onOpen}
              className="w-full rounded-md px-2 py-1 text-left text-micro text-primary hover:underline"
            >
              全部 {sessions.length} 个会话
            </button>
          )}
        </div>
      )}
    </div>
  );
}

/** 未归属会话的入口卡：项目墙里的兜底桶，避免孤儿会话在首页消失。 */
function OrphanCard({ count, onOpen, onStart }: { count: number; onOpen: () => void; onStart: () => void }) {
  return (
    <div className="rounded-xl border border-dashed border-border bg-card/40 p-3.5">
      <div className="flex items-center gap-2">
        <Folder className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate text-sm font-medium">未归属抓包</span>
        <span className="ml-auto shrink-0 font-mono text-micro text-muted-foreground">{count}</span>
      </div>
      <p className="mt-1 text-micro text-muted-foreground">
        没有归进任何项目的会话，在这里统一查看与批量清理。
      </p>
      <div className="mt-3 flex gap-2">
        <button
          type="button"
          onClick={onOpen}
          className="inline-flex flex-1 items-center justify-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-xs text-foreground hover:bg-muted/50"
        >
          打开清单
          <ChevronRight className="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          onClick={onStart}
          className="inline-flex items-center justify-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs text-muted-foreground hover:bg-muted/50 hover:text-foreground"
        >
          <Play className="h-3 w-3" />
          不归属抓包
        </button>
      </div>
    </div>
  );
}
