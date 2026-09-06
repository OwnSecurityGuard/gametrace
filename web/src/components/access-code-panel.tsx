import { useEffect, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Copy,
  Check,
  KeyRound,
  TerminalSquare,
  MonitorSmartphone,
  Clock,
} from "lucide-react";
import { useAgentDownloadOptions, useCreateAccessCode } from "@/hooks/use-mcp";
import { DeviceStatusList } from "@/components/device-status";
import { toast } from "@/components/ui/toast";
import type { CreateAccessCodeResult } from "@/types/access-code";

/** 从 UA 尽力推断平台（OS + 架构），用于默认推荐接入命令（不影响可选平台列表）。 */
function detectPlatform(): { os: string; arch: string } {
  if (typeof navigator === "undefined") return { os: "windows", arch: "amd64" };
  const ua = navigator.userAgent;
  const os = /Mac/.test(ua) ? "darwin" : /Linux/.test(ua) ? "linux" : "windows";
  const arch = /arm|aarch64/i.test(ua) ? "arm64" : "amd64";
  return { os, arch };
}

function formatExpiry(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString();
}

/**
 * 「我的接入」面板（纯设备接入）：普通用户选平台 → 生成 GT-XXXX 启动码 →
 * 复制一条接入命令，在目标机执行即可自动注册设备、领取回连配置并接入，
 * 全程无需手填 token/回连地址。抓包端口与解码插件不在接入时决定 ——
 * 设备在「我的设备」里显示在线后，用「开始抓包」选它并指定。
 * 成员管理（邀请码/账号列表）已拆分至 members-admin-dialog。
 */
export function AccessCodePanel() {
  const { data, isLoading } = useAgentDownloadOptions();
  const createCode = useCreateAccessCode();

  const opts = (data ?? null) as null | NonNullable<typeof data>;
  const platforms = opts?.platforms ?? [];
  const available = platforms.filter((p) => p.available);

  const det = detectPlatform();
  const [os, setOs] = useState<string>(det.os);
  // 同 OS 下智能选取当前机器的 CPU 架构；用户切换 OS 时尽量保留该架构。
  const [arch, setArch] = useState<string>(det.arch);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<CreateAccessCodeResult | null>(null);
  const [copiedField, setCopiedField] = useState<"code" | "cmd" | null>(null);

  // 打开/拿到服务端信息时：按当前机器推荐匹配的平台（OS + 架构均匹配优先）。
  useEffect(() => {
    if (isLoading || available.length === 0) return;
    const osWant = det.os === "darwin" ? "linux" : det.os;
    const best =
      available.find((p) => p.os === osWant && p.arch === det.arch) ??
      available.find((p) => p.os === osWant) ??
      available[0];
    if (best) {
      setOs(best.os);
      setArch(best.arch);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isLoading, available.length]);

  const selectedPlatform =
    platforms.find((p) => p.os === os && p.arch === arch) ??
    platforms.find((p) => p.os === os);

  async function handleGenerate() {
    if (!selectedPlatform) {
      toast.error("请选择目标操作系统", "当前没有可用的预置平台");
      return;
    }
    setBusy(true);
    try {
      const res = await createCode.mutateAsync({
        platform: selectedPlatform.available ? `${selectedPlatform.os}/${selectedPlatform.arch}` : `${os}/amd64`,
      });
      setCreated(res);
      toast.success("启动码已生成", "复制下方命令并在目标机执行即可接入");
    } catch (e) {
      toast.error("生成失败", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function copyText(
    field: "code" | "cmd",
    text: string,
  ) {
    try {
      await navigator.clipboard.writeText(text);
      setCopiedField(field);
      toast.success("已复制");
      setTimeout(() => setCopiedField(null), 1500);
    } catch {
      toast.error("复制失败", "请手动选择复制");
    }
  }

  const isWindows = os === "windows";
  const base = typeof window !== "undefined" ? window.location.origin : "";
  const platformName = selectedPlatform ? `${selectedPlatform.os}/${selectedPlatform.arch}` : `${os}/amd64`;

  // 接入命令：Linux/Mac 走 /setup.sh，Windows 走 /setup.ps1（均由服务端下发一键脚本）。
  const linuxCmd = useMemo(
    () => (created ? `curl -fsSL "${base}/setup.sh?code=${created.code}&platform=${encodeURIComponent(platformName)}" | bash` : ""),
    [created, base, platformName],
  );
  const windowsCmd = useMemo(
    () => (created ? `irm "${base}/setup.ps1?code=${created.code}&platform=${encodeURIComponent(platformName)}" | iex` : ""),
    [created, base, platformName],
  );
  const activeCmd = isWindows ? windowsCmd : linuxCmd;

  return (
    <div className="space-y-4">
      {/* 0. 我的设备：接入状态闭环（生成启动码后，这里实时反映 等待接入 → 已连接 → 正在抓包）。 */}
      <div>
        <label className="flex items-center gap-1.5 text-sm font-medium">
          <MonitorSmartphone className="h-3.5 w-3.5 text-muted-foreground" />
          我的设备
        </label>
        <div className="mt-1.5">
          <DeviceStatusList />
        </div>
      </div>

      {/* 1. 选择平台（接入只解决"这台机器是谁、回连到哪"） */}
      <div className="space-y-3">
        <div>
          <label className="flex items-center gap-1.5 text-sm font-medium">
            <MonitorSmartphone className="h-3.5 w-3.5 text-muted-foreground" />
            目标操作系统（要接入哪台电脑）
          </label>
          <div className="mt-1.5 grid grid-cols-2 gap-2">
            {platforms.map((p) => (
              <button
                key={`${p.os}/${p.arch}`}
                type="button"
                disabled={!p.available}
                onClick={() => {
                  if (!p.available) return;
                  setOs(p.os);
                  // 切 OS 时同步到该 OS 下已选架构的最佳产物（无则取该 OS 首个可用架构）。
                  setArch(
                    platforms.find(
                      (x) => x.os === p.os && x.arch === arch,
                    )?.arch ?? p.arch,
                  );
                }}
                aria-pressed={os === p.os}
                className={`flex items-center gap-2 rounded-md border px-2.5 py-2 text-sm transition-colors ${
                  os === p.os
                    ? "border-primary/60 bg-primary/10 text-foreground"
                    : "border-border bg-background text-muted-foreground"
                } ${p.available ? "cursor-pointer hover:bg-muted/60" : "cursor-not-allowed opacity-50"}`}
              >
                {os === p.os && <Check className="h-4 w-4 text-primary" />}
                {os !== p.os && <span className="h-4 w-4 rounded-full border border-border" />}
                <span className="truncate">{p.label}</span>
              </button>
            ))}
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            只展示已预置的平台；缺失产物请先在服务端执行 make build-agents。
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            启动码只负责接入这台机器；抓包端口与解码插件在「开始抓包」时指定。
          </p>
        </div>
      </div>

      {/* 2. 生成启动码 */}
      <Button onClick={handleGenerate} disabled={busy || isLoading || available.length === 0} className="w-full">
        <KeyRound className="h-4 w-4" />
        {busy ? "生成中…" : "生成启动码"}
      </Button>

      {/* 3. 启动码 + 接入命令 */}
      {created && (
        <div className="space-y-3 rounded-md border border-border bg-muted/30 p-3">
          <div>
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">启动码（一次性，24 小时有效）</span>
              {created.expires_at && (
                <span className="ml-auto flex items-center gap-1 text-[11px] text-muted-foreground">
                  <Clock className="h-3 w-3" />
                  {formatExpiry(created.expires_at)}
                </span>
              )}
            </div>
            <div className="mt-2 flex items-center gap-2">
              <code className="flex-1 rounded-md border border-border bg-background px-3 py-2 font-mono text-lg tracking-widest">
                {created.code}
              </code>
              <Button variant="outline" size="sm" onClick={() => copyText("code", created.code)}>
                {copiedField === "code" ? (
                  <Check className="h-4 w-4" />
                ) : (
                  <Copy className="h-4 w-4" />
                )}
                复制
              </Button>
            </div>
          </div>

          <div>
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">在目标机执行以下命令</span>
              <span className="flex items-center gap-1 rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                <TerminalSquare className="h-3 w-3" />
                {isWindows ? "Windows PowerShell" : "Linux / macOS"}
              </span>
              <Button
                variant="outline"
                size="sm"
                className="ml-auto"
                onClick={() => copyText("cmd", activeCmd)}
                disabled={!activeCmd}
              >
                {copiedField === "cmd" ? (
                  <Check className="h-4 w-4" />
                ) : (
                  <Copy className="h-4 w-4" />
                )}
                复制命令
              </Button>
            </div>
            <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-background p-3 font-mono text-[11.5px] leading-relaxed text-foreground">
              {activeCmd || "先生成启动码…"}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}