/**
 * 「我的设备」接入状态：把「探针（list_probes）」捏成用户可感知的设备状态。
 * 不引入 Device Service —— 一台接入的机器就是一个探针；抓包由「开始抓包」下发。
 */

/** 设备接入状态机。 */
export type DeviceState = "connected" | "capturing" | "stopped" | "offline";

/** 由探针派生的单台设备视图（接入后再捕获，不引入额外存储）。 */
export interface DeviceView {
  /** 设备种类：probe=已接入的探针 */
  kind: "probe";
  /** 稳定 id：探针为 probe_id */
  id: string;
  /** 探针 id */
  probeId?: string;
  /** 展示名（探针取 name/hostname） */
  name?: string;
  /** 探针 hostname */
  hostname?: string;
  /** 目标平台，如 windows/amd64 */
  platform?: string;
  /** 当前接入状态 */
  state: DeviceState;
  /** 最近一次抓包会话 id */
  sessionId?: string;
  /** 已抓包数 */
  packets: number;
  /** 已解码事件数 */
  events?: number;
  /** 解码错误数 */
  decodeErrors?: number;
  /** 最近活动时间（ISO） */
  lastSeen?: string;
  /** 创建时间（ISO） */
  createdAt: string;
}