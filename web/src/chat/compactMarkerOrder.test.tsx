import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { TurnBody } from '@/components/agent/TurnBody'
import { deriveRows } from '@/chat/derive'
import { historyToReplaced, rowsToChatMessages } from '@/chat/integrate'
import { reduce } from '@/chat/reduce'
import { initialChatState } from '@/chat/types'
import { renderWithProviders } from '@/test-utils'
import type { ChatMessage, WebCompaction, WebIteration } from '@/types/shared'

/**
 * 重新设计（2026-09-26 用户：「如果真的是在一个 turn 中间压缩的，应该插在 Iter
 * 中间，跟 Iter 同级渲染，类似 Cursor 的 context summarized」）。
 *
 * 压缩由后端在 LLM 请求前触发 ⇒ 恒在迭代边界 ⇒ 归属它所在的 turn，渲染在
 * **迭代之间**（不是独立的 between-turns 行）。老数据（无法定位迭代位置）回落为
 * standalone 行 —— 前端仍支持（基本兼容）。
 */
function iter(n: number, content: string): WebIteration {
  return { iteration: n, content, reasoning: '', tools: [], toolCount: 0 }
}

describe('turn 内压缩点（迭代之间内联渲染，Cursor 式）', () => {
  it('压缩点渲染在「对应迭代块之后」——afterIteration=1 ⇒ 落在迭代 1 块内、迭代 2 块外', () => {
    const compactions: WebCompaction[] = [
      { afterIteration: 1, content: '[Compacted context]\n\nsummary body' },
    ]
    const { container } = renderWithProviders(
      <TurnBody iterations={[iter(1, 'a'), iter(2, 'b')]} compactions={compactions} turnID={3} />,
    )
    const dividers = container.querySelectorAll('[data-testid="compaction-divider"]')
    expect(dividers.length, '压缩点必须渲染一次').toBe(1)
    // 位置：迭代 1 的块内（其后），迭代 2 的块内没有。
    expect(
      container.querySelector('[data-iter-id="1"] [data-testid="compaction-divider"]'),
      '压缩点必须紧跟在迭代 1 之后',
    ).toBeTruthy()
    expect(
      container.querySelector('[data-iter-id="2"] [data-testid="compaction-divider"]'),
    ).toBeFalsy()
    // Cursor 式文案存在。
    expect(container.textContent).toContain('Context compacted')
  })

  it('afterIteration=0 ⇒ 渲染在第一个迭代之前', () => {
    const compactions: WebCompaction[] = [{ afterIteration: 0, content: '[Compacted context]\n\ns' }]
    const { container } = renderWithProviders(
      <TurnBody iterations={[iter(1, 'a')]} compactions={compactions} turnID={3} />,
    )
    const divider = container.querySelector('[data-testid="compaction-divider"]')
    expect(divider, '压缩点必须存在').toBeTruthy()
    // 不在任何迭代块内（在迭代列表之前）。
    expect(divider!.closest('.iter-block')).toBeNull()
    expect(container.querySelector('[data-iter-id="1"] [data-testid="compaction-divider"]')).toBeFalsy()
  })

  it('无压缩点时完全不渲染分隔（零成本）', () => {
    const { container } = renderWithProviders(
      <TurnBody iterations={[iter(1, 'a'), iter(2, 'b')]} turnID={3} />,
    )
    expect(container.querySelector('[data-testid="compaction-divider"]')).toBeFalsy()
  })

  it('折叠（tool-only run 合并跳号）后压缩点仍落在最近的更早迭代块之后', () => {
    // 迭代 2..3 是连续 tool-only（会被 mergeToolRuns 合并到迭代 2 的块）；
    // 压缩点的 afterIteration=3 在原始列表里存在，但合并后只剩迭代号 2 —— 必须
    // 落到迭代 2 的块之后，绝不丢失。
    const iters: WebIteration[] = [
      iter(1, 'text-1'),
      {
        iteration: 2, content: '', reasoning: '', toolCount: 1,
        tools: [{ name: 'Shell', label: 'Shell', status: 'done', elapsedMs: 1, summary: '', detail: '', args: '{}', toolHints: '' }],
      },
      {
        iteration: 3, content: '', reasoning: '', toolCount: 1,
        tools: [{ name: 'Read', label: 'Read', status: 'done', elapsedMs: 1, summary: '', detail: '', args: '{}', toolHints: '' }],
      },
    ]
    const compactions: WebCompaction[] = [{ afterIteration: 3, content: '[Compacted context]\n\ns' }]
    const { container } = renderWithProviders(
      <TurnBody iterations={iters} compactions={compactions} turnID={3} />,
    )
    expect(container.querySelectorAll('[data-testid="compaction-divider"]').length, '压缩点绝不丢失').toBe(1)
    expect(container.querySelector('[data-iter-id="2"] [data-testid="compaction-divider"]')).toBeTruthy()
  })
})

describe('压缩点穿透历史管线', () => {
  it('HistoryMessage.compactions → ChatMessage.compactions（turn 的 assistant 行）', () => {
    const msgs: ChatMessage[] = [
      { id: 'u3', role: 'user', content: 'turn3 用户消息', iterations: [], timestamp: '', isPartial: false, turnID: 3, dbID: 100 },
      {
        id: 'a3', role: 'assistant', content: 'turn3 回复', iterations: [iter(1, 'a'), iter(2, 'b')],
        timestamp: '', isPartial: false, turnID: 3, dbID: 101,
        compactions: [{ afterIteration: 1, content: '[Compacted context]\n\nsummary' }],
      },
    ]
    let s = initialChatState('chat-1')
    s = reduce(s, historyToReplaced(msgs, null))
    const rows = rowsToChatMessages(deriveRows(s))
    const turn3 = rows.find((r) => r && r.turnID === 3 && r.role === 'assistant')
    expect(turn3, 'turn 3 的 assistant 行必须存在').toBeTruthy()
    expect(turn3!.compactions?.length, '压缩点必须穿透到渲染行').toBe(1)
    expect(turn3!.compactions![0].afterIteration).toBe(1)
  })

  it('用户消息绝不被压缩标记顶掉（P0 不回归）', () => {
    const msgs: ChatMessage[] = [
      { id: 'u4', role: 'user', content: '继续优化到 7ms', iterations: [], timestamp: '', isPartial: false, turnID: 4, dbID: 160 },
      {
        id: 'a4', role: 'assistant', content: 'turn4 回复', iterations: [iter(1, 'x')],
        timestamp: '', isPartial: false, turnID: 4, dbID: 161,
        compactions: [{ afterIteration: 0, content: '[Compacted context]\n\ns' }],
      },
    ]
    let s = initialChatState('chat-1')
    s = reduce(s, historyToReplaced(msgs, null))
    const rows = rowsToChatMessages(deriveRows(s))
    const turn4User = rows.find((r) => r && r.turnID === 4 && r.role === 'user')
    expect(turn4User?.content).toBe('继续优化到 7ms')
  })

  it('老数据兼容：standalone 压缩标记行仍渲染（不进 compactions，不占 turn 槽位）', () => {
    const msgs: ChatMessage[] = [
      { id: 'u3', role: 'user', content: 'turn3 用户消息', iterations: [], timestamp: '', isPartial: false, turnID: 3, dbID: 100 },
      { id: 'a3', role: 'assistant', content: 'turn3 回复', iterations: [iter(1, 'a')], timestamp: '', isPartial: false, turnID: 3, dbID: 101 },
      // 老数据形态：独立的 standalone 压缩标记行（anchor=3）。
      {
        id: 'marker', role: 'user', content: '[Compacted context]\n\nsummary', iterations: [],
        timestamp: '', isPartial: false, turnID: 0, dbID: 150,
        standalone: true, anchorTurnID: 3,
      },
      { id: 'u4', role: 'user', content: '继续', iterations: [], timestamp: '', isPartial: false, turnID: 4, dbID: 160 },
      { id: 'a4', role: 'assistant', content: 'turn4 回复', iterations: [iter(1, 'x')], timestamp: '', isPartial: false, turnID: 4, dbID: 161 },
    ]
    let s = initialChatState('chat-1')
    s = reduce(s, historyToReplaced(msgs, null))
    const rows = rowsToChatMessages(deriveRows(s))
    // 老数据标记行仍然存在（user 行、turnID=0、standalone）。
    const marker = rows.find((r) => r && String(r.content).startsWith('[Compacted context]'))
    expect(marker, '老数据压缩标记必须仍然渲染（不丢信息）').toBeTruthy()
    expect(marker!.turnID).toBe(0)
    // 用户消息不被顶掉。
    expect(rows.find((r) => r && r.turnID === 4 && r.role === 'user')?.content).toBe('继续')
  })

  it('兜底不变量：旧后端把标记绑到后续 turn（turn_id=4、非 standalone）也绝不顶掉用户消息', () => {
    // 部署不同步窗口 / 存量数据：deriveTurnIDs 的历史 bug 把标记绑到了 turn 4。
    // historyToReplaced 的域不变量必须拦下它（路由到 standalone），用户消息保留。
    const msgs: ChatMessage[] = [
      { id: 'u3', role: 'user', content: 'turn3 用户消息', iterations: [], timestamp: '', isPartial: false, turnID: 3, dbID: 100 },
      { id: 'a3', role: 'assistant', content: 'turn3 回复', iterations: [iter(1, 'a')], timestamp: '', isPartial: false, turnID: 3, dbID: 101 },
      // ⚠️ 旧后端形态：标记 role=user、turn_id=4（绑到了用户消息所在的 turn）、无 standalone。
      {
        id: 'marker', role: 'user', content: '[Compacted context]\n\nsummary', iterations: [],
        timestamp: '', isPartial: false, turnID: 4, dbID: 150,
      },
      { id: 'u4', role: 'user', content: '继续优化到 7ms', iterations: [], timestamp: '', isPartial: false, turnID: 4, dbID: 160 },
      { id: 'a4', role: 'assistant', content: 'turn4 回复', iterations: [iter(1, 'x')], timestamp: '', isPartial: false, turnID: 4, dbID: 161 },
    ]
    let s = initialChatState('chat-1')
    s = reduce(s, historyToReplaced(msgs, null))
    const rows = rowsToChatMessages(deriveRows(s))
    // 用户消息必须保留（绝不被标记顶掉）。
    const turn4User = rows.find((r) => r && r.turnID === 4 && r.role === 'user')
    expect(turn4User?.content, '用户消息绝不能被压缩标记顶掉（兜底不变量）').toBe('继续优化到 7ms')
    // 标记仍然渲染（信息不丢），但作为无 turn 的独立行。
    const marker = rows.find((r) => r && String(r.content).startsWith('[Compacted context]'))
    expect(marker, '压缩标记必须仍渲染（不丢信息）').toBeTruthy()
    expect(marker!.turnID, '标记必须是「无 turn」的独立行').toBe(0)
  })

  it('压缩点到达（压缩触发的 reload）必须被 mergeTurnData 吸收，不被幂等重放短路吞掉', () => {
    // 压缩触发的 reload 路径：本地 committed（无 compactions）× incoming committed（有）
    // —— iterations/content **完全相同**（同一数组引用）⇒ 旧 mergeTurnData 会走幂等短路
    // `return cur`，把新到达的压缩点吞掉（内联分隔永不出现）。
    const iters: WebIteration[] = [iter(1, 'a'), iter(2, 'b')]
    const user: ChatMessage = {
      id: 'u3', role: 'user', content: 'turn3 用户消息', iterations: [], timestamp: '',
      isPartial: false, turnID: 3, dbID: 100,
    }
    const assistantNoComp: ChatMessage = {
      id: 'a3', role: 'assistant', content: 'turn3 回复', iterations: iters, timestamp: '',
      isPartial: false, turnID: 3, dbID: 101,
    }
    const assistantWithComp: ChatMessage = {
      ...assistantNoComp,
      compactions: [{ afterIteration: 1, content: '[Compacted context]\n\ns' }],
    }

    let s = initialChatState('chat-1')
    s = reduce(s, historyToReplaced([user, assistantNoComp], null))
    expect(
      rowsToChatMessages(deriveRows(s)).find((r) => r && r.turnID === 3 && r.role === 'assistant')?.compactions,
      '第一次（无压缩点）不得有 compactions',
    ).toBeUndefined()

    // 第二次：同迭代引用 + 同 content，仅多了压缩点 → 必须吸收并重建该 turn。
    s = reduce(s, historyToReplaced([user, assistantWithComp], null))
    const row = rowsToChatMessages(deriveRows(s)).find((r) => r && r.turnID === 3 && r.role === 'assistant')
    expect(row?.compactions?.length, '压缩点必须被 mergeTurnData 吸收（不被并合吞掉）').toBe(1)
    expect(row?.compactions?.[0].afterIteration).toBe(1)
  })
})
