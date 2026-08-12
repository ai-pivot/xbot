import { describe, expect, it } from 'vitest'

import { MessageStore, mergeIterations, type LiveState } from '@/components/agent/messageStore'
import type { ChatMessage, WebIteration } from '@/types/shared'

function iter(n: number, thinking = `t${n}`): WebIteration {
  return { iteration: n, thinking, reasoning: '', tools: [], toolCount: 0 }
}

function user(id: string, content: string, turnID: number, over: Partial<ChatMessage> = {}): ChatMessage {
  return { id, role: 'user', content, iterations: [], timestamp: '', isPartial: false, turnID, persisted: true, ...over }
}

function liveState(turnID: number, over: Partial<LiveState> = {}): Partial<LiveState> {
  return { turnID, ...over }
}

// ── 基本状态迁移 ──
describe('MessageStore — 基本 turn 生命周期', () => {
  it('streaming 中：user + live 行（isPartial）', () => {
    const s = new MessageStore()
    s.setUser(360, user('u1', 'hello', 360))
    s.updateLive(360, liveState(360, { iterations: [iter(1)], lastIter: 1 }))
    const rows = s.toRows()
    expect(rows).toHaveLength(2)
    expect(rows[0].role).toBe('user')
    expect(rows[1]).toMatchObject({ id: 'turn-360-live', role: 'assistant', isPartial: true, turnID: 360 })
    expect(rows[1].iterations).toHaveLength(1)
  })

  it('text 事件：live → assistant 状态迁移（同对象，不产生第二行）', () => {
    const s = new MessageStore()
    s.setUser(360, user('u1', 'hello', 360))
    s.updateLive(360, liveState(360, { iterations: [iter(1)], content: 'partial' }))
    s.commitAssistant(360, 'final reply', [iter(1)], 120412)
    const rows = s.toRows()
    expect(rows).toHaveLength(2) // user + assistant，无 live
    expect(rows[1]).toMatchObject({ id: 'seq-120412', role: 'assistant', isPartial: false, turnID: 360, content: 'final reply' })
    expect(s.hasLive(360)).toBe(false)
  })

  it('commit 时 content 为空 → 回退 live 累积文本', () => {
    const s = new MessageStore()
    s.updateLive(360, liveState(360, { content: 'streamed text', iterations: [iter(1)] }))
    s.commitAssistant(360, '', [iter(1)])
    expect(s.toRows()[0]).toMatchObject({ content: 'streamed text', role: 'assistant' })
  })

  it('排序：多个 turn 乱序插入 → toRows 严格 (turnID, role)', () => {
    const s = new MessageStore()
    s.setUser(358, user('u358', 'a', 358))
    s.commitAssistant(358, 'r358', [iter(1)])
    s.setUser(360, user('u360', 'c', 360))
    s.commitAssistant(360, 'r360', [iter(1)])
    s.setUser(359, user('u359', 'b', 359))
    s.commitAssistant(359, 'r359', [iter(1)])
    const tids = s.toRows().map((r) => r.turnID)
    expect(tids).toEqual([358, 358, 359, 359, 360, 360])
  })
})

// ── 跨 turn 迭代号碰撞（本轮 P0 根因回归）──
describe('MessageStore — 跨 turn 迭代号碰撞（turn-vanish P0）', () => {
  it('两个 turn 各有 iterations [1,2] → 两行都渲染（结构上不可能误杀）', () => {
    const s = new MessageStore()
    // turn 359 committed assistant（[interrupted]，2 迭代）
    s.commitAssistant(359, '[interrupted]', [iter(1), iter(2)])
    // turn 360 live 也达到 2 迭代
    s.setUser(360, user('u360', '继续', 360))
    s.updateLive(360, liveState(360, { iterations: [iter(1, 'turn360-i1'), iter(2, 'turn360-i2')], lastIter: 2 }))
    const rows = s.toRows()
    // turn 359 assistant + turn 360 user + turn 360 live —— 全部渲染
    expect(rows).toHaveLength(3)
    expect(rows[0].role).toBe('assistant') // turn 359 committed
    expect(rows[1].role).toBe('user')
    expect(rows[2].id).toBe('turn-360-live')
    expect(rows[2].iterations.map((i) => i.iteration)).toEqual([1, 2])
  })

  it('turnID=0 的 legacy committed 与 live 不同 turn → 各自渲染（不 dedup）', () => {
    const s = new MessageStore()
    s.addLegacy(user('legacy-1', '', 0, { role: 'assistant', persisted: true }))
    s.updateLive(360, liveState(360, { iterations: [iter(1)], content: 'live text' }))
    const rows = s.toRows()
    expect(rows).toHaveLength(2)
    expect(rows[1].id).toBe('turn-360-live')
  })
})

// ── cancel 冻结 ──
describe('MessageStore — cancel 冻结', () => {
  it('cancel 后 assistant=[interrupted] + frozen live → 合并渲染（content 用 live）', () => {
    const s = new MessageStore()
    s.setUser(360, user('u360', '继续', 360))
    s.updateLive(360, liveState(360, { content: 'streamed partial', iterations: [iter(1)] }))
    s.freeze(360)
    s.commitAssistant(360, '[interrupted]', [iter(1)])
    const rows = s.toRows()
    expect(rows).toHaveLength(2) // user + 合并后的 assistant（live 不再独立行）
    expect(rows[1].content).toBe('streamed partial') // live 内容保留
    expect(rows.some((r) => r.id === 'turn-360-live')).toBe(false)
  })

  it('endTurn（session idle）清理冻结 live，但 assistant 保留', () => {
    const s = new MessageStore()
    s.updateLive(360, liveState(360, { content: 'x', iterations: [iter(1)] }))
    s.freeze(360)
    s.commitAssistant(360, '[interrupted]', [iter(1)])
    s.endTurn(360)
    expect(s.toRows()).toHaveLength(1)
    expect(s.toRows()[0].content).toBe('[interrupted]')
  })
})

// ── 乐观 user 绑定 ──
describe('MessageStore — optimistic user 绑定', () => {
  it('turn_started 把最后一条未持久化 user 绑定到该 turn', () => {
    const s = new MessageStore()
    s.setUser(0, user('opt-1', '你好', 0, { persisted: false }))
    s.beginTurn(360)
    const rows = s.toRows()
    expect(rows).toHaveLength(1)
    expect(rows[0].turnID).toBe(360)
    expect(rows[0].id).toBe('opt-1')
  })

  it('多条 pending user → 只绑定最后一条（最新发送）', () => {
    const s = new MessageStore()
    s.setUser(0, user('opt-1', '第一条', 0, { persisted: false }))
    s.setUser(0, user('opt-2', '第二条', 0, { persisted: false }))
    s.beginTurn(361)
    const rows = s.toRows()
    // 最新一条（opt-2）绑定到 turn 361；前一条留在 pending（底部）
    expect(rows.find((r) => r.id === 'opt-2')?.turnID).toBe(361)
    expect(rows[rows.length - 1].id).toBe('opt-1') // pending 仍在底部渲染
  })
})

// ── legacy 行 ──
describe('MessageStore — legacy 行', () => {
  it('无 turnID persisted 行 → 顶部', () => {
    const s = new MessageStore()
    s.addLegacy(user('legacy', '旧消息', 0))
    s.setUser(360, user('u360', '新', 360))
    s.commitAssistant(360, 'r', [iter(1)])
    const rows = s.toRows()
    expect(rows[0].id).toBe('legacy')
    expect(rows[1].turnID).toBe(360)
  })
})

// ── 迟到事件路由（跨 turn 竞态）──
describe('MessageStore — 迟到事件路由', () => {
  it('旧 turn 的 text 在 turn_started(N+1) 后到达 → 只更新旧 turn slot，不污染新 turn', () => {
    const s = new MessageStore()
    s.setUser(359, user('u359', 'a', 359))
    s.beginTurn(360) // 新 turn 开始
    s.setUser(360, user('u360', 'b', 360))
    // 迟到的 turn 359 text
    s.commitAssistant(359, 'turn359 reply', [iter(1)])
    const rows = s.toRows()
    expect(rows).toHaveLength(3) // u359 + a359 + u360（360 无 assistant/live）
    expect(rows.find((r) => r.role === 'assistant' && r.turnID === 359)?.content).toBe('turn359 reply')
    expect(rows.filter((r) => r.turnID === 360)).toHaveLength(1) // 只有 user
  })
})

// ── reload 回填 ──
describe('MessageStore — reload 回填', () => {
  it('mergeHistory 回填 dbID/persisted，不覆盖进行中的 live', () => {
    const s = new MessageStore()
    s.setUser(360, user('opt', 'hi', 360, { persisted: false }))
    s.updateLive(360, liveState(360, { content: 'streaming...', iterations: [iter(1)] }))
    // reload 返回 DB 版本（带 dbID）
    s.mergeHistory([
      user('db-1', 'hi', 360, { dbID: 100, persisted: true }),
      { id: 'db-2', role: 'assistant', content: 'partial db', iterations: [], timestamp: '', isPartial: false, turnID: 360, persisted: true, dbID: 101 },
    ])
    const rows = s.toRows()
    // user 回填 dbID；assistant 不覆盖 live（live 权威）→ 仍输出 live 行
    expect(rows.find((r) => r.role === 'user')?.dbID).toBe(100)
    expect(rows.some((r) => r.id === 'turn-360-live')).toBe(true)
  })

  it('mergeHistory 对已提交 turn 回填 DB 版本', () => {
    const s = new MessageStore()
    s.commitAssistant(360, 'optimistic', [iter(1)], 5)
    // reload（replace）→ DB 快照权威：content 覆盖本地提交
    s.mergeHistory([{ id: 'db-9', role: 'assistant', content: 'db version', iterations: [iter(1), iter(2)], timestamp: '', isPartial: false, turnID: 360, persisted: true, dbID: 9 }], { replace: true })
    const assistant = s.toRows().find((r) => r.role === 'assistant')
    expect(assistant?.dbID).toBe(9)
    expect(assistant?.content).toBe('db version')
    expect(assistant?.iterations).toHaveLength(2)
  })
})

// ── AskUser resume ──
describe('MessageStore — AskUser resume', () => {
  it('beginTurn(resume) 保留 iterations，只清流式字段', () => {
    const s = new MessageStore()
    s.setUser(360, user('u360', 'q', 360))
    s.updateLive(360, liveState(360, { content: 'old text', iterations: [iter(1)], lastIter: 1 }))
    s.beginTurn(360, { resume: true })
    const live = s.getLive(360)
    expect(live?.iterations).toHaveLength(1) // 保留
    expect(live?.content).toBe('') // 流式清空
  })
})

// ── beginTurn 自动 commit 旧 live ──
describe('MessageStore — beginTurn 自动 commit 旧 live', () => {
  it('turn_started(N+1) 时旧 turn 的未提交 live 被 commit（text 丢失兜底）', () => {
    const s = new MessageStore()
    s.setUser(359, user('u359', 'a', 359))
    s.updateLive(359, liveState(359, { content: 'unfinalized live', iterations: [iter(1)], eventSeq: 99 }))
    s.beginTurn(360)
    const rows = s.toRows()
    // turn 359：user + committed assistant（live 内容固化）
    expect(rows.find((r) => r.role === 'assistant' && r.turnID === 359)?.content).toBe('unfinalized live')
    expect(rows.some((r) => r.id === 'turn-359-live')).toBe(false)
  })
})

// ── subscribe/notify（Step 3 修复：useChatMessages 感知 live 更新）──
describe('MessageStore — subscribe/notify', () => {
  it('notifies listeners on any mutation（含 live 更新）', () => {
    const s = new MessageStore()
    let notified = 0
    const off = s.subscribe(() => { notified += 1 })
    s.setUser(360, user('u360', 'hi', 360))
    expect(notified).toBe(1)
    s.updateLive(360, liveState(360, { content: 'stream' }))
    expect(notified).toBe(2) // live 更新也通知
    s.commitAssistant(360, 'reply', [iter(1)])
    expect(notified).toBe(3)
    off()
    s.clear()
    expect(notified).toBe(3) // 取消订阅后不再通知
  })
})

// ── mergeIterations ──
describe('mergeIterations', () => {
  it('按迭代号合并，保留非空 thinking', () => {
    const a = [iter(1, 't1'), iter(2, '')]
    const b = [iter(1, ''), iter(2, 't2-full')]
    const merged = mergeIterations(a, b)
    expect(merged.map((i) => i.iteration)).toEqual([1, 2])
    expect(merged[0].thinking).toBe('t1')
    expect(merged[1].thinking).toBe('t2-full')
  })
})

// ── 补充 API（Step 2 接入 useChatMessages 需要）──
describe('MessageStore — patchUserById / removeById / loadMore 合并', () => {
  it('patchUserById 回填 pending optimistic user（REST 响应）', () => {
    const s = new MessageStore()
    s.setUser(0, user('opt-1', 'hi', 0, { persisted: false, requestID: 'r1' }))
    s.patchUserById('opt-1', { persisted: true, turnID: 360, dbID: 123, sending: false })
    const rows = s.toRows()
    expect(rows[0]).toMatchObject({ turnID: 360, dbID: 123, persisted: true, sending: false })
  })

  it('patchUserById 回填已绑定槽位的 user', () => {
    const s = new MessageStore()
    s.setUser(360, user('u360', 'hi', 360, { persisted: false }))
    s.patchUserById('u360', { persisted: true, dbID: 7 })
    expect(s.toRows()[0].dbID).toBe(7)
  })

  it('removeById 删除 pending user（sendMessage 失败）', () => {
    const s = new MessageStore()
    s.setUser(0, user('opt-1', 'hi', 0, { persisted: false }))
    s.removeById('opt-1')
    expect(s.toRows()).toHaveLength(0)
  })

  it('mergeHistory 对已提交 assistant 合并迭代（loadMore 边界 union）', () => {
    const s = new MessageStore()
    s.commitAssistant(360, 'reply', [iter(1)])
    // loadMore 返回同一 turn 的旧批次（iter 0？实际是同一 turn 拆批的中间迭代）
    s.mergeHistory([{ id: 'db-5', role: 'assistant', content: 'reply', iterations: [iter(2)], timestamp: '', isPartial: false, turnID: 360, persisted: true, dbID: 5 }])
    const assistant = s.toRows().find((r) => r.role === 'assistant')
    expect(assistant?.iterations.map((i) => i.iteration)).toEqual([1, 2]) // union
  })
})
