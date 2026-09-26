/** get_protocol_catalog 返回的协议级聚合条目（前端只取用到的字段）。 */
export interface ProtocolCatalogEntry {
  /** direction|msg_name 复合键。 */
  key: string;
  msg_name: string;
  direction: string;
  /** 该协议在本会话内实际出现过的 annotate 语义标签集合。 */
  semantic: string[];
  count: number;
  first_seen: string;
  last_seen: string;
}

/** get_protocol_catalog 完整响应。 */
export interface ProtocolCatalogResult {
  ok: boolean;
  error?: string;
  session_id: string;
  total_protocols: number;
  count: number;
  offset: number;
  limit: number;
  has_more: boolean;
  protocols: ProtocolCatalogEntry[];
}
