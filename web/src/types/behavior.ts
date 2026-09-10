/** begin_capture_run 完整响应 */
export interface BeginCaptureRunResult {
  ok?: boolean;
  run_id: string;
  time_from: string;
  capture_status: string;
  capture_isolation_mode: string;
  session_id: string;
  uncertainties?: string[];
}

/** 行为窗口增量统计 */
export interface RunSummary {
  captured_flow_count: number;
  captured_message_count: number;
  client_request_count: number;
  server_message_count: number;
  decode_error_count: number;
}

/** end_capture_run 完整响应 */
export interface EndCaptureRunResult {
  ok?: boolean;
  run_id: string;
  time_to: string;
  duration_ms: number;
  summary: RunSummary;
  idempotent?: boolean;
}

/** get_run_status 完整响应 */
export interface RunStatusResult {
  ok?: boolean;
  run_id: string;
  status: string;
  time_from: string;
  flow_count?: number;
  client_request_count?: number;
  server_message_count?: number;
  decode_error_count?: number;
}
