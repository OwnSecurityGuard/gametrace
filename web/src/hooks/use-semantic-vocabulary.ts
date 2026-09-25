/**
 * 语义标签多选下拉的数据源。
 *
 * 语义（annotate 结果）由各项目的解码插件声明，词表和数量都不固定，
 * 因此不能像 direction 那样写成闭集分段按钮 —— 必须从数据里长出来。
 * 这里把 get_protocol_catalog 的 per-protocol semantic 汇总成会话级词表。
 */
import { useMemo } from "react";
import { useProtocolCatalog } from "@/hooks/use-mcp";

export interface SemanticVocabulary {
  /** 按标签名排序的词表。 */
  labels: string[];
  /** 标签 → 该标签下的协议条目数（下拉框里的计数）。 */
  counts: Record<string, number>;
  isLoading: boolean;
}

export function useSemanticVocabulary(sessionId: string | null): SemanticVocabulary {
  const { data, isLoading } = useProtocolCatalog(sessionId);
  return useMemo(() => {
    const counts: Record<string, number> = {};
    for (const p of data?.protocols ?? []) {
      for (const s of p.semantic ?? []) counts[s] = (counts[s] ?? 0) + 1;
    }
    return {
      labels: Object.keys(counts).sort(),
      counts,
      isLoading,
    };
  }, [data, isLoading]);
}
