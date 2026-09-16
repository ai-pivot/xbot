/**
 * view_image 注入行 ≠ 用户消息（用户报告 2026-09-16）：
 *
 * web 端用按钮上传图片后，用户自己的消息**变了** —— 正文变成
 * 「📷 以下图片已通过 view_image 工具加载…」，图片地址也从
 * `/api/files/download?key=uploads%2F…`（用户上传的原件）变成
 * `/api/files/viewimg/<uuid>`（注入副本）。
 *
 * 机制（DB 实证 tenant=140480 turn=904）：
 *   1780461 user turn=904  "![IMG_5001.png](/api/files/download?key=uploads%2F4%2F….png&inline=1)激活你的技能"
 *   1780467 user turn=904  "📷 … ![ref_dog.jpg](/api/files/viewimg/2497df58….j…)"        ← view_image 注入
 *   1780477 user turn=904  "📷 … ![dog_chicken.jpg](/api/files/viewimg/9e356d4d….j…"     ← view_image 注入
 *
 * `injectViewImages`（agent/engine_run.go）把图片引用作为**一条新的 user 消息**
 * 持久化，并打上**与用户真实消息相同的 turn_id**（OpenAI tool role 不能带图，
 * user role 是多模态唯一载体）。渲染层每个 turn 只有一个 user 槽位 ⇒ 后来的
 * 注入行顶掉了真实消息。
 *
 * 契约：**一个 turn 的用户消息 = 该 turn 第一条 user 行（用户自己那条）**；
 * 注入行（LLM 侧多模态载体）不得顶替它。
 */

import { MessageStore } from '@/components/agent/messageStore'
import { describe, expect, it } from 'vitest'
import { deriveRows } from './derive'
import { historyToReplaced, rowsToChatMessages } from './integrate'
import { reduce } from './reduce'
import { initialChatState } from './types'
import type { ChatMessage } from '@/types/shared'

const REAL =
  '![IMG_5001.png](/api/files/download?key=uploads%2F4%2F68b355c3-ddb3-4354-aebb-e81f58730600.png&inline=1)激活你的技能'
const INJ1 = '📷 以下图片已通过 view_image 工具加载，可直接进行视觉分析：\n\n![ref_dog.jpg](/api/files/viewimg/2497df58-8339-485d-80ee-5ae2340609a7.jpeg)'
const INJ2 = '📷 以下图片已通过 view_image 工具加载，可直接进行视觉分析：\n\n![dog_chicken.jpg](/api/files/viewimg/9e356d4d-71d5-4dbd-8051-b2670e913300.jpeg)'

function userRow(dbID: number, content: string, turnID: number): ChatMessage {
  return {
    id: `db-${dbID}`,
    role: 'user',
    content,
    turnID,
    dbID,
    iterations: [],
    timestamp: '2026-09-16T00:00:00Z',
    isPartial: false,
    persisted: true,
  }
}

function assistantRow(dbID: number, content: string, turnID: number): ChatMessage {
  return {
    id: `db-${dbID}`,
    role: 'assistant',
    content,
    turnID,
    dbID,
    iterations: [],
    timestamp: '2026-09-16T00:00:00Z',
    isPartial: false,
    persisted: true,
  }
}

/** turn 904 的真实历史（DB id 升序）：真实 user → assistant/tool → 两条注入 user → assistant。 */
const HISTORY: ChatMessage[] = [
  userRow(1780461, REAL, 904),
  assistantRow(1780463, '技能已激活（image-gen）。先把图里的狗和鸡裁出来当参考图：', 904),
  userRow(1780467, INJ1, 904),
  userRow(1780477, INJ2, 904),
  assistantRow(1780478, '两张参考图都对。现在生成合照：', 904),
]

function renderedUserRows(history: ChatMessage[]) {
  const state = reduce(initialChatState('chat-1'), historyToReplaced(history, null))
  return deriveRows(state)
    .filter((r) => r.kind === 'user')
    .map((r) => r.content)
}

describe('view_image 注入行不得顶替用户消息（turn 内多条 user 行）', () => {
  it('渲染出的 user 行 = 用户自己那条（uploads 引用），不是注入行', () => {
    const users = renderedUserRows(HISTORY)
    expect(users).toHaveLength(1)
    expect(users[0]).toBe(REAL)
    expect(users[0]).toContain('/api/files/download?key=uploads%2F4%2F')
    expect(users[0]).not.toContain('view_image 工具加载')
  })

  it('rowsToChatMessages（MessageList 的 props）里 turn 的 user 也是真实那条', () => {
    const state = reduce(initialChatState('chat-1'), historyToReplaced(HISTORY, null))
    const msgs = rowsToChatMessages(deriveRows(state))
    const users = msgs.filter((m) => m.role === 'user').map((m) => m.content)
    expect(users).toHaveLength(1)
    expect(users[0]).toBe(REAL)
  })

  it('续跑（reload/loadMore 增量）后仍然保持真实那条（不被后来的注入顶掉）', () => {
    const s0 = reduce(initialChatState('chat-1'), historyToReplaced(HISTORY.slice(0, 2), null))
    // 增量批次（loadMore 边界 / reload）带来注入行
    const s1 = reduce(s0, historyToReplaced(HISTORY, null))
    const users = deriveRows(s1)
      .filter((r) => r.kind === 'user')
      .map((r) => r.content)
    expect(users).toHaveLength(1)
    expect(users[0]).toBe(REAL)
  })
})

// ── 上游：MessageStore（useChatMessages 的输出，才是状态机的历史输入） ──
describe('MessageStore.mergeHistory — 一个 turn 的 user 行取【最早那条】（用户真实消息）', () => {
  it('注入行不得覆盖用户真实消息（DB 实证 turn 904 三行顺序）', () => {
    const store = new MessageStore()
    store.mergeHistory(HISTORY, { replace: true })
    const users = store.toRows().filter((r) => r.role === 'user')
    expect(users).toHaveLength(1)
    expect(users[0].content).toBe(REAL)
    expect(users[0].content).not.toContain('view_image 工具加载')
    expect(users[0].dbID).toBe(1780461)
  })

  it('分批到达（loadMore 倒序/id 边界跨界）也必须取最早那条，与批次顺序无关', () => {
    // 先到新批（含注入），后到旧批（含真实消息）—— 与 append 顺序相反
    const newer = [userRow(1780467, INJ1, 904), userRow(1780477, INJ2, 904)]
    const older = [userRow(1780461, REAL, 904), assistantRow(1780478, 'reply', 904)]

    const a = new MessageStore()
    a.mergeHistory(newer, { replace: true })
    a.mergeHistory(older)
    expect(a.toRows().filter((r) => r.role === 'user')[0]?.content).toBe(REAL)

    // 正向顺序（先旧批后新批）同样成立
    const b = new MessageStore()
    b.mergeHistory(older, { replace: true })
    b.mergeHistory(newer)
    expect(b.toRows().filter((r) => r.role === 'user')[0]?.content).toBe(REAL)
  })
})
