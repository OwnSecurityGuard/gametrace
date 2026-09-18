/**
 * 状态变更里 before 的展示语义。
 *
 * before 有两种来源，可信度不同，混着显示会把「首次同步」读成业务动作：
 *  - 平台持有的旧值（before_resolved=true）：真实变化，展示为「变更前」；
 *  - 平台没见过该字段（before_resolved=false 且 before 为空）：首见；
 *  - 插件自己声明的前值（before_resolved=false 但 before 有值）：平台未验证的参考值。
 *
 * 抽成纯函数是为了让这三种情况的判定有单测兜底 —— 判定错一位，页面就会把批量同步
 * 当成业务动作展示。
 */
export type BeforeKind = "resolved" | "first-seen" | "plugin-declared";

export function beforeKind(change: { before_resolved?: boolean; before?: unknown }): BeforeKind {
  if (change.before_resolved) {
    return "resolved";
  }
  return change.before === null || change.before === undefined ? "first-seen" : "plugin-declared";
}
