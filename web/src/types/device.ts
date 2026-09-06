/**
 * 「我的设备」接入状态：把「探针（list_probes）+ 启动码（list_access_codes）」两个数据源
 * 捏成用户可感知的设备状态。不引入 Device Service —— 一台接入的机器就是一个探针，
 * 启动码只负责把它接进来；抓包由「开始抓包」下发。
 */

/** 设备接入状态机。 */
export type DeviceState = "waiting" | "connected" | "capturing" | "stopped" | "offline";

/** 由探针与会话派生的单台设备视图（接入后再捕获，不引入额外存储）。 */
export interface DeviceView {
  /** 设备种类：probe=已接入的探针 / code=待接入的启动码 */
  kind: "probe" | "code";
  /** 稳定 id：探针为 probe_id，启动码为 code 本身 */
  id: string;
  /** 启动码（仅 waiting 设备有，形如 GT-XXXX-XXXX） */
  code?: string;
  /** 探针 id（kind=probe 时有） */
  probeId?: string;
  /** 展示名（探针取 name/hostname，启动码取 code） */
  name?: string;
  /** 探针 hostname */
  hostname?: string;
  /** 目标平台，如 windows/amd64 */
  platform?: string;
  /** 绑定的项目 ID */
  projectId?: string;
  /** 抓包端口 */
  port?: number;
  /** 解码插件 */
  plugin?: string;
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
  /** 是否已被认领（一次性启动码） */
  claimed?: boolean;
  /** 创建时间（ISO） */
  createdAt: string;
}
