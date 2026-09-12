/**
 * 前端过滤工具（协议数据页的事件 / 关系 / 状态变更 / 原始数据四视图共用）。
 *
 * 语义约定：
 *  - 大小写不敏感的子串匹配；
 *  - 多关键词以空白分隔，AND 语义（所有关键词都命中才算匹配）；
 *  - 对每个事件把「协议名 / 消息名 / 方向 / id / 关联 id / 业务 payload」拼成一个
 *    字段包再统一下沉匹配，让用户随便输入某个值的片段都能搜到；
 *  - 方向过滤与模糊搜索叠加（AND）：direction 为空时视为「全部方向」不过滤。
 *
 * 纯前端过滤：仅在已加载的数据上做子串匹配，不调用后端 filter 表达式。
 */
import type { DecodedEvent } from "@/types/event";
import type { Change } from "@/types/state-change";
import type { ConnectionSummary } from "@/types/connection";
import { extractMeta } from "@/lib/event-display";

/** 查询串 → 小写关键词数组（去空白、去空串）。 */
export function fuzzyTokens(query: string): string[] {
  return query
    .trim()
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean);
}

/** 消息方向过滤值："" = 全部方向（不过滤），其余为后端 direction 枚举。 */
export type DirectionFilter = "" | "client_to_server" | "server_to_client";

/** 事件是否命中方向过滤（空值恒真，与模糊搜索叠加为 AND）。 */
export function eventMatchesDirection(ev: DecodedEvent, direction: DirectionFilter): boolean {
  if (!direction) return true;
  return extractMeta(ev.data, ev.meta).direction === direction;
}

/** 事件是否命中连接过滤（conn 为 null 时视为全部连接）。按捕获上下文 conn_id 匹配。 */
export function eventMatchesConnection(ev: DecodedEvent, conn: ConnectionSummary | null): boolean {
  if (!conn) return true;
  return ev.capture?.conn_id === conn.conn_id;
}

/** 状态变更是否命中连接过滤（conn 为 null 视为全部连接）。变更携带的连接标识来自源消息。 */
export function changeMatchesConnection(change: Change, conn: ConnectionSummary | null): boolean {
  if (!conn) return true;
  const cid = conn.conn_id;
  return change.flow_id === cid || change.source?.conn_id === cid;
}

/** 字段变更是否命中方向过滤（按其来源消息的 direction；空值恒真）。 */
export function changeMatchesDirection(c: Change, direction: DirectionFilter): boolean {
  if (!direction) return true;
  return c.source.direction === direction;
}

/** 字段包 must 包含所有的关键词（AND）。空查询恒为真。 */
export function haystackIncludes(haystack: string, tokens: string[]): boolean {
  if (tokens.length === 0) return true;
  return tokens.every((t) => haystack.includes(t));
}

/** 任意值 → 小写扁平文本，供子串匹配。 */
function flatten(o: unknown): string {
  try {
    return JSON.stringify(o).toLowerCase();
  } catch {
    return "";
  }
}

/**
 * 事件是否命中查询。
 * 覆盖协议名、消息名、方向、id / correlation / causation / parent、
 * 以及 data / meta / analysis 全量序列化文本。
 */
export function eventMatchesQuery(ev: DecodedEvent, query: string): boolean {
  const tokens = fuzzyTokens(query);
  if (tokens.length === 0) return true;

  const meta = extractMeta(ev.data, ev.meta);
  const haystack = [
    ev.protocol,
    meta.msgName,
    meta.direction,
    ev.correlation_id || "",
    ev.causation_id || "",
    ev.parent_id || "",
    ev.id,
    flatten({ data: ev.data, meta: ev.meta, analysis: ev.analysis }),
  ].join(" ").toLowerCase();

  return haystackIncludes(haystack, tokens);
}

/**
 * 字段变更是否命中查询。
 * 覆盖主体类型/id、实体键、字段路径、操作、来源消息名/事件 id、变更前/后值。
 */
export function changeMatchesQuery(c: Change, query: string): boolean {
  const tokens = fuzzyTokens(query);
  if (tokens.length === 0) return true;

  const haystack = [
    c.subject_type,
    c.subject_id,
    c.entity_key,
    c.path,
    c.op,
    c.source.msg_name,
    c.source.event_id,
    flatten(c.before),
    flatten(c.after),
  ].join(" ").toLowerCase();

  return haystackIncludes(haystack, tokens);
}