/**
 * 解码失败原因的展示派生（纯函数，无 React 依赖，便于单测）。
 *
 * 为什么需要这一层：后端给的是「归一化模板 + 次数」，而用户要的是
 * 「主要是哪一种、该往哪查」。把翻译收在这里，组件只负责画。
 */

import type { DecodeErrorGroup } from "@/types/decode-error";

/** 把错误来源翻译成人话。 */
export function describeErrorKind(kind: string): string {
  switch (kind) {
    case "plugin":
      // 插件跑通了链路、但明确说这条数据它解不了 —— 最常见的是协议不匹配。
      return "插件无法解析";
    case "transport":
      // 链路本身出问题：流断开、等待超时、包无法还原。
      return "解码链路中断";
    case "binding":
      // 解码器压根没接上：会话绑定的插件没有可用实例。
      return "解析插件未接入";
    default:
      return "其他失败";
  }
}

/** 来源对应的语义色调（与项目里的 tone 约定一致）。 */
export function errorKindTone(kind: string): "warn" | "error" | "muted" {
  switch (kind) {
    case "plugin":
      return "warn";
    case "transport":
    case "binding":
      // binding 比 transport 更该显眼：它意味着整个会话一个事件都不会有。
      return "error";
    default:
      return "muted";
  }
}

/**
 * 一句话摘要：主要是什么问题。
 *
 * 只在「主因占比很高」时才点名主因 —— 三类错误各占三分之一时说"主要是某某"
 * 是误导，此时应该说的是"错误分散在几类里"。
 */
export function summarizeDecodeErrors(groups: DecodeErrorGroup[], total: number): string {
  const top = groups[0];
  if (!top) {
    // 空分组有两种含义：真的没失败，或失败过但没记录原因（旧版本抓的）。
    // 混为一谈会让用户以为「没有失败」，那是最坏的一种错。
    return total > 0 ? "失败原因未记录（可能由旧版本抓取）" : "没有解码失败";
  }
  const ratio = total > 0 ? top.count / total : 0;
  if (groups.length === 1) {
    return `全部是同一类错误：${describeErrorKind(top.kind)}`;
  }
  if (ratio >= 0.8) {
    return `主要是${describeErrorKind(top.kind)}（占 ${Math.round(ratio * 100)}%）`;
  }
  return `错误分散在 ${groups.length} 类里，最常见的是${describeErrorKind(top.kind)}`;
}

/** 默认直接展示的分组数；超出的折叠，避免一屏全是错误。 */
export const DECODE_ERROR_VISIBLE = 3;
