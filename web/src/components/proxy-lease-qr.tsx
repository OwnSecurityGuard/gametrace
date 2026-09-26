// proxy-lease-qr.tsx — 手机怎么连上某个代理租约：二维码 + 可复制地址 + 客户端下载。
//
// 从「代理服务器配置」弹窗里抽出来，是因为「开始抓包 → 手机代理」同样需要它：
// 用户此刻的诉求就是"给我二维码，我要让手机连上"，再点一层去建租约等于把主流程
// 放到了别处。两处共用同一块渲染，connect_addr / singbox_uri 的口径也不会分叉。
import { useState } from "react";
import QRCode from "react-qr-code";
import {
  Copy,
  CopyCheck,
  Download,
  ExternalLink,
  MonitorSmartphone,
  Smartphone,
} from "lucide-react";
import { toast } from "@/components/ui/toast";
import type { ProxyLease } from "@/types/proxy";

/** 手机端 sing-box 官方客户端下载地址（Project S 官方分发渠道）。 */
const SINGBOX_CLIENTS = [
  {
    key: "android-play",
    label: "Android · Google Play",
    href: "https://play.google.com/store/apps/details?id=io.nekohasekai.sfa",
    title: "sing-box for Android (SFA) · Google Play",
  },
  {
    key: "android-apk",
    label: "Android · APK",
    href: "https://github.com/SagerNet/sing-box/releases",
    title: "sing-box for Android (SFA) · GitHub Releases（含 APK / F-Droid）",
  },
  {
    key: "ios",
    label: "iOS · App Store",
    href: "https://apps.apple.com/app/sing-box-vt/id6673731168",
    title: "sing-box for Apple platforms (SFI) · 需使用非中国大陆区 Apple ID",
  },
] as const;

/** 客户端下载入口：装上 sing-box 才能扫码导入。 */
function SingboxDownloadHint() {
  return (
    <div className="w-full border-t border-border pt-2.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="flex items-center gap-1">
          <Download className="h-3.5 w-3.5" />
          手机还没装 sing-box？
        </span>
        {SINGBOX_CLIENTS.map((c) => (
          <a
            key={c.key}
            href={c.href}
            target="_blank"
            rel="noopener noreferrer"
            title={c.title}
            className="inline-flex items-center gap-0.5 font-medium text-primary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {c.label}
            <ExternalLink className="h-3 w-3" />
          </a>
        ))}
      </div>
      <p className="mt-1 text-micro leading-relaxed text-muted-foreground">
        用其他客户端（Clash 等）时，按上面的地址手动配置 HTTP 代理即可。
      </p>
    </div>
  );
}

/** 租约的手机接入面板。qrSize 让调用方按容器宽窄决定二维码尺寸（弹窗里 132 足够扫）。 */
export function LeaseQrPanel({ lease, qrSize = 132 }: { lease: ProxyLease; qrSize?: number }) {
  const [copied, setCopied] = useState(false);
  const singboxUri = lease.singbox_uri ?? "";
  const connectAddr = lease.connect_addr ?? "";
  // 手机连的是宿主映射后的端口；public_port 是权威值，兜底旧的 agent_listen_port
  //（非容器部署两者相同）。
  const publicPort = lease.public_port || lease.agent_listen_port;
  const value = singboxUri || connectAddr;
  const connected = (lease.active_conns ?? 0) > 0;

  async function handleCopy() {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      toast.success(singboxUri ? "已复制 sing-box 导入链接" : "已复制连接地址", value);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.info("复制失败，请手动选择地址", value);
    }
  }

  return (
    <div className="@container space-y-2.5 rounded-lg border border-border bg-background p-2.5">
      {/* 窄弹窗里二维码和信息列竖排：并排会把右侧压成十几字的折行。 */}
      <div className="flex flex-col gap-3 @xs:flex-row">
        <div className="shrink-0 self-start rounded-md bg-white p-1.5 shadow-sm">
          {value ? (
            <QRCode value={value} size={qrSize} bgColor="#ffffff" fgColor="#0f172a" />
          ) : (
            <div
              className="flex items-center justify-center px-2 text-center text-micro text-muted-foreground"
              style={{ width: qrSize, height: qrSize }}
            >
              未拿到局域网地址，无法生成二维码
            </div>
          )}
        </div>

        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex items-center gap-1.5 text-sm font-medium text-foreground">
            <Smartphone className="h-3.5 w-3.5 text-muted-foreground" />
            手机扫码接入
            <span
              className={
                "ml-auto rounded px-1.5 py-0.5 text-2xs font-normal " +
                (connected ? "bg-success/15 text-success" : "bg-muted text-muted-foreground")
              }
            >
              {connected ? "已接入" : "等待接入"}
            </span>
          </div>

          <button
            type="button"
            onClick={handleCopy}
            disabled={!value}
            title={singboxUri ? "复制 sing-box 导入链接" : "复制连接地址"}
            className="flex w-full items-center gap-2 rounded-md border border-border bg-background px-2 py-1.5 text-left transition-colors hover:border-primary/40 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {copied ? (
              <CopyCheck className="h-3.5 w-3.5 shrink-0 text-success" />
            ) : (
              <Copy className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            )}
            <span className="min-w-0 flex-1 truncate font-mono text-xs">{value || "——"}</span>
          </button>

          {singboxUri ? (
            <p className="text-micro leading-relaxed text-muted-foreground">
              sing-box「添加配置 → 扫描二维码」导入，自动生成 TUN 配置并连到{" "}
              <code className="font-mono">{connectAddr || "本机"}</code>。
            </p>
          ) : (
            <p className="text-micro leading-relaxed text-muted-foreground">
              手机代理软件添加 HTTP 代理，服务器填{" "}
              <code className="font-mono">{lease.lan_ip || "本机IP"}</code>，端口填{" "}
              <code className="font-mono">{publicPort}</code>。
            </p>
          )}

          <p className="flex items-center gap-1 text-micro text-muted-foreground">
            <MonitorSmartphone className="h-3 w-3 shrink-0" />
            <span className="min-w-0 truncate">
              手机连上后上方会变成「已接入」，这时再点「开始抓包」才有数据。
            </span>
          </p>
        </div>
      </div>

      {/* 装上客户端才扫得动码，所以下载入口跟着二维码走。 */}
      <SingboxDownloadHint />
    </div>
  );
}
