// session-source.ts — 会话「从哪儿抓的」的唯一口径。
//
// 后端不同链路写进 sessions.source 的词并不统一：start_capture 透传调用方给的
// source（agent / proxy / 空），probe_start_capture 写 "probe"，归档回放写
// "probe-archive"。曾经各处只认 "agent"，于是探针抓的会话在概览里被叫成
// 「服务器网卡」——同一个会话在两个页面两种说法。判定收敛到这里一次。

/** 该 source 是否探针链路（gt-agent 推流或探针归档回放）。 */
export function isProbeSource(source?: string): boolean {
  return source === "agent" || source === "probe" || source === "probe-archive";
}

/** 来源展示名。空值 = 直接在服务网卡上抓（历史默认路径）。 */
export function captureSourceName(source?: string): string {
  if (isProbeSource(source)) return "抓包探针";
  if (source === "proxy") return "手机代理";
  return "服务器网卡";
}
