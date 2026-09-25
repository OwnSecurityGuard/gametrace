/**
 * 解码失败原因（query_decode_errors）的类型。
 *
 * 后端把失败按**归一化错误模板**聚合：同一类错误（只差数字/地址/长度）只占一组，
 * 所以错误再多，这里也只会有有限几组（见 pkg/decode/errorcol.go）。
 */

/** 一类解码失败。 */
export interface DecodeErrorGroup {
  /**
   * plugin = 插件主动报「这条我解不了」；transport = 流断开 / 超时 / 包无法还原；
   * binding = 会话绑定的插件没有可用实例（未启动 / 离线 / 不属于当前项目），
   * 解码器从未接上。plugin 与 transport 的次数是按包算，binding 按状态跳变算，
   * 所以「有包、0 事件、解码失败 1 次」也是整场没解码。
   */
  kind: string;
  /** 归一化模板，同类错误共用，例如 "unexpected EOF at offset <n>"。 */
  template: string;
  /** 该类失败的次数。 */
  count: number;
  /** 首条原始错误文本（未归一化），用于还原真实原因。 */
  sample?: string;
  /** 该组的代表包 id，可用它下钻原始包。 */
  sample_raw_packet_id?: string;
  sample_src?: string;
  sample_dst?: string;
  first_seen?: string;
  last_seen?: string;
}

/** query_decode_errors 的响应。 */
export interface QueryDecodeErrorsResult {
  session_id: string;
  /** 失败总次数（各组之和），与会话状态的 decode_errors 同口径。 */
  total_failures: number;
  /** 错误种类数。远小于 total_failures 时说明是同一类错误在反复发生。 */
  kinds: number;
  groups: DecodeErrorGroup[];
  /** 后端附的说明（尤其是「没有记录」与「没有失败」的区别）。 */
  note?: string;
}
