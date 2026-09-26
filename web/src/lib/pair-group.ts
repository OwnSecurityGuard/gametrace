// 配对组（一问一答）的血缘解析 —— 协议事件展开行的左请求 / 右响应数据来源。
//
// 为什么需要它：事件表默认按页取数据，配对伙伴只在「当前这一页」里找，
// 于是响应落在第 3 页、或用户从状态变更视图定位单条消息时，行上有配对角标、
// 展开却退化成一条普通 JSON（静默降级：用户分不清「没配上」和「没查到」）。
// 这里把「按血缘补查 + 合并 + 分组」收成一个纯函数，视图只负责渲染。
import type { DecodedEvent } from "@/types/event";

/**
 * 取一组配对的 expr filter。
 *
 * 只用 causation_id（响应 → 请求事件 id，pair 规则写入），不用 correlation_id：
 * 后者可能是插件解码侧写的流键（全流共享），按它查会把整条流拉进来。
 * 请求那一侧的行可能压根没写回 correlation/causation（宿主只改内存指针时
 * 已落库的请求行不会被重写），所以请求 → 响应方向必须按 `causation_id == 请求 id` 反查。
 */
export function pairGroupFilter(event: DecodedEvent): string {
  const reqId = event.causation_id || event.id;
  const quoted = JSON.stringify(reqId);
  return event.causation_id
    ? `id == ${quoted} || causation_id == ${quoted}`
    : `causation_id == ${quoted}`;
}

/** 合并页内伙伴与服务端配对组：按 id 去重、剔除自己，页内那份更新鲜所以优先。 */
export function mergePartnerPool(
  event: DecodedEvent,
  local: DecodedEvent[],
  remote: DecodedEvent[] | undefined,
): DecodedEvent[] {
  const byId = new Map<string, DecodedEvent>();
  for (const p of local) if (p.id !== event.id) byId.set(p.id, p);
  for (const p of remote ?? []) if (p.id !== event.id && !byId.has(p.id)) byId.set(p.id, p);
  return [...byId.values()];
}

/**
 * 从配对池里解出这一组。
 *
 * - 当前事件是请求（无 causation_id）：响应 = 池子里指向它的成员。
 * - 当前事件是响应：请求按 causation_id 取，响应含兄弟响应（一问多答要全看到）；
 *   请求查不到时 responses 为空，调用方据此退回单事件展示。
 */
export function resolvePairGroup(
  event: DecodedEvent,
  pool: DecodedEvent[],
): { request: DecodedEvent; responses: DecodedEvent[] } {
  const request = event.causation_id ? pool.find((p) => p.id === event.causation_id) : event;
  if (!request) return { request: event, responses: [] };
  if (request.id === event.id) {
    return { request, responses: pool.filter((p) => p.causation_id === event.id) };
  }
  return {
    request,
    responses: [event, ...pool.filter((p) => p.causation_id === request.id && p.id !== event.id)],
  };
}
