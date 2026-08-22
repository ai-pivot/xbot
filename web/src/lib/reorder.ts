/**
 * 拖拽重排的纯数组计算 —— SidebarSectionStack（左栏 section）与
 * RightActivityBar（右栏图标）共用。
 *
 * 返回把 src 插到 target 前/后得到的新顺序；若结果与当前顺序相同（no-op，
 * 如拖回原位）返回 null —— 调用方据此隐藏插入线并跳过持久化。
 */
export function computeReorder(
  ids: string[],
  src: string,
  targetId: string,
  before: boolean,
): string[] | null {
  const without = ids.filter((x) => x !== src)
  let insertAt = without.indexOf(targetId)
  if (insertAt === -1) return null
  if (!before) insertAt += 1
  const next = [...without.slice(0, insertAt), src, ...without.slice(insertAt)]
  return next.join(' ') === ids.join(' ') ? null : next
}
