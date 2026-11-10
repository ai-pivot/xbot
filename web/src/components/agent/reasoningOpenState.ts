/**
 * reasoningOpenState — 仅保留思考块的稳定 key 派生。
 *
 * ⚠️ 2026-10-20：**展开态不再做任何缓存**。原先这里有一个模块级 Map
 * （`turnID:iteration` → open），用于"commit 成历史时不要自动收起"（2026-09-12 需求）。
 * 但它带来两个问题：
 *   1. turnID 缺失时 key 退化成 `0:N`，所有 turn 的同号迭代共用 → 没点过的思考块也展开；
 *   2. 状态只在进程内存里，刷新即丢 —— 表现为「刷新就没了」的不一致行为。
 * 展开是瞬态交互，交给 `ThinkingLine` 的本地 state；remount 后回到默认折叠。
 */
/** 思考块展开态的共享 key（live 与 committed 必须用同一个构造函数）。 */
export function reasoningKey(turnID: number | null | undefined, iteration: number): string {
  // ⚠️ turnID 无效时**不能**退化成 "0:N"：那会让所有 turn 中同号的迭代共用同一个
  // key —— 点开任意一个「思考」，其他 turn 的思考块也跟着展开（2026-10-20 用户
  // 报告：没点过的思考内容自己显示出来）。返回空串 → ThinkingLine 退回组件本地
  // state（默认折叠、不跨迭代共享），也不会写入共享表。
  if (turnID == null || turnID <= 0) return ''
  return `${turnID}:${iteration}`
}
