/**
 * Tests for LiveIteration (Spec A §2 — typewriter cursor position).
 *
 * Verifies:
 *  - Streaming content renders with streaming-content class
 *  - Typewriter cursor (CSS ::after) is applied when streaming
 *  - No streaming-content class when not streaming
 *  - SubAgent tree renders when subAgents present
 */
import { act } from 'react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { LiveIteration } from '@/components/agent/LiveIteration'
import { renderWithProviders } from '@/test-utils'
import type { ProgressSnapshot } from '@/types/shared'

function makeSnapshot(overrides: Partial<ProgressSnapshot> = {}): ProgressSnapshot {
  return {
    eventSeq: 0,
    phase: 'thinking',
    iteration: 1,
    streamContent: '',
    content: '',
    reasoningStreamContent: '',
    streaming: true,
    activeTools: [],
    completedTools: [],
    iterationHistory: [],
    streamingTools: [],
    genuiContent: '',
    lastIter: 0,
    lastReasoning: '',
    todos: [],
      goal: null,
    subAgents: [],
    tokenUsage: null,
    turnID: 0,
    ...overrides,
  }
}

describe('LiveIteration — typewriter cursor', () => {
  it('renders streaming content with streaming-content class when streaming', () => {
    const snapshot = makeSnapshot({
      streamContent: 'Hello world',
      streaming: true,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    const streamingDiv = container.querySelector('.streaming-content')
    expect(streamingDiv).not.toBeNull()
    // Typewriter starts empty; content appears after the 50ms interval tick.
    // The test verifies the CSS class is applied, not the full text (which
    // depends on timer advancement).
  })

  it('does NOT apply streaming-content class when not streaming', () => {
    const snapshot = makeSnapshot({
      streamContent: 'Final text',
      streaming: false,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    const streamingDiv = container.querySelector('.streaming-content')
    expect(streamingDiv).toBeNull()
  })

  it('does not render streaming content section when streamContent is empty (thinking phase)', () => {
    const snapshot = makeSnapshot({
      streamContent: '',
      reasoningStreamContent: 'thinking about something',
      streaming: true,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    const streamingDiv = container.querySelector('.streaming-content')
    expect(streamingDiv).toBeNull()
  })

  it('sweeps the in-progress thought character count with catch-up (smooth, not jump)', () => {
    vi.useFakeTimers()
    try {
      const snapshot = makeSnapshot({
        reasoningStreamContent: 'thinking about something',
        streaming: true,
        phase: 'thinking',
      })
      const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)

      const full = snapshot.reasoningStreamContent.length // 24
      const extractCount = () => {
        const txt = container.querySelector<HTMLElement>('.sweep-text')?.textContent ?? ''
        const m = txt.match(/\d+/)
        return m ? Number(m[0]) : NaN
      }

      // 初始：typewriter 从 0 开始，数字尚未到达完整长度（不再是跳变到 full）
      const initial = extractCount()
      expect(initial).toBeLessThan(full)

      // 追赶中途（gap/3 per 50ms）：数字增长但尚未追满
      act(() => { vi.advanceTimersByTime(100) })
      const mid = extractCount()
      expect(mid).toBeGreaterThan(initial)
      expect(mid).toBeLessThan(full)

      // 追满：最终收敛到完整长度
      act(() => { vi.advanceTimersByTime(2000) })
      expect(extractCount()).toBe(full)

      expect(container.querySelectorAll('.sweep-text')).toHaveLength(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it.each(['pending', 'generating', 'running'] as const)(
    'hides the reasoning sweep while a %s tool is in progress',
    (status) => {
      const snapshot = makeSnapshot({
        reasoningStreamContent: 'thinking about something',
        streamingTools: [{
          name: 'Read',
          label: 'Read',
          status,
          elapsedMs: 0,
          summary: '',
          detail: '',
          args: '',
          toolHints: '',
        }],
      })
      const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)

      expect(container.querySelectorAll('.sweep-text')).toHaveLength(1)
      expect(container.querySelector('.sweep-text')).toHaveTextContent('Read')
    },
  )

  it('renders SubAgent tree when subAgents present', () => {
    const snapshot = makeSnapshot({
      streamContent: '',
      streaming: false,
      subAgents: [
        { role: 'explore', instance: 'sub-1', status: 'running', desc: 'searching' },
      ],
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    expect(container.textContent).toContain('explore:sub-1')
    expect(container.textContent).toContain('searching')
  })

  it('returns null when no content to show', () => {
    const snapshot = makeSnapshot({
      streamContent: '',
      reasoningStreamContent: '',
      streaming: true,
      phase: '',
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    // Should render nothing meaningful (empty)
    expect(container.querySelector('.streaming-content')).toBeNull()
  })

  it('does NOT filter out running activeTools that share name+label with a completed iteration', () => {
    // BUG: LiveIteration filtered ALL tools (including activeTools) by name+label
    // against iterationHistory. If the same tool (e.g. Shell) appeared in both a
    // completed iteration and the current running iteration, the running tool was
    // filtered out — making it disappear from the UI.
    const snapshot = makeSnapshot({
      streaming: true,
      phase: 'tool_exec',
      iteration: 2,
      // Iteration 1 is completed — has Shell(done)
      iterationHistory: [{
        iteration: 1,
        content: '',
        reasoning: '',
        tools: [{
          name: 'Shell',
          label: 'Shell echo hello',
          status: 'done',
          elapsedMs: 100,
          summary: '',
          detail: '',
          args: '',
          toolHints: '',
        }],
        toolCount: 1,
      }],
      // Current iteration 2 — Shell is running (same name+label!)
      activeTools: [{
        name: 'Shell',
        label: 'Shell echo hello',
        status: 'running',
        elapsedMs: 0,
        summary: '',
        detail: '',
        args: '',
        toolHints: '',
        iteration: 2,
      }],
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    // The running Shell must NOT be filtered out — it renders with a SweepText
    // (the animated "running" indicator). Check for the tool name + sweep.
    expect(container.textContent).toContain('Shell')
    // SweepText is shown when a tool is running (status === 'running')
    expect(container.querySelector('.sweep-text')).not.toBeNull()
  })

  it('filters stale generating tools from COMPLETED iterations (catchup gap residue)', () => {
    // BUG: catchup gap 后旧迭代的 generating tool（streaming_tools 事件无迭代
    // 号、或带旧迭代号）残留在 store.streamingTools。LiveIteration 之前对
    // streamingTools 不做迭代过滤（currentActive/completedTools 都有），
    // 旧工具错误渲染在最新迭代上，直到最新迭代真正的 tool 出现才被替换
    // （用户报告："过去的 generating 状态可能错误的在最新迭代上渲染"）。
    const snapshot = makeSnapshot({
      streaming: true,
      phase: 'tool_exec',
      iteration: 2,
      lastIter: 2,
      // Iteration 1 completed — the stale generating tool belongs to it
      iterationHistory: [{
        iteration: 1,
        content: '',
        reasoning: '',
        tools: [{
          name: 'Bash',
          label: 'Bash run build',
          status: 'done',
          elapsedMs: 100,
          summary: '',
          detail: '',
          args: '',
          toolHints: '',
          iteration: 1,
        }],
        toolCount: 1,
      }],
      // Stale generating tool from iteration 1 (catchup gap residue)
      streamingTools: [{
        name: 'Bash',
        label: 'Bash run build',
        status: 'generating',
        elapsedMs: 0,
        summary: '',
        detail: '',
        args: '',
        toolHints: '',
        iteration: 1,
      }],
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    // The stale generating tool from a completed iteration must NOT render
    expect(container.textContent).not.toContain('Bash')
  })

  it('keeps CURRENT-iteration generating tools visible', () => {
    // 当前迭代（iteration 2）的 generating tool 必须渲染（不被误过滤）
    const snapshot = makeSnapshot({
      streaming: true,
      phase: 'tool_exec',
      iteration: 2,
      lastIter: 2,
      iterationHistory: [{
        iteration: 1,
        content: '',
        reasoning: '',
        tools: [{
          name: 'Bash',
          label: 'Bash old',
          status: 'done',
          elapsedMs: 100,
          summary: '',
          detail: '',
          args: '',
          toolHints: '',
          iteration: 1,
        }],
        toolCount: 1,
      }],
      streamingTools: [{
        name: 'Read',
        label: 'Read main.go',
        status: 'generating',
        elapsedMs: 0,
        summary: '',
        detail: '',
        args: '',
        toolHints: '',
        iteration: 2,
      }],
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    expect(container.textContent).toContain('Read')
  })
})

describe('LiveIteration thinking placeholder (reuses ShimmerThinking — iteration boundary)', () => {
  it('shows the EXISTING thinking placeholder at a NON-first iteration boundary (prev iter done, next not arrived)', () => {
    // liveMessage is non-null here → MessageList's busy placeholder is
    // suppressed. Reusing ShimmerThinking keeps the "思考中…" visible during
    // the boundary wait (user: "之前那个思考中有些情况没显示"). Requires a
    // predecessor iteration (iterationHistory non-empty) — the FIRST iteration
    // is special: busy placeholder covers the pre-first-iter window.
    // ⚠️ `iteration` 必须显式给出**下一个**迭代号（2）：边界态 = 迭代 1 已完成、
    // 迭代 2 在飞但尚无内容。默认值 1 表示"在飞的就是那个已渲染完的迭代 1" ——
    // 那种自相矛盾的状态现在**故意**不再显示占位符（见下一条用例）。
    const { container } = renderWithProviders(
      <LiveIteration
        progress={makeSnapshot({
          iteration: 2,
          lastIter: 2,
          iterationHistory: [{ iteration: 1, content: 't1', reasoning: '', tools: [], toolCount: 0 }],
        })}
      />,
    )
    expect(container.textContent).toMatch(/思考中|thinking/)
  })

  it('does NOT show the placeholder when the in-flight iteration is already rendered as history (user 2026-09-20)', () => {
    // ⛔ 用户报告（截图：「思考 15175 字」下方同时出现「思考中…」）：
    // 「思考中和思考 stream 明显不可能同时存在才对」。
    // 迭代边界时后端当前迭代号仍等于刚 commit 的迭代（live.iter === maxCompleted）
    // ⇒ 该迭代已被 TurnBody 渲染成历史块，此处再画「思考中…」就是同一个迭代
    // "既已完成又在思考"。判据 `liveIterationInFlight` 与 MessageList 的 busy
    // 占位符共用（互斥 ⇒ 恰好一个指示器；此状态下两者都不渲染）。
    const { container } = renderWithProviders(
      <LiveIteration
        progress={makeSnapshot({
          iteration: 1,
          lastIter: 1,
          iterationHistory: [{ iteration: 1, content: '', reasoning: '思考 15175 字', tools: [], toolCount: 0 }],
        })}
      />,
    )
    expect(container.textContent).not.toMatch(/思考中|thinking/)
  })

  it('renders ShimmerThinking for the FIRST iteration (iterationHistory empty — M4: live row exists so MessageList busy placeholder is suppressed)', () => {
    // REPRO（切换会话后新 agent turn 完全空白，不渲染思考中）：
    // M4 架构下 turn_started 立即创建 live turn（EMPTY_LIVE streaming=true）
    // → deriveRows 输出 live 行（isPartial）→ MessageList liveId 非 null 且
    // 最后一行是 live assistant（非 user）→ busy placeholder 条件
    // （liveId===null || rows 最后是 user）失败 → 不渲染。旧代码此处也
    // return null（第一迭代 iterationHistory 空）→ 两个指示器都不渲染 →
    // 完全空白。修复：第一迭代空内容 + streaming → 渲染 ShimmerThinking。
    const { container } = renderWithProviders(
      <LiveIteration progress={makeSnapshot({ lastIter: 1 })} />,
    )
    expect(container.textContent).toMatch(/思考中|thinking/)
  })

  it('renders ShimmerThinking in the pre-iteration phase (lastIter=0 — turn just started, no SSE delta yet)', () => {
    // turn_started 刚到、第一个 stream/iteration 事件未到：EMPTY_LIVE 快照
    // （lastIter=0，无任何内容）。live 行已存在 → busy placeholder 不渲染
    // （条件 3 失败）→ 此处必须渲染思考中，否则空白。
    const { container } = renderWithProviders(
      <LiveIteration progress={makeSnapshot({ lastIter: 0 })} />,
    )
    expect(container.textContent).toMatch(/思考中|thinking/)
  })

  it('does NOT render the thinking placeholder while compressing (single status indicator)', () => {
    // REPRO（用户报告截图：`thinking…` 与 `Compressing context…` 同时渲染）：
    // phase='compressing' 的压缩指示器由 AssistantMessage / MessageList 渲染；
    // 而压缩期间 streaming=true 且无内容 → LiveIteration 的空内容分支同样渲染
    // ShimmerThinking（"思考中…"）→ 两个状态指示器上下堆叠，看起来像 bug。
    // 不变量：每个状态下有且只有一个状态指示器 —— 压缩期间归压缩指示器。
    const { container } = renderWithProviders(
      <LiveIteration progress={makeSnapshot({ phase: 'compressing', streaming: true })} />,
    )
    expect(container.textContent).not.toMatch(/思考中|thinking/)
  })

  it('returns null when the turn is not streaming (ended — committed reply replaces the row)', () => {
    const { container } = renderWithProviders(
      <LiveIteration
        progress={makeSnapshot({
          lastIter: 2,
          streaming: false,
          iterationHistory: [{ iteration: 1, content: 't1', reasoning: '', tools: [], toolCount: 0 }],
        })}
      />,
    )
    expect(container.textContent).not.toMatch(/思考中|thinking/)
  })
})

// ─── 已渲染迭代的重复副本守卫（2026-10-02 P0 用户截图实证）─────────────────────
// 「一个 iter 重复渲染两次，甚至第一次渲染没 tool，一个非结尾的 iter 不可能没 tool」
// ——迟到/重放的流式帧（stream 无状态合帧、无 seq gate）让 live 持有**更早的已提交
// 迭代**的内容；旧判据只对比**最后一个**已完成迭代 ⇒ 比较失败 ⇒ 同一段文本在历史块
// （带 pill）与 live 块（无 pill）各渲染一份。
// 修复：live 的内容与**任意**已完成迭代相同 ⇒ 抑制（内容匹配，非迭代号比较——号可能
// 滞后于内容，turnBodyLiveDedup 用例 2 钉死反例）。
describe('LiveIteration — 已渲染迭代的重复副本守卫（内容匹配全部历史）', () => {
  const hist = (n: number, content = '', reasoning = '') => ({
    iteration: n, content, reasoning, tools: [], toolCount: 0,
  })

  it('★ live 持有【更早的已提交迭代】的内容（非最后一个）⇒ 抑制，整块不渲染', () => {
    const snapshot = makeSnapshot({
      iteration: 3,
      streamContent: 'X 文本', // == 迭代 1 的内容（更早，不是最后一个 'Y 文本'）
      streaming: true,
      iterationHistory: [hist(1, 'X 文本'), hist(2, 'Y 文本')] as never,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    // mutation：把判据改回「只对比最后一个」（lastIter.content）⇒ 本条必红（'X 文本' 重复渲染）。
    // （占位符「思考中…」可能存在——迭代 3 确实在飞，那不是重复副本。）
    expect(container.textContent).not.toContain('X 文本')
  })

  it('同源：live 的 reasoning 与【更早的】已提交迭代相同 ⇒ 抑制', () => {
    const snapshot = makeSnapshot({
      iteration: 3,
      reasoningStreamContent: 'R-old',
      lastReasoning: 'R-old',
      streaming: true,
      iterationHistory: [hist(1, '', 'R-old'), hist(2, '', 'R-new')] as never,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    expect(container.textContent).not.toContain('R-old')
  })

  it('不误伤：live 内容与任何已提交迭代都不同 ⇒ 照常渲染（真新内容）', () => {
    const snapshot = makeSnapshot({
      iteration: 3,
      streamContent: '全新内容',
      streaming: true,
      iterationHistory: [hist(1, 'X 文本'), hist(2, 'Y 文本')] as never,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    expect(container.firstChild).not.toBeNull()
  })

  // ─── 正交性判别（6641c9b3 revert 事件的闭环验证）───
  // 2026-10-02 事故链：6641c9b3（live-wins regionsBefore 传播）激活追赶填洞 →
  // 段 union 后 contiguous 扩展 → 当时的旧判据（只比最后一个）漏杀更早历史迭代
  // 的 live 副本 ⇒ 「一个 iter 重复渲染两次」P0 ⇒ 6641c9b3 被 revert。
  // 本组用例钉死：传播 on + 段填洞 union 全链路下，b4f9aa10 的内容匹配判据
  // 必须抑制旧副本 —— 修复缺失（判据退回 lastIter 单点）⇒ 必红。
  // ⚠️ typewriter 首帧 0 字符 —— 必须 fake timers 推满帧再断言，否则
  // not.toContain 恒过（假绿，mutation 下不红）。
  it('★ 熄屏恢复追赶填洞后：live 停在熄屏前的旧迭代内容 ⇒ 抑制（传播+判据正交）', () => {
    // 场景：本地 live [1..40] × incoming [52..90]+rb=6（live-wins union，6641c9b3 传播）
    // → 段加载填洞 [41..51] → 历史完整 [1..90]。live 的 streamContent 停在熄屏前
    // 最后看到的迭代 40 的内容 —— 它已在历史块渲染，绝不可再渲染一次。
    const history = [
      ...Array.from({ length: 40 }, (_, i) => hist(1 + i, `迭代${1 + i}内容`)),
      ...Array.from({ length: 11 }, (_, i) => hist(41 + i, `迭代${41 + i}内容`)),
      ...Array.from({ length: 39 }, (_, i) => hist(52 + i, `迭代${52 + i}内容`)),
    ]
    const snapshot = makeSnapshot({
      iteration: 90,
      streamContent: '迭代40内容', // 熄屏前最后看到的旧内容（历史里第 40 号）
      streaming: true,
      iterationHistory: history as never,
    })
    vi.useFakeTimers()
    try {
      const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
      // 推满 typewriter（50ms/步追赶；2000ms 必收敛）——判据失效时旧文本会打出来
      act(() => { vi.advanceTimersByTime(2000) })
      // 旧文本不在 live 块重复出现（历史块已渲染了它）
      expect(container.textContent).not.toContain('迭代40内容')
    } finally {
      vi.useRealTimers()
    }
  })

  it('★ 同场景反例：追赶填洞后流式恢复的【真新】内容必须照常渲染（不误杀）', () => {
    const history = [
      ...Array.from({ length: 90 }, (_, i) => hist(1 + i, `迭代${1 + i}内容`)),
    ]
    const snapshot = makeSnapshot({
      iteration: 91,
      streamContent: '第 91 个迭代的新流式内容（任何历史迭代都没有过）',
      streaming: true,
      iterationHistory: history as never,
    })
    vi.useFakeTimers()
    try {
      const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
      act(() => { vi.advanceTimersByTime(2000) })
      expect(container.textContent).toContain('第 91 个迭代的新流式内容')
    } finally {
      vi.useRealTimers()
    }
  })
})
