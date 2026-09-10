/**
 * 状态变更分析（query_state_changes / get_state_change_detail）的类型。
 *
 * 三种视图（按操作 / 按实体 / 按时间）共用同一份 changes：
 * 后端一次查询同时给出扁平变更流与四种分组，前端切换视图不重新查询。
 */

/** 变更操作类型（对齐后端 event.StateChange.Validate 的限定值）。 */
export type StateChangeOp = "set" | "delete" | "merge";

/** 锚点类型：决定时间原点 T0 与是否附加实体约束。 */
export type AnchorKind = "" | "operation" | "entity" | "event";

/** 聚合维度。 */
export type GroupBy = "operation" | "entity" | "event" | "time";

/** 排序方式。 */
export type SortBy = "time" | "first_change" | "change_count";

/** 消息语义：请求 / 响应 / 推送。 */
export type MessageKind = "request" | "response" | "push" | "unknown";

/** 产生变更的协议消息。 */
export interface MessageRef {
  event_id: string;
  timestamp: string;
  /** 相对锚点的偏移（毫秒），用于 T+220ms 展示。 */
  offset_ms: number;
  msg_name: string;
  kind: MessageKind | string;
  direction?: string;
  flow_id?: string;
  conn_id?: string;
  correlation_id?: string;
  causation_id?: string;
  /** 操作分组键：有 correlation 用 correlation，否则用自身事件 ID。 */
  operation_key: string;
}

/** 一条富化后的字段变更。 */
export interface Change {
  id: string;
  /** 全局顺序号（按发生时间排）。 */
  seq: number;
  event_id: string;
  session_id?: string;
  flow_id?: string;
  timestamp: string;
  offset_ms: number;
  subject_type: string;
  subject_id: string;
  entity_key: string;
  op: StateChangeOp | string;
  path: string;
  before?: unknown;
  after?: unknown;
  version?: number;
  metadata?: unknown;
  source: MessageRef;
}

/** 某操作/事件对某实体造成的影响。 */
export interface OperationHit {
  key: string;
  label: string;
  kind: MessageKind | string;
  event_id: string;
  timestamp: string;
  offset_ms: number;
  change_count: number;
  field_paths: string[];
  /** 改动了本实体的具体消息（请求/响应/推送）。 */
  events?: MessageRef[];
}

export interface EntityGroup {
  subject_type: string;
  subject_id: string;
  key: string;
  change_count: number;
  field_count: number;
  field_paths: string[];
  first_change?: string;
  last_change?: string;
  first_offset_ms: number;
  last_offset_ms: number;
  operations?: OperationHit[];
  changes?: Change[];
}

export interface OperationGroup {
  key: string;
  label: string;
  kind: MessageKind | string;
  anchor: MessageRef;
  /** 协议连：请求 → 响应 → 推送。 */
  chain: MessageRef[];
  change_count: number;
  entity_count: number;
  field_count: number;
  start_offset_ms: number;
  end_offset_ms: number;
  entities?: EntityGroup[];
  changes?: Change[];
}

export interface EventGroup {
  event_id: string;
  source: MessageRef;
  change_count: number;
  entity_count: number;
  changes?: Change[];
}

export interface BucketEntity {
  key: string;
  subject_type: string;
  subject_id: string;
  change_count: number;
  field_paths: string[];
}

export interface BucketOperation {
  key: string;
  label: string;
  kind: MessageKind | string;
  offset_ms: number;
  change_count: number;
  entities: BucketEntity[];
}

export interface TimeBucket {
  index: number;
  start_offset_ms: number;
  end_offset_ms: number;
  start: string;
  change_count: number;
  entity_count: number;
  operations?: BucketOperation[];
}

export interface StateChangeSummary {
  change_count: number;
  entity_count: number;
  field_count: number;
  operation_count: number;
  event_count: number;
  first_change?: string;
  last_change?: string;
  /** 每个实体变了几次、涉及哪些字段（按变化数降序）。 */
  entity_changes?: EntityGroup[];
}

export interface AnchorRef {
  kind: AnchorKind;
  id: string;
  t0: string;
  resolved: boolean;
  note?: string;
  source?: MessageRef;
}

export interface QueryWindow {
  from?: string;
  to?: string;
  before_ms: number;
  after_ms: number;
}

export interface QueryStateChangesResult {
  ok: boolean;
  session_id: string;
  group_by: GroupBy;
  sort_by: SortBy;
  desc: boolean;
  anchor?: AnchorRef;
  window: QueryWindow;
  /** 是否因 limit 截断。 */
  truncated: boolean;
  summary: StateChangeSummary;
  changes: Change[];
  operations?: OperationGroup[];
  entities?: EntityGroup[];
  events?: EventGroup[];
  buckets?: TimeBucket[];
}

/** ===== 详情 ===== */

export interface ChainEntity {
  key: string;
  subject_type: string;
  subject_id: string;
  change_count: number;
  field_paths: string[];
  changes?: Change[];
}

export interface ChainStep {
  message: MessageRef;
  offset_ms: number;
  change_count: number;
  entities?: ChainEntity[];
}

export interface Chain {
  operation: MessageRef;
  event_count: number;
  steps: ChainStep[];
}

export interface FieldHistory {
  path: string;
  change_count: number;
  ops: string[];
  first_value?: unknown;
  last_value?: unknown;
  first_change?: string;
  last_change?: string;
  changes?: Change[];
}

export interface EntityHistory {
  subject_type: string;
  subject_id: string;
  key: string;
  change_count: number;
  first_change?: string;
  last_change?: string;
  fields?: FieldHistory[];
  timeline?: Change[];
}

export interface StateChangeDetail {
  session_id: string;
  t0?: string;
  change?: Change;
  chain?: Chain;
  history?: EntityHistory;
  field_history?: FieldHistory;
}

export interface StateChangeDetailResult extends StateChangeDetail {
  ok: boolean;
}
