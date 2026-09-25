// capture-channel-pickers.tsx — 两条抓包通道各自的目标选择器。
//
// 拆出来只为一个原因：探针卡片（三维度状态 + 网卡多选）和代理租约卡片（设备 + 就地建租约
// + 扫码面板）各自都有不少判定逻辑，塞进弹窗会让「通道选择」这件事本身被埋掉。
import { useState, type ReactNode } from "react";
import { Check, PauseCircle, Plus, PowerOff, Server, Settings2, Smartphone, Wifi } from "lucide-react";
import { SELECT_ACTIVE, SELECT_IDLE } from "@/components/capture-fields";
import { LeaseQrPanel } from "@/components/proxy-lease-qr";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { useCreateProxyLease, useReleaseProxyLease, useStopLeaseCapture } from "@/hooks/use-mcp";
import { cn } from "@/lib/utils";
import type { ProbeInfo } from "@/types/probe";
import type { ProxyLease } from "@/types/proxy";

/** 探针选择卡片的可抓包判定：在线且不在抓包中（idle/stopped/failed 可选）。 */
export function probeSelectable(p: ProbeInfo): boolean {
  if (p.connection_state !== "online") return false;
  return p.capture_state !== "starting" && p.capture_state !== "running";
}

/** 代理租约可开新抓包的判定：agent 常驻进程活着，且当前没有进行中的会话。 */
export function leaseSelectable(l: ProxyLease): boolean {
  return l.agent_running && !l.session_running;
}

/** 探针三维度状态 chip（connection + capture 合并展示）。 */
function ProbeStateChip({ p }: { p: ProbeInfo }) {
  if (p.connection_state !== "online") {
    return <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">离线</span>;
  }
  switch (p.capture_state) {
    case "running":
      return (
        <span className="inline-flex items-center gap-1 rounded bg-success/15 px-1.5 py-0.5 text-[10px] text-success">
          <span className="gt-live-dot" />
          抓包中
        </span>
      );
    case "starting":
      return (
        <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] text-amber-700 dark:text-amber-300">
          启动中
        </span>
      );
    case "failed":
      return <span className="rounded bg-destructive/15 px-1.5 py-0.5 text-[10px] text-destructive">失败</span>;
    default:
      return <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">空闲</span>;
  }
}

function DisabledHint({ children }: { children: ReactNode }) {
  return <span className="shrink-0 text-[11px] text-muted-foreground">{children}</span>;
}

/** 探针机器选择 + 网卡多选。
 *  网卡从「高级设置」提到了这一层：选完机器就顺手选卡，是「点探针就能看到网卡」
 *  这条诉求的字面意思，不该再多点一次展开。 */
export function ProbeChannelPicker({
  probes,
  probeId,
  onPick,
  ifaces,
  onIfaces,
}: {
  probes: ProbeInfo[];
  probeId: string;
  onPick: (probeId: string) => void;
  ifaces: string[];
  onIfaces: (ifaces: string[]) => void;
}) {
  const selected = probes.find((p) => p.probe_id === probeId) ?? null;
  const nics = selected?.interfaces ?? [];

  return (
    <div>
      <label className="text-sm font-medium">选择机器</label>
      {probes.length === 0 ? (
        <div className="mt-1.5 rounded-lg border border-dashed border-border bg-muted/40 px-3 py-4 text-center text-xs text-muted-foreground">
          还没有可用的探针机器。通过顶部「接入设备」下载探针，在目标机器上运行 gt-agent 完成接入。
        </div>
      ) : (
        <div className="mt-1.5 grid max-h-48 grid-cols-1 gap-1.5 overflow-auto gt-scroll">
          {probes.map((p) => {
            const selectable = probeSelectable(p);
            const active = probeId === p.probe_id;
            return (
              <button
                key={p.probe_id}
                type="button"
                aria-pressed={active}
                disabled={!selectable}
                onClick={() => onPick(p.probe_id)}
                title={selectable ? p.hostname : undefined}
                className={cn(
                  "flex items-center gap-2.5 rounded-md border px-2.5 py-2 text-left text-sm transition-all",
                  active ? SELECT_ACTIVE : SELECT_IDLE,
                  selectable ? "cursor-pointer" : "cursor-not-allowed opacity-50",
                )}
              >
                <Server className="h-4 w-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium text-foreground">{p.name}</span>
                  <span className="block truncate font-mono text-[10px]">
                    {p.hostname}
                    {p.connection_state === "online" && !p.capture_iface ? " · 待选网卡" : ""}
                  </span>
                </span>
                {active && <Check className="h-4 w-4 shrink-0 text-primary" aria-hidden />}
                {selectable ? <ProbeStateChip p={p} /> : <DisabledHint>{p.connection_state === "online" ? "抓包中" : "离线"}</DisabledHint>}
              </button>
            );
          })}
        </div>
      )}

      {selected && (
        <div className="mt-2.5">
          <label className="flex items-center gap-1.5 text-sm font-medium">
            <Wifi className="h-3.5 w-3.5 text-muted-foreground" />
            探针侧网卡
          </label>
          {nics.length === 0 ? (
            <p className="mt-1 text-xs text-muted-foreground">
              该探针未上报网卡清单（未装 Npcap 或版本过旧），将自动选择默认网卡。
            </p>
          ) : (
            <>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {nics.map((nic) => {
                  const active = ifaces.includes(nic.name);
                  return (
                    <button
                      key={nic.name}
                      type="button"
                      aria-pressed={active}
                      onClick={() =>
                        onIfaces(active ? ifaces.filter((x) => x !== nic.name) : [...ifaces, nic.name])
                      }
                      title={nic.ips?.length ? `${nic.name} · ${nic.ips.join(", ")}` : nic.name}
                      className={cn(
                        "inline-flex items-center rounded-md border px-2 py-0.5 font-mono text-[11px] transition-all",
                        active
                          ? "border-primary/70 bg-primary/10 text-primary ring-1 ring-primary/40"
                          : "border-border bg-muted text-muted-foreground hover:border-primary/30 hover:text-foreground",
                      )}
                    >
                      {active && <Check className="mr-0.5 inline h-3 w-3" />}
                      {nic.friendly || nic.name}
                    </button>
                  );
                })}
              </div>
              <p className="mt-1 text-xs text-muted-foreground">
                {ifaces.length === 0
                  ? "不选即由探针自动挑默认网卡；可点选一个或多个网卡同时抓。"
                  : `已选 ${ifaces.length} 张网卡，多选时各卡并发抓、汇入同一会话。`}
              </p>
            </>
          )}
        </div>
      )}
    </div>
  );
}

function LeaseStateChip({ l }: { l: ProxyLease }) {
  if (!l.agent_running) {
    return <span className="rounded bg-destructive/15 px-1.5 py-0.5 text-[10px] text-destructive">代理已停</span>;
  }
  if (l.session_running) {
    return (
      <span className="inline-flex items-center gap-1 rounded bg-success/15 px-1.5 py-0.5 text-[10px] text-success">
        <span className="gt-live-dot" />
        抓包中
      </span>
    );
  }
  return <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">空闲</span>;
}

/** 手机代理租约选择 + 就地创建 / 关停。
 *  租约 = 手机扫过码、常驻的代理端口；一次抓包只是在它上面开一个新会话。
 *  租约的生老病死（建、拿二维码、停抓包、释放回收端口）都在这一层完成，不再让用户
 *  多点一层：此刻的诉求就是"给我一个能扫的租约，用完收掉它"，搬到「代理服务器配置」
 *  等于把主流程放到别处。租约的长期属性（换插件、改筛选）仍归「租约管理」。
 *  创建表单只收设备标签 —— 解析器与主机/端口筛选上方 FilterFields/ParserPicker 已经有了，
 *  那里填的东西会作为本次会话的覆盖参数下发，不必在租约上再问一遍。 */
export function ProxyChannelPicker({
  leases,
  leaseId,
  onPick,
  onManage,
  projectId,
}: {
  leases: ProxyLease[];
  leaseId: string;
  onPick: (leaseId: string) => void;
  /** 打开「代理服务器配置」改租约的长期属性（解码插件、连接筛选）。 */
  onManage?: () => void;
  /** 新建租约归属的项目（空=不归属），避免建完再补一次归位。 */
  projectId?: string;
}) {
  const [creating, setCreating] = useState(false);
  const [device, setDevice] = useState("");
  // 租约列表 4s 轮询一次，刚建的不在里面；用它兜住，否则创建后二维码要空几秒。
  const [justCreated, setJustCreated] = useState<ProxyLease | null>(null);
  const createLease = useCreateProxyLease();

  const selected =
    leases.find((l) => l.lease_id === leaseId) ??
    (justCreated?.lease_id === leaseId ? justCreated : null);
  // 一个租约都没有时，创建表单就是这一节的主体（而不是"空态 + 要点某处才能建"）。
  const showForm = creating || leases.length === 0;

  function handleCreate() {
    createLease.mutate(
      {
        device: device.trim() || undefined,
        projectId: projectId || undefined,
        // 只建租约不开抓包：否则一建成就 session_running，反倒不能在这里被选中，
        // 用户还得先停一次才能开始抓包。
        noAutoStart: true,
      },
      {
        onSuccess: (res) => {
          const lease = res?.lease;
          if (!lease?.lease_id) return;
          setJustCreated(lease);
          setDevice("");
          setCreating(false);
          onPick(lease.lease_id);
          toast.success("代理租约已创建", "手机扫码接入后点「启动抓包」");
        },
        onError: (err) => toast.error("创建租约失败", err.message),
      },
    );
  }

  return (
    <div>
      <div className="flex items-center justify-between gap-2">
        <label className="text-sm font-medium">代理租约</label>
        <div className="flex items-center gap-3">
          {leases.length > 0 && (
            <button
              type="button"
              onClick={() => setCreating((c) => !c)}
              className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
            >
              <Plus className="h-3.5 w-3.5" />
              新建租约
            </button>
          )}
          {onManage && (
            <button
              type="button"
              onClick={onManage}
              className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:underline"
            >
              <Settings2 className="h-3.5 w-3.5" />
              租约管理
            </button>
          )}
        </div>
      </div>

      {leases.length > 0 && (
        <div className="mt-1.5 grid max-h-48 grid-cols-1 gap-1.5 overflow-auto gt-scroll">
          {leases.map((l) => {
            const active = leaseId === l.lease_id;
            return (
              <button
                key={l.lease_id}
                type="button"
                aria-pressed={active}
                onClick={() => onPick(l.lease_id)}
                title={l.connect_addr}
                className={cn(
                  "flex cursor-pointer items-center gap-2.5 rounded-md border px-2.5 py-2 text-left text-sm transition-all",
                  active ? SELECT_ACTIVE : SELECT_IDLE,
                )}
              >
                <Smartphone className="h-4 w-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium text-foreground">
                    {l.device || "未命名设备"}
                  </span>
                  <span className="block truncate font-mono text-[10px]">{l.connect_addr || "未拿到局域网地址"}</span>
                </span>
                {active && <Check className="h-4 w-4 shrink-0 text-primary" aria-hidden />}
                <LeaseStateChip l={l} />
              </button>
            );
          })}
        </div>
      )}

      {showForm && (
        <div className="mt-1.5 rounded-lg border border-dashed border-border bg-muted/40 p-2.5">
          <div className="flex items-end gap-2">
            <div className="min-w-0 flex-1">
              <label htmlFor="new-lease-device" className="text-xs font-medium">
                设备标签
              </label>
              <Input
                id="new-lease-device"
                value={device}
                onChange={(e) => setDevice(e.target.value)}
                placeholder="如 alice-phone（可选，便于识别）"
                className="mt-1 h-8 bg-background text-xs"
              />
            </div>
            <Button
              size="sm"
              variant="outline"
              className="h-8 shrink-0 gap-1 px-2 text-xs"
              onClick={handleCreate}
              disabled={createLease.isPending}
            >
              {createLease.isPending ? "创建中…" : (
                <>
                  <Plus className="h-3.5 w-3.5" />
                  创建租约
                </>
              )}
            </Button>
          </div>
          <p className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
            租约只是一个常驻的手机代理端口，创建后二维码长期有效；本次抓包用哪套筛选和解析器，
            以下面的表单为准。
            {createLease.isError && (
              <span className="text-destructive"> 创建失败：{createLease.error?.message}</span>
            )}
          </p>
        </div>
      )}

      <div className="mt-2">
        {selected ? (
          <>
            <LeaseQrPanel lease={selected} />
            <LeaseActions lease={selected} onReleased={() => onPick("")} />
          </>
        ) : (
          <p className="rounded-lg border border-border bg-muted/40 px-3 py-2.5 text-xs text-muted-foreground">
            {leases.length === 0 && showForm
              ? "创建租约后，手机要扫的二维码会出现在这里。"
              : "选中上面的租约，这里就显示手机要扫的二维码。"}
          </p>
        )}
      </div>
    </div>
  );
}

/** 选中租约的就地关停。
 *  「停止抓包」只结束本轮会话（端口/二维码留着）；「释放租约」才杀 agent、回收端口。
 *  两者都必须在这一层能点到：抓包中的租约在这里是灰掉的话，用户就被卡在"想停要另开一层、
 *  想关要另开一层"，而这一层正是他决定用不用这个租约的地方。
 *  释放不可逆（再建会拿到新端口、手机要重新扫码），所以二次确认做在按钮自己身上。 */
function LeaseActions({ lease, onReleased }: { lease: ProxyLease; onReleased: () => void }) {
  const stopCapture = useStopLeaseCapture();
  const releaseLease = useReleaseProxyLease();
  const [confirmRelease, setConfirmRelease] = useState(false);
  const capturing = !!(lease.capture_running ?? lease.session_running);

  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
      {capturing && (
        <Button
          variant="outline"
          size="sm"
          className="h-7 shrink-0 gap-1 px-2 text-xs"
          disabled={stopCapture.isPending}
          title="停掉本轮抓包会话；代理端口和二维码不变，随时可再开一轮"
          onClick={() =>
            stopCapture.mutate({ leaseId: lease.lease_id }, {
              onSuccess: (res) =>
                toast.success(
                  "已停止抓包（租约保留）",
                  res ? `${res.raw_packets ?? 0} 包 · ${res.events ?? 0} 事件` : "",
                ),
              onError: (err) => toast.error("停止抓包失败", err.message),
            })
          }
        >
          <PauseCircle className="h-3.5 w-3.5" />
          {stopCapture.isPending ? "停止中…" : "停止抓包"}
        </Button>
      )}
      <Button
        variant="outline"
        size="sm"
        className={cn(
          "h-7 shrink-0 gap-1 px-2 text-xs text-destructive",
          confirmRelease ? "border-destructive/60 bg-destructive/10" : "hover:bg-destructive/10 hover:text-destructive",
        )}
        disabled={releaseLease.isPending}
        title="释放租约：杀 agent、回收端口、二维码失效（之后再建会是新端口，手机要重新扫码）"
        onClick={() => {
          if (!confirmRelease) {
            setConfirmRelease(true);
            return;
          }
          releaseLease.mutate(lease.lease_id, {
            onSuccess: () => {
              setConfirmRelease(false);
              onReleased();
              toast.success("租约已释放", "agent 已停、端口已回收，二维码失效");
            },
            onError: (err) => toast.error("释放租约失败", err.message),
          });
        }}
      >
        <PowerOff className="h-3.5 w-3.5" />
        {releaseLease.isPending ? "释放中…" : confirmRelease ? "确认释放（二维码失效）" : "释放租约"}
      </Button>
      {/* 释放按钮永远排在这行末尾，所以提示语跟在它后面；窄弹窗里让它折到下一行，
          而不是截断——这句话正是"该停抓包还是该释放租约"的区分说明。 */}
      <span className="min-w-[140px] flex-1 leading-snug text-[11px] text-muted-foreground">
        {capturing
          ? "该租约正在抓包，先停止才能开新一轮。"
          : lease.agent_running
            ? "租约会一直常驻，不用时再点「释放租约」回收端口。"
            : "agent 已停：这个租约已经不能用了，释放它回收端口即可。"}
      </span>
    </div>
  );
}
