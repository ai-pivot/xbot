/**
 * gapReloadGate —— 「缺口 ⇒ 权威重载」去重闸门的判别力测试。
 *
 * 背景（P0，2026-10-18 审查）：旧实现（`AgentPanel` 内联）只比 `gapReloadToken` 数值，
 * 而 `reset()` 会把 token 归零、权威 hydration 又会把**同一形状**的缺口重新算出来 ⇒
 * ① 每个 gap 多打一次 reload；② token 0→1→0→1 振荡 ⇒ **reset+reload 自激循环**。
 *
 * 判据（mutation 自证）：
 *  · 把 `token <= seen.token` 改回 `token === seen.token` ⇒ ② 必红（reset 后被当新 gap）；
 *  · 删掉 `sig === seen.sig` 的短路 ⇒ ① 必红（同形状跨 reset 再现时再次重载）；
 *  · 把「新 key 只记录」改成「直接重载」⇒ ③ 必红。
 */
import { describe, expect, it } from 'vitest'

import { gateGapReload, type GapReloadSeen } from './gapReloadGate'

const KEY = 'web:chat-A:'
const SIG = '445:liveAheadAt1001'

/** 按闸门推进一条序列（返回每次是否 reload，并保持记录）。 */
function run(events: Array<{ token: number; sig: string; key?: string }>): boolean[] {
  let seen: GapReloadSeen = { key: KEY, sig: '', token: 0 }
  return events.map((e) => {
    const d = gateGapReload(seen, { key: e.key ?? KEY, token: e.token, sig: e.sig })
    seen = d.next
    return d.reload
  })
}

describe('gateGapReload —— 缺口重载去重（跨 reset 幂等）', () => {
  it('① 首个 gap ⇒ 重载一次；同形状再现（token 再次变大）⇒ 不再重载', () => {
    const r = run([
      { token: 0, sig: '' },       // 首帧无缺口（记录）
      { token: 1, sig: SIG },      // 新缺口 ⇒ reload
      { token: 2, sig: SIG },      // 同形状（并发 effect / 重复计算）⇒ 不 reload
    ])
    expect(r).toEqual([false, true, false])
  })

  it('★ ② reset 把 token 归零不得被当成新缺口；reset 后同形状再现也不得重复重载', () => {
    const r = run([
      { token: 1, sig: SIG },  // 新缺口 ⇒ reload
      { token: 0, sig: '' },   // reset()：token 归零、sig 清空
      { token: 1, sig: SIG },  // 权威 hydration 重新算出**同一形状** ⇒ 必须不 reload
      { token: 0, sig: '' },   // 又 reset
      { token: 1, sig: SIG },  // 再同形状 ⇒ 仍不 reload（防自激循环）
    ])
    expect(r).toEqual([true, false, false, false, false])
  })

  it('③ 洞被补上后换了一个洞（形状不同）⇒ 必须重载', () => {
    const r = run([
      { token: 1, sig: SIG },
      { token: 2, sig: '445:liveAheadAt1501' },
      { token: 3, sig: '445:gap41-51' },
    ])
    expect(r).toEqual([true, true, true])
  })

  it('④ 换会话（key 变化）⇒ 只记录、不重载（新面板首帧语义）', () => {
    const r = run([
      { token: 1, sig: SIG },
      { token: 1, sig: SIG, key: 'cli:chat-B:' },
      { token: 2, sig: SIG, key: 'cli:chat-B:' },
    ])
    expect(r).toEqual([true, false, false])
  })

  it('⑤ 无形状信息（sig=\'\'）⇒ 退化为「token 增大就重载」（不引入永不重载的死路）', () => {
    const r = run([
      { token: 1, sig: '' },
      { token: 2, sig: '' },
    ])
    expect(r).toEqual([true, true])
  })

  it('⑥ 记录必须带出 sig（去重基准）—— 否则 reset 后无法认出同形状', () => {
    const d = gateGapReload({ key: KEY, sig: '', token: 0 }, { key: KEY, token: 1, sig: SIG })
    expect(d.reload).toBe(true)
    expect(d.next).toEqual({ key: KEY, sig: SIG, token: 1 })
  })
})
