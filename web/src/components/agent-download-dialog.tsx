import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import {
  Download,
  Server,
  Radio,
  X,
  Monitor,
  Check,
  Play,
  Loader2,
  AlertTriangle,
  Cpu,
} from "lucide-react";
import { useAgentDownloadOptions, useListProbes } from "@/hooks/use-mcp";
import { authHeaders, withTokenParam } from "@/lib/auth";
import { toast } from "@/components/ui/toast";
import type { AgentPlatform } from "@/types/agent";

interface AgentDownloadDialogProps {
  open: boolean;
  onClose: () => void;
  /** 探针接入后「开始抓包」：交给外层打开开始抓包弹窗并预选这台机器。 */
  onStartCapture?: (probeId: string) => void;
}

/** 从 UA 尽力推断用户的操作系统/架构，用于默认推荐下载平台。 */
function detectPlatform(): { os: string; arch: string } {
  if (typeof navigator === "undefined") return { os: "windows", arch: "amd64" };
  const ua = navigator.userAgent;
  const os = /Mac/.test(ua) ? "darwin" : /Linux/.test(ua) ? "linux" : "windows";
  const uad = (navigator as { userAgentData?: { architecture?: string } }).userAgentData;
  const archHint = (uad?.architecture ?? "").toLowerCase();
  const arch =
    archHint.includes("arm") || /arm/i.test(ua)
      ? "arm64"
      : /x64|win64|amd64|_64/i.test(ua)
        ? "amd64"
        : "amd64";
  return { os, arch };
}

/**
 * 远程 Agent 下载对话框（Web First · 多平台）：
 * 只需选目标平台 —— 回连地址/token 打包进 zip，抓包端口与解码插件**不在下载时决定**，
 * 探针接入后由「开始抓包」在 Web 上下发（改端口/换插件都不必重下探针）。
 * 平台取自服务端预置产物，不再依赖服务端平台。
 */
export function AgentDownloadDialog({ open, onClose, onStartCapture }: AgentDownloadDialogProps) {
  // 手点「现场编译」（或自动兜底中）后高频轮询，直到后端产物就绪 / 编译结束。
  const [buildTrigger, setBuildTrigger] = useState<string | null>(null);
  const { data, isLoading } = useAgentDownloadOptions(
    buildTrigger !== null ? { refetchIntervalSec: 2500 } : undefined,
  );

  const opts = (data ?? null) as null | NonNullable<typeof data>;
  const platforms = opts?.platforms ?? [];
  const available = platforms.filter((p) => p.available);

  // 编译结束（产物就绪，或后端已不再处于 building——无论成败）即停止轮询。
  useEffect(() => {
    if (!buildTrigger) return;
    const p = platforms.find((x) => `${x.os}/${x.arch}` === buildTrigger);
    if (p && (p.available || !p.building)) setBuildTrigger(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  // 阶段：configure（选平台 + 下载）→ awaiting（等待探针接入）
  const [phase, setPhase] = useState<"configure" | "awaiting">("configure");
  const [os, setOs] = useState("windows");
  const [arch, setArch] = useState("amd64");
  const [busy, setBusy] = useState(false);
  // 下载前已存在的探针 id：用于认出"这次新接入的那台机器"。
  const knownProbeIds = useRef<Set<string>>(new Set());

  // 打开/拿到平台时：按用户 UA 推荐首个可用平台。
  useEffect(() => {
    if (!open) return;
    setBusy(false);
    setPhase("configure");
    if (available.length > 0) {
      const det = detectPlatform();
      const match = available.find((p) => p.os === det.os && p.arch === det.arch) ?? available[0];
      if (match) {
        setOs(match.os);
        setArch(match.arch);
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, opts?.registry_port]);

  // awaiting 阶段轮询探针列表，驱动「等待接入 → 已接入」。
  const { data: probesData } = useListProbes();
  const probes = probesData?.probes ?? [];
  const onlineProbes = useMemo(
    () =>
      probes
        .filter((p) => p.connection_state === "online")
        .sort((a, b) => (a.last_seen_at < b.last_seen_at ? 1 : -1)),
    [probes],
  );
  // 优先取这次新接入的探针；没有新面孔就取最近活跃的那台。
  const attachedProbe =
    onlineProbes.find((p) => !knownProbeIds.current.has(p.probe_id)) ?? onlineProbes[0];

  const selectedPlatform = platforms.find((p) => p.os === os && p.arch === arch);

  /** 请求服务端现场编译指定平台产物（POST /agent/build，幂等：已有产物直接返回可用）。 */
  async function handleBuild(p: AgentPlatform) {
    const key = `${p.os}/${p.arch}`;
    setBuildTrigger(key);
    try {
      const resp = await fetch(`/agent/build?platform=${encodeURIComponent(key)}`, {
        method: "POST",
        headers: authHeaders(),
      });
      const body = (await resp.json().catch(() => ({}))) as {
        status?: string;
        message?: string;
      };
      if (!resp.ok || body.status === "error") {
        toast.error("现场编译启动失败", body.message ?? `HTTP ${resp.status}`);
        setBuildTrigger(null);
      } else if (body.status === "available") {
        // 后端已有该平台产物，无需重新编译——直接放行下载。
        setBuildTrigger(null);
        toast.success("产物已就绪", `${p.label} 已在服务端预置，可直接下载`);
      }
      // status == building：保持轮询，等后端产出后刷新平台列表。
    } catch (e) {
      toast.error("现场编译请求失败", e instanceof Error ? e.message : String(e));
      setBuildTrigger(null);
    }
  }

  async function handleDownload() {
    if (!selectedPlatform) {
      toast.error("请选择操作系统", "当前没有任何可下载的平台产物");
      return;
    }
    if (!selectedPlatform.available) {
      toast.error(
        "该平台尚未就绪",
        selectedPlatform.buildable
          ? "请先在页面点「现场编译」，服务器生成产物后再下载"
          : "该平台需在镜像构建期预置（BUILD_DARWIN_AGENT=1）或经 GT_AGENT_BIN_DIR 补充",
      );
      return;
    }
    if (!opts?.registry_addr) {
      toast.error("服务端信息未就绪", "请稍后重试");
      return;
    }
    // 只传平台：回连地址由服务端解析（GT_PUBLIC_HOST 或请求回推），token 走请求头。
    const url = `/download/agent?platform=${encodeURIComponent(
      `${selectedPlatform.os}/${selectedPlatform.arch}`,
    )}`;
    const markAttached = () => {
      knownProbeIds.current = new Set(probes.map((p) => p.probe_id));
      setPhase("awaiting");
    };
    setBusy(true);
    try {
      const resp = await fetch(url, { headers: authHeaders() });
      if (!resp.ok) {
        const txt = (await resp.text().catch(() => "")) || `HTTP ${resp.status}`;
        toast.error("探针下载失败", txt.slice(0, 200));
        return;
      }
      const blob = await resp.blob();
      const objUrl = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = objUrl;
      a.download = `gt-agent-${selectedPlatform.os}-${selectedPlatform.arch}.zip`;
      a.rel = "noopener";
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(objUrl);

      markAttached();
      toast.success("探针已下载", "解压运行后即可在「开始抓包」里选到这台机器");
    } catch (e) {
      // fetch 通道失败（网络层错误，如大响应经代理/中继链路被截断）：退回浏览器
      // 原生下载——走下载管理器的另一条传输路径；凭证经查询参数携带（anchor 无法
      // 自定义请求头，withTokenParam 是 SSE 等无头传输的既有约定）。
      try {
        const a = document.createElement("a");
        a.href = withTokenParam(url);
        a.download = `gt-agent-${selectedPlatform.os}-${selectedPlatform.arch}.zip`;
        a.rel = "noopener";
        document.body.appendChild(a);
        a.click();
        a.remove();
        markAttached();
        toast.info(
          "已切换为浏览器直接下载",
          "fetch 通道失败，已自动改走原生下载；如未开始请检查网络后重试",
        );
      } catch {
        toast.error("下载失败", e instanceof Error ? e.message : String(e));
      }
    } finally {
      setBusy(false);
    }
  }

  const dialogTitle = phase === "awaiting" ? "等待探针接入" : "下载抓包探针";
  const dialogDesc =
    phase === "awaiting"
      ? "在目标电脑解压并双击运行探针，接入后回到「开始抓包」选这台机器。"
      : "只需选择目标操作系统：回连地址与凭证已打包进 zip。抓包端口与解码插件在「开始抓包」时指定。";

  return (
    <Dialog
      open={open}
      onClose={onClose}
      icon={<Download className="h-5 w-5" />}
      title={dialogTitle}
      description={dialogDesc}
      footer={
        phase === "awaiting" ? (
          <>
            <Button variant="outline" onClick={() => setPhase("configure")}>
              <Download className="h-4 w-4" />
              下载另一个探针
            </Button>
            <Button variant="outline" onClick={() => onClose()} className="ml-auto">
              <X className="h-4 w-4" />
              关闭
            </Button>
            <Button
              onClick={() => attachedProbe && onStartCapture?.(attachedProbe.probe_id)}
              disabled={!attachedProbe}
            >
              <Play className="h-4 w-4" />
              开始抓包
            </Button>
          </>
        ) : (
          <>
            <Button variant="outline" onClick={onClose}>
              <X className="h-4 w-4" />
              关闭
            </Button>
            <Button onClick={handleDownload} disabled={busy || isLoading || !opts?.registry_addr}>
              <Download className="h-4 w-4" />
              {busy ? "打包下载中…" : "下载探针"}
            </Button>
          </>
        )
      }
    >
      {phase === "awaiting" ? (
        <AwaitingAgentPanel
          attached={!!attachedProbe}
          probeName={attachedProbe?.name || attachedProbe?.hostname}
        />
      ) : (
        <div className="space-y-3">
          {/* 目标平台 */}
          <div>
            <label className="flex items-center gap-1.5 text-sm font-medium">
              <Monitor className="h-3.5 w-3.5 text-muted-foreground" />
              目标操作系统（在哪个电脑上抓包）
            </label>
            <div className="mt-1.5 grid grid-cols-2 gap-2">
              {platforms.map((p) => (
                <div key={`${p.os}/${p.arch}`}>
                  <PlatformOption
                    p={p}
                    selected={os === p.os && arch === p.arch}
                    onSelect={() => {
                      // 所有候选都可选中：未就绪平台交由下方状态判定（现场编译/需预置）。
                      setOs(p.os);
                      setArch(p.arch);
                    }}
                  />
                  {/* 未就绪平台的状态判定：编译中 → 可现场编译 → 需镜像预置 */}
                  {!p.available && !(os === p.os && arch === p.arch) && (
                    <p className="mt-1 flex items-center justify-center text-micro">
                      {p.building ? (
                        <span className="inline-flex items-center gap-1 text-muted-foreground">
                          <Loader2 className="h-3 w-3 animate-spin" />
                          服务器编译中…
                        </span>
                      ) : p.buildable ? (
                        <button
                          type="button"
                          onClick={(e) => {
                            e.stopPropagation();
                            setOs(p.os);
                            setArch(p.arch);
                            handleBuild(p);
                          }}
                          className="inline-flex items-center gap-1 text-primary hover:underline"
                        >
                          <Cpu className="h-3 w-3" />
                          现场编译
                        </button>
                      ) : (
                        <span className="text-muted-foreground">需镜像预置</span>
                      )}
                    </p>
                  )}
                </div>
              ))}
            </div>
            {selectedPlatform && !selectedPlatform.available && (
              <p className="mt-1 text-xs text-destructive">
                {selectedPlatform.building
                  ? `服务器正在现场编译 ${selectedPlatform.label}，稍候即可下载。`
                  : selectedPlatform.buildable
                    ? `点上面的「现场编译」，服务器会为 ${selectedPlatform.label} 生成产物；编译结果平台共享，其他机器无需重复编译。`
                    : `${selectedPlatform.label} 未预置：macOS 需镜像构建期开启 BUILD_DARWIN_AGENT=1、Linux ARM64 需 BUILD_ARM_AGENT=1，或经 GT_AGENT_BIN_DIR 提供产物。`}
              </p>
            )}
            <p className="mt-1.5 text-xs text-muted-foreground">
              抓包端口与解码插件不在这里选 —— 探针接入后，在「开始抓包」里选这台机器并指定。
            </p>
          </div>

          {/* 服务端回连地址（只读）：来自 GT_PUBLIC_HOST 或请求回推，不开放手填 */}
          <div className="rounded-md border border-border bg-muted/40 px-3 py-2">
            <label className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
              <Server className="h-3.5 w-3.5" />
              探针回连地址（已写入下载包）
            </label>
            {isLoading || !opts?.registry_addr ? (
              <p className="mt-1 text-xs text-muted-foreground">正在读取服务端信息…</p>
            ) : (
              <div className="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs font-mono text-foreground">
                <span className="text-muted-foreground">回连</span>
                <span>{opts.registry_addr}</span>
                <span className="text-muted-foreground">推流</span>
                <span>{opts.ingest_addr}</span>
              </div>
            )}
            {opts && opts.addr_source !== "env" && (
              <p className="mt-1.5 flex gap-1.5 text-micro text-warning">
                <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                服务端未配置 GT_PUBLIC_HOST，地址是按当前访问方式推测的。Docker / 公网部署请在服务端设置
                GT_PUBLIC_HOST（必要时配 GT_PUBLIC_REGISTRY_PORT / GT_PUBLIC_INGEST_PORT），否则远端探针可能连不上。
              </p>
            )}
            {opts?.message && <p className="mt-1.5 text-micro text-muted-foreground">{opts.message}</p>}
          </div>
        </div>
      )}
    </Dialog>
  );
}

function PlatformOption({
  p,
  selected,
  onSelect,
}: {
  p: AgentPlatform;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={selected}
      className={`flex items-center gap-2 rounded-md border px-2.5 py-2 text-sm transition-colors ${
        selected
          ? "border-primary/60 bg-primary/10 text-foreground"
          : "border-border bg-background text-muted-foreground"
      } ${p.available ? "cursor-pointer hover:bg-muted/60" : "cursor-pointer hover:bg-muted/40"}`}
    >
      {selected ? (
        <Check className="h-4 w-4 text-primary" />
      ) : (
        <span className="h-4 w-4 rounded-full border border-border" />
      )}
      <span className="truncate">{p.label}</span>
      {p.exe ? <span className="ml-auto font-mono text-2xs text-muted-foreground">.exe</span> : null}
    </button>
  );
}

function AwaitingAgentPanel({ attached, probeName }: { attached: boolean; probeName?: string }) {
  const steps = [
    {
      label: "探针已生成并下载",
      detail: "zip 已保存到浏览器下载目录",
      done: true,
    },
    {
      label: "解压并双击运行探针",
      detail: "在目标电脑解压 zip，双击运行 gt-agent（.exe 视平台而定）",
      done: true,
    },
    {
      label: attached ? "探针已接入" : "等待探针接入…",
      detail: attached
        ? `${probeName ?? "探针"} 已在线，可在「开始抓包」里选它并指定端口与解析器`
        : "探针会回连服务端并注册为在线探针（无需任何参数）",
      done: attached,
    },
  ];
  return (
    <div className="space-y-3">
      <ol className="space-y-2.5">
        {steps.map((s, i) => (
          <li key={i} className="flex gap-2.5">
            <span
              className={`mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full ${
                s.done ? "bg-primary/15 text-primary" : "bg-muted text-muted-foreground"
              }`}
            >
              {s.done ? <Check className="h-3.5 w-3.5" /> : <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            </span>
            <div>
              <p className={`text-sm ${s.done ? "font-medium text-foreground" : "text-muted-foreground"}`}>
                {s.label}
              </p>
              <p className="text-xs text-muted-foreground">{s.detail}</p>
            </div>
          </li>
        ))}
      </ol>

      {attached ? (
        <div className="flex items-center gap-2 rounded-md border border-success/40 bg-success/10 px-3 py-2 text-xs text-success">
          <Radio className="h-3.5 w-3.5 shrink-0" />
          {probeName ?? "探针"} 已在线，点「开始抓包」指定端口与解析器。
        </div>
      ) : (
        <p className="rounded-md border border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
          正在等待探针回连… 若长时间无变化，请确认目标电脑能访问上面的回连地址，且压缩包中的
          config.embedded.json 与探针在同一目录。
        </p>
      )}
    </div>
  );
}
