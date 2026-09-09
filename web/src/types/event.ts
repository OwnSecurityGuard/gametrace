/** list_decoded_data 返回的单个事件 */
export interface DecodedEvent {
  id: string;
  timestamp: string;
  session_id: string;
  protocol: string;
  raw_len: number;
  /** pair 语义规则写入的配对键：同一组请求/响应共享同一个值（未配对时为空）。 */
  correlation_id?: string;
  /** 响应侧特有：指向触发它的请求事件 id。 */
  causation_id?: string;
  /** extract 语义规则产出的子事件特有：指向其父事件 id（顶级事件为空）。 */
  parent_id?: string;
  data: Record<string, unknown>;
  /** 代理抓包特有：捕获上下文（Captured By / Connection / Stream / Source）。 */
  capture?: import("./connection").CaptureContext;
}

/** list_decoded_data 完整响应 */
export interface ListDecodedDataResult {
  ok: boolean;
  total_matched: number;
  count: number;
  events: DecodedEvent[];
}

/** get_capture_schema 返回的列信息 */
export interface SchemaColumn {
  name: string;
  type: string;
  description: string;
}

/** get_capture_schema 返回的数据源 */
export interface SchemaSource {
  name: string;
  description: string;
  columns: SchemaColumn[];
}

/** get_capture_schema 返回的规则 */
export interface SchemaRule {
  name: string;
  filter: string;
  type: string;
  window: string;
  group_by: string[];
  value: string;
  output: string;
}

/** get_capture_schema 完整响应 */
export interface CaptureSchemaResult {
  ok: boolean;
  sources: SchemaSource[];
  query_fields: SchemaColumn[];
  rules: SchemaRule[];
  examples?: {
    list_decoded_data_filter?: string[];
  };
}
