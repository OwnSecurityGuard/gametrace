// capture-fields.tsx — 「开始抓包」里两条通道共用的字段。
//
// 探针抓包与手机代理抓包在「抓什么」上完全同构（端口 / 服务端地址 / 解析器 / 归属项目），
// 差别只在「从哪儿抓」。所以这些字段在这里一次实现，通道各自只渲染自己的目标选择器 ——
// 否则同一个字段会在两套通道 UI 里各写一遍，改一处漏一处。
import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown, FolderInput, MonitorSmartphone, Smartphone, Wifi } from "lucide-react";
import { Input } from "@/components/ui/input";
import { useProjects, useRegisteredPlugins } from "@/hooks/use-mcp";
import { groupParsers, GROUP_LABEL } from "@/lib/parsers";
import { cn } from "@/lib/utils";
import type { ProjectInfo } from "@/types/project";

/** 抓包通道：probe = 在目标机器的网卡上抓；proxy = 手机经 sing-box 代理租约推流。 */
export type CaptureChannel = "probe" | "proxy";

/** 可选项「已选中」的统一醒目样式：主色边框 + 浅底 + 外圈 ring + 轻投影，配合 Check 角标。 */
export const SELECT_ACTIVE =
  "border-primary/70 bg-primary/10 text-foreground ring-1 ring-primary/40 shadow-sm";
/** 未选中态：弱边框，hover 时给一点主色过渡，提示可点。 */
export const SELECT_IDLE =
  "border-border bg-background text-muted-foreground hover:border-primary/30 hover:bg-muted/60 hover:text-foreground";

/** 卡片半宽只放得下一行小字，所以副行只报「有没有目标」——
 *  两条通道各是什么已经在标题和弹窗说明里说清，不必在这里重复。
 *  零目标时的提示按通道分开：探针要去别处下载接入，手机代理在弹窗里就能就地建租约，
 *  写成同一句「暂无可用目标」会让人以为这条通道也走不通。 */
const CHANNEL_META: Record<
  CaptureChannel,
  { label: string; unit: string; empty: string; icon: typeof Wifi }
> = {
  probe: { label: "探针抓包", unit: "台可用", empty: "暂无可用目标", icon: Wifi },
  proxy: { label: "手机代理", unit: "个租约", empty: "可就地创建租约", icon: Smartphone },
};

/** 通道选择：抓包方式是一切的前提，放在弹窗第一屏最上面。 */
export function ChannelSwitch({
  value,
  onChange,
  counts,
}: {
  value: CaptureChannel;
  onChange: (channel: CaptureChannel) => void;
  /** 各通道当前可选的目标数；0 时卡片上按通道如实写明下一步，而不是禁掉让人猜。 */
  counts: Record<CaptureChannel, number>;
}) {
  return (
    <div className="grid grid-cols-2 gap-1.5">
      {(Object.keys(CHANNEL_META) as CaptureChannel[]).map((id) => {
        const meta = CHANNEL_META[id];
        const active = value === id;
        return (
          <button
            key={id}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(id)}
            className={`flex items-start gap-2.5 rounded-md border px-2.5 py-2 text-left transition-all ${
              active ? SELECT_ACTIVE : SELECT_IDLE
            }`}
          >
            <meta.icon className={cn("mt-0.5 h-4 w-4 shrink-0", active ? "text-primary" : "")} />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-sm font-medium text-foreground">{meta.label}</span>
              <span className="block truncate text-[11px] text-muted-foreground">
                {counts[id] > 0 ? `${counts[id]} ${meta.unit}` : meta.empty}
              </span>
            </span>
            {active && <Check className="mt-0.5 h-4 w-4 shrink-0 text-primary" aria-hidden />}
          </button>
        );
      })}
    </div>
  );
}

/** 端口 / 端口协议 / 服务端地址：两条通道都吃这三个条件，只是探针侧派生 BPF、
 *  代理侧是连接级过滤，所以协议（TCP/UDP）只对探针有意义。 */
export function FilterFields({
  port,
  onPort,
  protocol,
  onProtocol,
  hosts,
  onHosts,
  protocolVisible,
}: {
  port: string;
  onPort: (value: string) => void;
  protocol: "tcp" | "udp" | "both";
  onProtocol: (value: "tcp" | "udp" | "both") => void;
  hosts: string;
  onHosts: (value: string) => void;
  protocolVisible: boolean;
}) {
  return (
    <>
      {/* 端口协议要放得下「TCP+UDP」三段，所以给它更宽的列（等分时第三段会被截成省略号）。 */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)]">
        <div>
          <label className="text-sm font-medium" htmlFor="capture-port">
            端口
          </label>
          <Input
            id="capture-port"
            value={port}
            onChange={(e) => onPort(e.target.value)}
            inputMode="numeric"
            placeholder="可留空（全抓）"
            className="mt-1.5 font-mono"
          />
        </div>
        <div>
          <label className="text-sm font-medium">端口协议</label>
          {protocolVisible ? (
            <div className="mt-1.5 flex items-center gap-1 rounded-lg bg-muted p-1">
              {(
                [
                  { id: "tcp", label: "TCP" },
                  { id: "udp", label: "UDP" },
                  { id: "both", label: "TCP+UDP" },
                ] as const
              ).map((opt) => (
                <button
                  key={opt.id}
                  type="button"
                  role="radio"
                  aria-checked={protocol === opt.id}
                  onClick={() => onProtocol(opt.id)}
                  className={
                    "min-w-0 flex-1 truncate rounded-md px-2 py-1.5 text-xs font-medium transition-[background-color,color] " +
                    (protocol === opt.id
                      ? "bg-card text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground")
                  }
                >
                  {opt.label}
                </button>
              ))}
            </div>
          ) : (
            <p className="mt-1.5 rounded-md border border-border bg-muted/40 px-2.5 py-1.5 text-xs text-muted-foreground">
              代理通道按连接的目标端口过滤，不区分 TCP/UDP。
            </p>
          )}
        </div>
      </div>
      {protocolVisible && (
        <p className="text-xs text-muted-foreground">
          端口非空时按所选协议在探针侧过滤；UDP/TCP+UDP 需要探针 Npcap 支持。
        </p>
      )}
      <div>
        <label htmlFor="capture-host-filter" className="text-sm font-medium">
          服务端 IP/域名（可选）
        </label>
        <Input
          id="capture-host-filter"
          value={hosts}
          onChange={(e) => onHosts(e.target.value)}
          placeholder="如 10.0.0.8 或 api.example.com；多个用逗号或空格分隔"
          className="mt-1.5 font-mono"
        />
        <p className="mt-1 text-xs text-muted-foreground">
          按连接的服务端地址筛选抓包；与端口同时填写时取交集（只抓该服务的对应端口）。
          {protocolVisible ? "域名需探针侧可解析，解析失败会导致抓包启动失败。" : ""}
        </p>
      </div>
    </>
  );
}

/** 解析器选择：按协议归组的卡片（Godot/Unity/HTTP/自定义），离线插件置灰但保留可见。 */
export function ParserPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { data: pluginsData } = useRegisteredPlugins();
  const groups = groupParsers(pluginsData?.plugins ?? []);
  return (
    <div>
      <label className="text-sm font-medium">解码解析器（可选）</label>
      {groups.order.length === 0 ? (
        <p className="mt-1.5 text-xs text-muted-foreground">
          当前没有已注册的解析器，可留空仅抓包；或先启动解析器插件使其注册到 Pipeline。
        </p>
      ) : (
        <>
          <div className="mt-1.5 grid grid-cols-2 gap-1.5">
            {groups.order.map((g) =>
              groups.byGroup[g].map((opt) => (
                <button
                  key={opt.plugin}
                  type="button"
                  aria-pressed={value === opt.plugin}
                  disabled={!opt.online}
                  onClick={() => onChange(value === opt.plugin ? "" : opt.plugin)}
                  className={`flex w-full items-center gap-2.5 rounded-md border px-2.5 py-2 text-sm transition-all ${
                    value === opt.plugin ? SELECT_ACTIVE : SELECT_IDLE
                  } ${opt.online ? "cursor-pointer" : "cursor-not-allowed opacity-50"}`}
                >
                  <span className="flex min-w-0 flex-1 items-center gap-2">
                    <span className="rounded bg-muted px-1 py-0.5 font-mono text-[10px] uppercase">
                      {GROUP_LABEL[g] ?? g}
                    </span>
                    <span className="truncate">{opt.label}</span>
                  </span>
                  {value === opt.plugin && <Check className="h-4 w-4 shrink-0 text-primary" aria-hidden />}
                </button>
              )),
            )}
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            {value ? "已选择；不选即只抓原始包不解码。" : "点击选择一个解析器；离线解析器置灰不可选。"}
          </p>
        </>
      )}
    </div>
  );
}

/** 归属项目：抓包会话归到哪个项目 —— 过去只有入口预填、弹窗里看不见也改不了，
 *  「无论有没有归属都要能选」正是这个控件存在的原因。 */
export function AttributionPicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (projectId: string, project: ProjectInfo | null) => void;
}) {
  const { data } = useProjects();
  const projects = data?.projects ?? [];
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: PointerEvent) {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") setOpen(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const selected = projects.find((p) => p.id === value) ?? null;
  const pick = (project: ProjectInfo | null) => {
    onChange(project?.id ?? "", project);
    setOpen(false);
  };

  return (
    <div ref={rootRef} className="relative">
      <label className="text-sm font-medium">归属项目</label>
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={`mt-1.5 flex w-full items-center gap-2 rounded-md border px-2.5 py-2 text-left text-sm transition-all ${
          selected ? SELECT_ACTIVE : SELECT_IDLE
        }`}
      >
        <FolderInput className="h-4 w-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate">
          {selected ? selected.name : "不归属任何项目"}
        </span>
        <ChevronDown
          className={`h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform ${open ? "rotate-180" : ""}`}
        />
      </button>
      {open && (
        <div
          role="listbox"
          // 归属项目是表单的最后一项：向下弹会被弹窗的滚动容器底边裁掉，只能往上开。
          className="absolute inset-x-0 bottom-[calc(100%+4px)] z-30 max-h-56 overflow-auto rounded-lg border border-border bg-popover p-1 shadow-lg gt-pop-in gt-scroll"
        >
          <button
            type="button"
            role="option"
            aria-selected={!value}
            onClick={() => pick(null)}
            className="flex w-full items-center gap-2 rounded px-1.5 py-1.5 text-left text-sm hover:bg-muted"
          >
            <MonitorSmartphone className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate">不归属任何项目</span>
            {!value && <Check className="h-3.5 w-3.5 shrink-0 text-primary" />}
          </button>
          {projects.map((p) => (
            <button
              key={p.id}
              type="button"
              role="option"
              aria-selected={p.id === value}
              onClick={() => pick(p)}
              className="flex w-full items-center gap-2 rounded px-1.5 py-1.5 text-left text-sm hover:bg-muted"
            >
              <FolderInput className="h-3.5 w-3.5 text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate">{p.name}</span>
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                {p.default_port ? `:${p.default_port}` : ""}
                {p.default_plugin ? ` ${p.default_plugin}` : ""}
              </span>
              {p.id === value && <Check className="h-3.5 w-3.5 shrink-0 text-primary" />}
            </button>
          ))}
          {projects.length === 0 && (
            <p className="px-1.5 py-2 text-[11px] text-muted-foreground">
              还没有项目。可以先不归属，之后在会话概览里归位。
            </p>
          )}
        </div>
      )}
      <p className="mt-1 text-xs text-muted-foreground">
        {selected
          ? "选项目会带上它的默认端口与解析器（仍可再改）。"
          : "不归属的会话在左栏「未归属抓包」里能找到。"}
      </p>
    </div>
  );
}

/** 派生摘要：把人填的条件回读成一句「接下来会发生什么」。
 *  抓包是重动作（起探针网卡、占端口），确认一次比事后排查便宜。 */
export function CapturePreview({ rows }: { rows: Array<{ label: string; value: string }> }) {
  return (
    <div className="rounded-lg border border-border bg-muted/40 px-3 py-2.5">
      <div className="grid grid-cols-1 gap-x-5 gap-y-1 sm:grid-cols-2">
        {rows.map((r) => (
          <div key={r.label} className="flex items-baseline gap-1.5 text-xs">
            <span className="shrink-0 text-muted-foreground">{r.label}</span>
            <span className="min-w-0 flex-1 truncate font-medium text-foreground">{r.value}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
