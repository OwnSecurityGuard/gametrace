/** 已注册插件对应的制品视图（Developer Plane 磁盘扫描，status_plugin.artifact 同源） */
export interface PluginArtifact {
  /** 制品态：unknown | scaffolded | compiled | validated */
  state: string;
  /** 源码目录（插件不在 Dev Plane 目录时为空串） */
  source_dir: string;
  /** 构建产物路径（未构建过时为空串） */
  binary_path: string;
  /** 源码比二进制新 → 需要重新 build_plugin */
  binary_stale: boolean;
}

/** list_registered_plugins 返回的单个已注册插件摘要 */
export interface RegisteredPlugin {
  instance_id: string;
  name: string;
  protocol: string;
  type: string;
  api_version: string;
  online: boolean;
  last_heartbeat: number;
  /** 注册者（token 模式下为用户名；匿名模式为空串） */
  owner?: string;
  /** 制品视图（Developer Plane 已连接时返回；未连接或磁盘无此插件时不返回） */
  artifact?: PluginArtifact;
}

/** list_registered_plugins 返回的最近注册失败记录（后端 15min TTL 自动消失） */
export interface PluginRegisterFailure {
  name: string;
  error: string;
  owner?: string;
  timestamp_unix: number;
}

/** list_registered_plugins 完整响应 */
export interface ListRegisteredPluginsResult {
  ok: boolean;
  plugins: RegisteredPlugin[];
  /** 最近的注册失败（manifest / 语义契约被拒，或隧道 Connect 流被拒等） */
  recent_register_failures?: PluginRegisterFailure[] | null;
}

/** set_session_plugin 响应 */
export interface SetSessionPluginResult {
  ok: boolean;
  session_id: string;
  plugin: string;
  message?: string;
}

/** deregister_plugin 响应 */
export interface DeregisterPluginResult {
  ok: boolean;
  instance_id: string;
  name: string;
}

/** stop_capture 响应 */
export interface StopCaptureResult {
  ok: boolean;
  status: string;
  session_id: string;
  raw_packets: number;
  events: number;
  metrics: number;
  decode_errors: number;
  duration_sec: number;
}
