/** decode_raw_packets 返回的完整响应 */
export interface DecodeRawPacketsResult {
  ok: boolean;
  status: string;
  session_id: string;
  plugin: string;
  total_raw: number;
  decoded: number;
  decode_errors: number;
  clear_existing: boolean;
}
