// ConfigDrawer — 右上角配置抽屉：收纳低频的全局配置入口。
//
// 抽屉不承载配置表单，只做「入口清单」——每一条点开的是既有的完整管理界面
// （插件面板 / 探针管理 / 成员管理 / 设置）。这样既把顶栏从七个按钮降回三个，
// 又不必为省空间去阉割每个管理界面本来就需要的表格宽度。
//
// 刻意不收录的：接入设备与 MCP 接入（一级可见，见顶栏）、代理通道
// （属于抓包方式，跟着「开始抓包」的通道选择走，不与成员同级）。
import { useEffect } from "react";
import { ChevronRight, KeyRound, Plug, Server, SlidersHorizontal, Users, X } from "lucide-react";

export type DrawerSection = "plugins" | "probes" | "members" | "account";

/** 顶栏/竖轨能要求的动作：打开抽屉总览，或直达某一节。 */
export type DrawerAction = DrawerSection | "overview";

const ENTRIES: { key: DrawerSection; group: string; label: string; hint: string; icon: typeof Plug }[] = [
  { key: "plugins", group: "资源", label: "解码插件", hint: "插件注册表按 owner 隔离", icon: Plug },
  {
    key: "probes",
    group: "资源",
    label: "探针管理",
    hint: "接入机器的状态 / 停抓 / 本地留存导入",
    icon: Server,
  },
  { key: "members", group: "资源", label: "成员管理", hint: "成员账号列表与撤销", icon: Users },
  { key: "account", group: "账号", label: "账号与访问令牌", hint: "令牌校验 / 身份注册", icon: KeyRound },
];

const GROUPS = ["资源", "账号"] as const;

interface ConfigDrawerProps {
  open: boolean;
  onClose: () => void;
  onOpen: (section: DrawerSection) => void;
}

export function ConfigDrawer({ open, onClose, onOpen }: ConfigDrawerProps) {
  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50" role="dialog" aria-modal="true" aria-label="配置">
      <div className="absolute inset-0 bg-slate-950/45 backdrop-blur-[2px] gt-fade-in" onClick={onClose} />
      <aside className="absolute inset-y-0 right-0 flex w-[19rem] max-w-[85vw] flex-col border-l border-border bg-popover shadow-xl gt-drawer-in">
        <header className="flex items-center gap-2 border-b border-border px-4 py-3">
          <SlidersHorizontal className="h-4 w-4 text-primary" />
          <h2 className="flex-1 text-sm font-semibold">配置</h2>
          <button
            type="button"
            aria-label="关闭配置"
            onClick={onClose}
            className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            <X className="h-4 w-4" />
          </button>
        </header>

        <div className="flex-1 overflow-auto gt-scroll p-3">
          {GROUPS.map((group) => (
            <section key={group} className="mb-4 last:mb-0">
              <h3 className="px-1 pb-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                {group}
              </h3>
              <div className="overflow-hidden rounded-lg border border-border bg-card">
                {ENTRIES.filter((e) => e.group === group).map((e) => (
                  <button
                    key={e.key}
                    type="button"
                    onClick={() => onOpen(e.key)}
                    className="flex w-full items-center gap-2.5 border-b border-border px-3 py-2.5 text-left transition-colors last:border-b-0 hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/40"
                  >
                    <e.icon className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium">{e.label}</span>
                      <span className="block truncate text-[11px] text-muted-foreground">{e.hint}</span>
                    </span>
                    <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground/60" />
                  </button>
                ))}
              </div>
            </section>
          ))}

          <p className="px-1 text-[11px] leading-relaxed text-muted-foreground">
            这里只放低频配置。「接入设备」与「MCP 接入」在顶栏一级；代理通道跟随 「开始抓包」的抓包方式选择。
          </p>
        </div>
      </aside>
    </div>
  );
}
