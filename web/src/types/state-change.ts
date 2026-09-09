/** list_state_changes 返回的单条实体状态变更（state_changes 投影表的一行）。 */

/** state_change 的操作类型（对齐后端 event.StateChange.Validate 的限定值）。 */
export type StateChangeOp = "set" | "delete" | "merge";

/** 单条实体状态变更：`subject_type:subject_id` 的 `path` 字段在 `op` 下从 before 变到 after。 */
export interface StateChangeRow {
  id: string;
  event_id: string;
  session_id: string;
  flow_id?: string;
  timestamp: string;
  subject_type: string;
  subject_id: string;
  op: StateChangeOp | string;
  path: string;
  /** 变更前值（JSON 对象，未解析基线时可能为空）。 */
  before?: unknown;
  /** 变更后值（JSON 对象）。 */
  after?: unknown;
  version: number;
  /** 附加元数据（JSON 对象，可能为空）。 */
  metadata?: unknown;
}

/** list_state_changes 完整响应 */
export interface ListStateChangesResult {
  ok: boolean;
  count: number;
  changes: StateChangeRow[];
}