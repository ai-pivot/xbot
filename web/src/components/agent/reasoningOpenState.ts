/**
 * reasoningOpenState — 思考块展开状态的**会话内共享**存储。
 *
 * 背景（2026-09-12 用户报告）：live 迭代 commit 成历史迭代时，已经展开的思考块会
 * **自动收起**。原因是形态切换必然 remount（流式由 `LiveIteration` 渲染，commit 后
 * 由 `TurnBody → CommittedTurn → IterationGroup` 渲染），而展开态原本是组件内部
 * `useState` → remount 即丢失。用户要求：不要自动收起，也不要没必要的 DOM 重建。
 *
 * 方案：把展开态按「逻辑迭代」在模块级共享（key = `turnID:iteration`），live 与
 * committed 两种形态读写同一份状态 → remount 后初始值即用户此前的选择，视觉上不发生
 * 收起。DOM 仍会由 React 在该迭代形态变化时重建（不同渲染路径），但不含任何多余的
 * 重建（未变化的迭代由 `CommittedTurn`/`ThinkingLine` 的 memo 挡住）。
 *
 * key 有界（最多 MAX 条，FIFO 淘汰）—— 会话切换/长会话不会无界增长。
 */
const MAX_ENTRIES = 300
const openByKey = new Map<string, boolean>()

/** 读取某迭代思考块的展开态（无记录 → undefined，调用方用 defaultOpen）。 */
export function getReasoningOpen(key: string): boolean | undefined {
  const v = openByKey.get(key)
  if (v !== undefined) {
    // 重新插入以保持"最近使用"顺序（Map 迭代顺序 = 插入顺序）
    openByKey.delete(key)
    openByKey.set(key, v)
  }
  return v
}

/** 记录某迭代思考块的展开态。 */
export function setReasoningOpen(key: string, open: boolean): void {
  if (openByKey.has(key)) openByKey.delete(key)
  openByKey.set(key, open)
  while (openByKey.size > MAX_ENTRIES) {
    const oldest = openByKey.keys().next().value
    if (oldest === undefined) break
    openByKey.delete(oldest)
  }
}

/** 思考块展开态的共享 key（live 与 committed 必须用同一个构造函数）。 */
export function reasoningKey(turnID: number | null | undefined, iteration: number): string {
  return `${turnID ?? 0}:${iteration}`
}

/** 测试用：清空（模块级单例，跨用例必须隔离）。 */
export function __resetReasoningOpenState(): void {
  openByKey.clear()
}
