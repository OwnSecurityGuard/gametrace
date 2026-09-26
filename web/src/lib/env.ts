/** 是否启用「原始包」调试界面。
 * 对应后端 --enable-raw-debug / GT_MCP_ENABLE_RAW_DEBUG：两侧都默认开启，
 * 会话详情页的「原始数据」视图依赖它（无插件时也是唯一的看包入口）。
 * 需要收回暴露面时显式设 VITE_ENABLE_RAW_DEBUG=0 构建（后端同理设 0）。 */
export const RAW_DEBUG_ENABLED =
  (import.meta.env as Record<string, string | boolean | undefined>).VITE_ENABLE_RAW_DEBUG !==
  "0";
