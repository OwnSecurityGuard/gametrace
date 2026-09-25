/**
 * 极简 hash 路由：解析当前 URL、订阅变化、跳转。
 *
 * 不引入路由库：层级只有三段、跳转点不到二十处，一个 useSyncExternalStore
 * 加 hashchange 就够，换来的是零依赖与完全可读的 URL。
 *
 * 快照按 hash 字符串缓存，同一 hash 下引用恒等 —— 这是 useSyncExternalStore
 * 不陷入无限重渲染的前提，也是"点击当前所在面包屑不触发重绘"的原因。
 */
import { useSyncExternalStore } from "react";
import {
  HOME_ROUTE,
  normalizeConfigTab,
  normalizeView,
  type Route,
} from "@/lib/routes";

/**
 * 解析 hash 为路由。纯函数（显式传参），便于在无 DOM 的环境下单测。
 * 未知路径一律回落工作台；查询串刻意忽略（筛选条件属于视图内状态，
 * 不进 URL，见 routes.ts 的空间层级说明）。
 */
export function parseRoute(hash: string = window.location.hash): Route {
  const raw = hash.replace(/^#\/?/, "").split("?")[0] ?? "";
  const [head, id, tail, tail2] = raw.split("/").filter(Boolean);

  if (head === "session" && id) {
    return {
      ...HOME_ROUTE,
      space: "session",
      sessionId: decodeSegment(id),
      view: normalizeView(tail),
    };
  }
  if (head === "unassigned") {
    // 与项目同构的桶：projectId 为空即"未归属"，会话由元数据反查而非 URL 决定。
    return { ...HOME_ROUTE, space: "project", projectId: null };
  }
  if (head === "project" && id) {
    const projectId = decodeSegment(id);
    if (tail === "config") {
      return {
        ...HOME_ROUTE,
        space: "project",
        projectId,
        projectSection: "config",
        configTab: normalizeConfigTab(tail2),
      };
    }
    return { ...HOME_ROUTE, space: "project", projectId };
  }
  return HOME_ROUTE;
}

/** href 归一：接受 "#/x"、"/x"、"x" 三种写法，统一成 location.hash 可用形态。 */
function toHash(href: string): string {
  if (href.startsWith("#")) return href === "#" ? "#/" : href;
  return `#${href.startsWith("/") ? href : `/${href}`}`;
}

/** href 生成侧做了 encodeURIComponent，解析侧必须还原；手打的非法转义序列不该让整页崩掉。 */
function decodeSegment(seg: string): string {
  try {
    return decodeURIComponent(seg);
  } catch {
    return seg;
  }
}

const listeners = new Set<() => void>();

function notify(): void {
  for (const l of listeners) l();
}

if (typeof window !== "undefined") {
  window.addEventListener("hashchange", notify);
}

let cachedHash: string | null = null;
let cachedRoute: Route = HOME_ROUTE;

function getSnapshot(): Route {
  const hash = window.location.hash;
  if (hash !== cachedHash) {
    cachedHash = hash;
    cachedRoute = parseRoute(hash);
  }
  return cachedRoute;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** 当前路由（随 hashchange 重渲染）。 */
export function useRoute(): Route {
  return useSyncExternalStore(subscribe, getSnapshot);
}

/**
 * 跳转。replace=true 用于"修正 URL"（初始化、不可用视图回落），
 * 不进入历史栈 —— 后退键应当回到用户真正来过的地方，而不是校正记录。
 */
export function navigate(href: string, opts: { replace?: boolean } = {}): void {
  const target = toHash(href);
  if (opts.replace) {
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}${target}`);
    notify();
    return;
  }
  if (window.location.hash === target) return;
  window.location.hash = target;
}

/** 当前 URL 是否已指向该目标（用于避免无意义的历史条目）。 */
export function isCurrentLocation(href: string): boolean {
  return window.location.hash === toHash(href);
}
