import { describe, expect, it } from 'vitest'
import { loadMoreScrollDelta } from './MessageList'

/**
 * ⛔ P0 回归守护（用户 2026-09-21 报告）：
 * 「向上滚动触发加载更多后视角会被移动到最底下」。
 *
 * 根因：翻页锚定补偿量曾用 `ΔtotalSize`。总高**也包含视口下方**的增长（正在流式的
 * turn 持续输出、新 turn 到来）⇒ 每长一像素就把 `scrollTop` 往下推一像素 ⇒ 视口被
 * 一路推到最底（且 `deadline` 写了却从没被读过，快照无限期保留 ⇒ 永不停止）。
 *
 * 正解：补偿量 = **锚点行**（prepend 前的首个可见行）的 offset 位移 —— 只有"上方新增"
 * 会改变它。下面三条用例把策略钉死；把实现换回 `ΔtotalSize` ⇒ 第 2 条必红。
 */
describe('loadMore 锚定补偿：只认锚点位移，绝不被下方增长推动', () => {
  it('上方 prepend（锚点被推下去）⇒ 等量下移视口（用户看不到跳动）', () => {
    // prepend 前锚点 offset=1000；prepend 之后（上方多出 3000px）⇒ 4000
    expect(loadMoreScrollDelta(1000, 4000)).toBe(3000)
  })

  it('下方增长（totalSize 从 5000→9000，锚点 offset 不变）⇒ 补偿 0（视口不动）', () => {
    // 旧实现用 ΔtotalSize = 4000 ⇒ 会把视口往下推 4000px（这就是"被移动到最底下"）。
    expect(loadMoreScrollDelta(1000, 1000)).toBe(0)
  })

  it('上方内容反而变矮（实测修正）⇒ 返回负值（调用方据此只更新基准、不反向补偿）', () => {
    expect(loadMoreScrollDelta(4000, 3000)).toBe(-1000)
  })
})
