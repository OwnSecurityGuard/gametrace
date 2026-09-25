// StartCaptureDialog — 开始抓包的通道弹窗。
//
// 弹窗回答四个问题，顺序就是用户做决定的顺序：
//   ① 从哪儿抓（探针机器 / 手机代理租约）② 抓什么（网卡、端口、服务端地址、解析器）
//   ③ 归到哪个项目（可选，不归属也能抓）④ 以上几条的回读摘要。
// 手机代理这一侧可以在这里直接建租约、拿二维码、停抓包、释放回收端口：抓包时才发现"还没有
// 租约"、或者想收掉一个正在跑的租约，却被弹去另一层表单，等于把主流程搬到了别处。
// 「代理服务器配置」只留租约的长期属性（换插件、改筛选）与历史会话入口。
import { useEffect, useMemo, useRef, useState } from "react";
import { Check, Loader2, Play, ShieldQuestion, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import {
  AttributionPicker,
  CapturePreview,
  ChannelSwitch,
  FilterFields,
  ParserPicker,
  type CaptureChannel,
} from "@/components/capture-fields";
import {
  ProbeChannelPicker,
  ProxyChannelPicker,
  leaseSelectable,
  probeSelectable,
} from "@/components/capture-channel-pickers";
import {
  useListProbes,
  useMoveSessionToProject,
  useProbeStartCapture,
  useProjects,
  useProxyLeases,
  useSessionStatus,
  useStartLeaseCapture,
} from "@/hooks/use-mcp";
import { toast } from "@/components/ui/toast";
import type { ProjectInfo } from "@/types/project";

interface StartCaptureDialogProps {
  open: boolean;
  onClose: () => void;
  /** 用户看完启动结果、点「进入会话分析」时回调。 */
  onStarted?: (sessionId: string) => void;
  /** 从项目预填的默认端口（0=不预填） */
  initialPort?: number;
  /** 从项目预填的默认插件名 */
  initialPlugin?: string;
  /** 从项目一键抓包时绑定的项目 id；抓包会话归属到该项目 */
  initialProjectId?: string;
  /** 从「下载探针」接入后带入的探针 id，打开时自动预选它 */
  initialProbeId?: string;
  /** 切到「代理服务器配置」去改租约插件/筛选或释放（弹窗互斥，换场由父级负责）。 */
  onOpenProxyConfig?: () => void;
}

/** 一次抓包的下发结果：会话已建，剩下的问题只是数据到没到。 */
interface Started {
  sessionId: string;
  channel: CaptureChannel;
  target: string;
}

/** 开始抓包对话框：选通道 → 填条件 → 下发 → 等数据到达。 */
export function StartCaptureDialog({
  open,
  onClose,
  onStarted,
  initialPort,
  initialPlugin,
  initialProjectId,
  initialProbeId,
  onOpenProxyConfig,
}: StartCaptureDialogProps) {
  const [channel, setChannel] = useState<CaptureChannel>("probe");
  // 探针网卡选卡：空数组 = 由探针按出口 IP 挑默认网卡；可多选并发抓。
  const [probeId, setProbeId] = useState("");
  const [probeIfaces, setProbeIfaces] = useState<string[]>([]);
  // 代理：租约 id。一次抓包只是在它上面开一个新会话。
  const [leaseId, setLeaseId] = useState("");
  const [port, setPort] = useState("8080");
  // 端口过滤协议：tcp/udp/both（默认 tcp）。仅探针抓包生效（探针侧派生 BPF）。
  const [protocol, setProtocol] = useState<"tcp" | "udp" | "both">("tcp");
  // 服务端地址筛选（非必填）：IP 或域名，多个用逗号/空格/换行分隔。
  const [hostFilter, setHostFilter] = useState("");
  const [plugin, setPlugin] = useState("");
  // 本次抓包会话的归属项目（空 = 不归属，会话进「未归属抓包」）。
  const [projectId, setProjectId] = useState("");
  const [started, setStarted] = useState<Started | null>(null);

  const { data: probesData } = useListProbes();
  const probes = probesData?.probes ?? [];
  const { data: leasesData } = useProxyLeases();
  const leases = leasesData?.leases ?? [];
  const { data: projectsData } = useProjects();
  const projects = projectsData?.projects ?? [];

  const probeStart = useProbeStartCapture();
  const leaseStart = useStartLeaseCapture();
  const moveSession = useMoveSessionToProject();
  const pending = probeStart.isPending || leaseStart.isPending;

  const selectedProbe = probes.find((x) => x.probe_id === probeId) ?? null;
  const selectedLease = leases.find((x) => x.lease_id === leaseId) ?? null;
  const counts: Record<CaptureChannel, number> = {
    probe: probes.filter(probeSelectable).length,
    proxy: leases.filter(leaseSelectable).length,
  };
  const targetName =
    channel === "probe"
      ? selectedProbe?.name ?? "未选机器"
      : selectedLease?.device || selectedLease?.connect_addr || "未选设备";

  // 等待数据到达期间 2s 轮询会话实时状态：packets_in/raw_count > 0 即数据已进来。
  const { data: liveStatus } = useSessionStatus(started?.sessionId ?? null, 2000);
  const packetsIn = (liveStatus?.packets_in ?? 0) + (liveStatus?.raw_count ?? 0);
  const hasData = started != null && packetsIn > 0;
  // 实时态非 running 即已终结（词汇与后端一致：running | stopped | error），
  // 此时再等也不会有数据进来，得让用户重来。
  const sessionClosed =
    started != null && liveStatus?.state != null && liveStatus.state !== "running";

  // 数据到达提示只弹一次（等待 → 已到达 的边沿）。
  const notifiedRef = useRef(false);
  useEffect(() => {
    if (hasData && !notifiedRef.current) {
      notifiedRef.current = true;
      toast.success("数据已到达", "正在推流，可以开始分析");
    }
    if (!started) notifiedRef.current = false;
  }, [hasData, started]);

  useEffect(() => {
    if (!open) return;
    setStarted(null);
    // 从「下载探针」接入闭环带入探针 id：自动预选，免手动找。
    setProbeId(initialProbeId ?? "");
    setProbeIfaces([]);
    setLeaseId("");
    setHostFilter("");
    setProtocol("tcp");
    // 打开时应用项目预填：有初始端口/插件才覆盖默认值，否则回到默认。
    setPort(initialPort && initialPort > 0 ? String(initialPort) : "8080");
    setPlugin(initialPlugin ?? "");
    setProjectId(initialProjectId ?? "");
    // 默认通道跟着「哪条通道现在真能抓」走：只接过手机、没有探针的人，打开弹窗
    // 第一眼就该看到自己能用的那条路，而不是一个空机器列表。
    if (initialProbeId) setChannel("probe");
    else if (counts.proxy > 0 && counts.probe === 0) setChannel("proxy");
    else setChannel("probe");
    // 只在每次打开时读一次预填（initial* 当次快照）。probes/leases 只用于默认通道判定，
    // 放进依赖会让 4~5s 一次的轮询反过来重置用户正在填的表单。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const hostList = useMemo(
    () =>
      hostFilter
        .split(/[\s,，]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    [hostFilter],
  );
  const parsedPort = parseInt(port, 10);
  const ports = parsedPort > 0 ? [parsedPort] : [];

  /** 本次会话最终归属：手机通道没选时沿用租约自己的归属。 */
  const effectiveProjectId = projectId || (channel === "proxy" ? selectedLease?.project_id || "" : "");
  const projectName = projects.find((p) => p.id === effectiveProjectId)?.name ?? "";

  // 选中项目即带上它的默认端口/解析器：这正是「建项目存默认值」的意义。
  function handleAttribution(nextId: string, project: ProjectInfo | null) {
    setProjectId(nextId);
    if (!project) return;
    if (project.default_port && project.default_port > 0) setPort(String(project.default_port));
    if (project.default_plugin) setPlugin(project.default_plugin);
  }

  function fail(err: Error) {
    toast.error("下发失败", err.message);
  }

  function handleStart() {
    if (channel === "probe") {
      if (!selectedProbe) {
        toast.error("请选择一台探针机器");
        return;
      }
      // 探针抓包：建会话 + AssignCapture 一体（probe_start_capture）。
      probeStart.mutate(
        {
          probeId,
          ports,
          hosts: hostList,
          ifaces: probeIfaces,
          plugin: plugin || undefined,
          projectId: projectId || undefined,
          protocol,
        },
        {
          onSuccess: (data) => {
            const sessionId = data?.session_id ?? "";
            if (!sessionId) {
              fail(new Error("探针未返回会话 id"));
              return;
            }
            // 进入「等待推流」闭环：探针收到指令开网卡 → 推流到达。
            setStarted({ sessionId, channel: "probe", target: targetName });
            toast.success("抓包已下发", `${targetName} · 会话 ${sessionId}`);
          },
          onError: fail,
        },
      );
      return;
    }

    if (!selectedLease) {
      toast.error("请选择一个代理租约");
      return;
    }
    leaseStart.mutate(
      {
        leaseId,
        plugin: plugin || undefined,
        includeHosts: hostList,
        includePorts: ports,
      },
      {
        onSuccess: (data) => {
          const sessionId = data?.session_id ?? "";
          if (!sessionId) {
            fail(new Error("代理租约未返回会话 id"));
            return;
          }
          setStarted({ sessionId, channel: "proxy", target: targetName });
          toast.success("抓包会话已开启", `${targetName} · 会话 ${sessionId}`);
          // 租约的归属项目在创建时就定死了，而 start_lease_capture 没有 project_id 参数：
          // 这里选的归属只能在会话建好后补一次归位，否则手机通道的归属选择是空话。
          if (projectId && projectId !== selectedLease.project_id) {
            moveSession.mutate(
              { session_id: sessionId, project_id: projectId },
              { onError: (err) => toast.error("归位到项目失败", err.message) },
            );
          }
        },
        onError: fail,
      },
    );
  }

  // 统一关闭路径：清掉等待状态再回调（重新打开时 useEffect 亦会兜底重置）。
  function handleClose() {
    setStarted(null);
    onClose();
  }

  const ready =
    channel === "probe"
      ? !!selectedProbe && probeSelectable(selectedProbe)
      : !!selectedLease && leaseSelectable(selectedLease);

  return (
    <Dialog
      open={open}
      onClose={handleClose}
      icon={<Play className="h-5 w-5" />}
      title="开始抓包"
      description="先选从哪儿抓：服务器 / PC 上运行的探针，或手机扫码接入的代理租约。"
      // 手机代理这一侧要就地放二维码 + 接入说明，max-w-md 会把二维码面板挤成竖排。
      className="max-w-lg"
      footer={
        started ? (
          <Button onClick={() => onStarted?.(started.sessionId)}>
            <Check className="h-4 w-4" />
            进入会话分析
          </Button>
        ) : (
          <>
            <Button variant="outline" onClick={handleClose}>
              <X className="h-4 w-4" />
              取消
            </Button>
            <Button onClick={handleStart} disabled={pending || !ready}>
              {pending ? "启动中…" : "启动抓包"}
            </Button>
          </>
        )
      }
    >
      {started ? (
        <div className="space-y-3">
          {sessionClosed ? (
            <div className="flex items-center gap-2 rounded-lg border border-border bg-muted/50 px-3 py-2.5 text-sm text-muted-foreground">
              <ShieldQuestion className="h-4 w-4 shrink-0" />
              会话已结束（可能在探针或代理侧被停止）。
            </div>
          ) : hasData ? (
            <div className="flex items-center gap-2 rounded-lg border border-success/40 bg-success/10 px-3 py-2.5 text-sm text-success">
              <Check className="h-4 w-4 shrink-0" />
              {started.channel === "probe" ? "探针正在推流" : "手机侧流量已进入"}
              <span className="ml-auto font-mono text-xs">
                {packetsIn.toLocaleString()} packets ·{" "}
                {(liveStatus?.event_count ?? 0).toLocaleString()} events
              </span>
            </div>
          ) : (
            <div className="rounded-lg border border-border bg-muted/50 px-3 py-2.5">
              <div className="flex items-center gap-2 text-sm">
                <Loader2 className="h-4 w-4 shrink-0 animate-spin text-primary" />
                {started.channel === "probe"
                  ? "指令已下发，等待探针开始推流…"
                  : "会话已就绪，等待手机侧发起请求…"}
              </div>
              {/* 提示另起一行： inline 挤在右侧时，弹窗半宽下会把主句和提示都压成折行。 */}
              <p className="mt-1 pl-6 text-xs text-muted-foreground">
                {started.channel === "probe"
                  ? "探针在线时会自动对齐并开抓"
                  : "手机没连上代理时不会有数据"}
              </p>
            </div>
          )}
          <CapturePreview
            rows={[
              { label: "会话", value: started.sessionId },
              { label: "目标", value: started.target },
              { label: "归属", value: projectName || "不归属项目" },
            ]}
          />
        </div>
      ) : (
        <div className="space-y-3">
          <div>
            <label className="text-sm font-medium">抓包方式</label>
            <div className="mt-1.5">
              <ChannelSwitch value={channel} onChange={setChannel} counts={counts} />
            </div>
          </div>

          {channel === "probe" ? (
            <ProbeChannelPicker
              probes={probes}
              probeId={probeId}
              onPick={(id) => {
                setProbeId(id);
                setProbeIfaces([]);
              }}
              ifaces={probeIfaces}
              onIfaces={setProbeIfaces}
            />
          ) : (
            <ProxyChannelPicker
              leases={leases}
              leaseId={leaseId}
              onPick={setLeaseId}
              projectId={projectId}
              onManage={() => {
                handleClose();
                onOpenProxyConfig?.();
              }}
            />
          )}

          <FilterFields
            port={port}
            onPort={setPort}
            protocol={protocol}
            onProtocol={setProtocol}
            protocolVisible={channel === "probe"}
            hosts={hostFilter}
            onHosts={setHostFilter}
          />

          <ParserPicker value={plugin} onChange={setPlugin} />

          <AttributionPicker value={projectId} onChange={handleAttribution} />

          <CapturePreview
            rows={[
              { label: "方式", value: channel === "probe" ? "探针抓包" : "手机代理" },
              { label: "目标", value: targetName },
              ...(channel === "probe"
                ? [
                    {
                      label: "网卡",
                      value: probeIfaces.length
                        ? probeIfaces.join("、")
                        : selectedProbe?.capture_iface || "自动选择",
                    },
                  ]
                : []),
              {
                label: "端口",
                value: ports.length
                  ? `${ports[0]}${channel === "probe" ? `/${protocol === "both" ? "tcp+udp" : protocol}` : ""}`
                  : "全部",
              },
              { label: "服务端", value: hostList.length ? hostList.join("、") : "不限" },
              { label: "解析器", value: plugin || "仅抓包" },
              { label: "归属", value: projectName || "不归属项目" },
            ]}
          />
        </div>
      )}
    </Dialog>
  );
}
