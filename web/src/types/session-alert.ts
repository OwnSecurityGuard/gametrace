/**
 * 「命中提醒」（list_session_alerts）的类型。
 *
 * 一次命中 = 一行：项目里的检查规则在本会话的解码流上命中时，pipeline 会把
 * 「触发记录 + 触发前各方向最近 N 条」一起落库（见 pkg/checkrule.AlertBundle）。
 * 这里就是那份数据的读取形态，供会话内回看「是哪些数据导致了通知」。
 */

/** 一条解码记录的告警形态（trigger / context / after 共用）。 */
export interface AlertRecord {
  id: string;
  timestamp: string;
  /** 事件类型（带插件前缀，如 game.LoginResp）。 */
  type: string;
  /** 原始方向值（client_to_server / server_to_client / 插件自定义）。 */
  direction: string;
  /** 业务 payload；后端按上限裁剪，超限时可能是带省略标记的字符串。 */
  data?: unknown;
  meta?: Record<string, unknown>;
}

/** 一次检查规则命中。 */
export interface SessionAlert {
  alert_id: string;
  rule_id: string;
  rule_name: string;
  title: string;
  message: string;
  /** 触发记录的时刻（列表按它倒序）。 */
  timestamp: string;
  /** 命中生成时刻，可能略晚于 timestamp（异步下发）。 */
  generated_at?: string;
  trigger?: AlertRecord;
  /** 触发前各方向最近若干条，键为 request / response / unknown。 */
  context?: Record<string, AlertRecord[]>;
  /** 触发后最近若干条（时间正序扁平列表；仅在请求带 after 时返回）。 */
  after?: AlertRecord[];
}

/** list_session_alerts 完整响应。 */
export interface ListSessionAlertsResult {
  ok: boolean;
  count: number;
  alerts: SessionAlert[];
  limit?: number;
  offset?: number;
}

/** 上下文分组键的展示名（后端 normalizeDir 的三桶）。 */
export function directionBucketText(bucket: string): string {
  switch (bucket) {
    case "request":
      return "请求方向";
    case "response":
      return "响应方向";
    default:
      return "其他方向";
  }
}
