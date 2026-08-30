/**
 * ShimmerThinking — bold borderless "正在思考" text using the shared
 * CSS-driven status sweep.
 */
import { memo, useEffect, useRef } from 'react'

import { useI18n } from '@/providers/i18n'
import { SweepText } from './SweepText'

export const ShimmerThinking = memo(function ShimmerThinking() {
  const { t } = useI18n()
  const text = t('agent.reasoningStreaming') // "思考中…" / "thinking…"
  const ref = useRef<HTMLDivElement>(null)
  const warnedDomRef = useRef(false)

  // ── 强一致性保证（诊断，不阻断渲染）─────────────────────────────
  // 思考中… 必须渲染在消息列表的最新位置 —— 它之后（下方）不能存在任何
  // 同级 DOM / turn / iter 内容。若出现，说明排序或状态错乱（例如思考中
  // 渲染在了已完成 turn 或新 turn 之前 —— turn 重复 / 思考中残留 bug 的
  // 常见形态），console 打印错误 + 当前 DOM 状态，便于日志定位。
  // 每次渲染后检查（无依赖数组）：兄弟 DOM 动态变化（新 turn 插入）也能捕获。
  // 分级：下方是消息行（data-message-id/turn-id/iter-count）→ error（强违规）；
  //       下方是普通同级 DOM（如 footer/AskUserPanel）→ warn 一次性（不刷屏）。
  //
  // PERF（手机发烫根因之一）：该 effect 无依赖数组，ShimmerThinking 每次
  // re-render 都执行 closest + nextElementSibling + 触发时
  // querySelectorAll('[data-message-id]') 全列表扫描。busy placeholder 挂在
  // AgentPanel 每帧 re-render 的树里，流式期间每帧 2 次 closest + 全列表
  // DOM 扫描。这是纯诊断（console 输出，不渲染任何东西），生产环境跳过
  // 对用户零感知（与 progressStore.assertInvariants 的既有 PROD 模式一致，
  // progressStore.ts assertInvariants 同样 if (import.meta.env?.PROD) return）。
  // Vite 构建时对 DEV 常量做 dead-code elimination，生产 bundle 完全不含
  // 这段扫描代码。
  useEffect(() => {
    if (!import.meta.env.DEV) return
    const el = ref.current
    if (!el) return
    // 找到思考中所在的行级容器：消息行（data-message-id 祖先，LiveIteration/
    // AssistantMessage）或消息列表容器的直接子元素（busy placeholder）。
    // 必须检查"行容器"的兄弟 —— ShimmerThinking 自身 div 的兄弟恒为空
    // （它被包裹在行容器内），直接检查 el.nextElementSibling 永远查不到。
    const rowContainer = findThinkingRowContainer(el)
    if (!rowContainer) return
    let sibling = rowContainer.nextElementSibling
    let depth = 0
    while (sibling) {
      depth++
      const siblingText = (sibling.textContent || '').trim()
      if (!siblingText && sibling.querySelectorAll('*').length === 0) {
        sibling = sibling.nextElementSibling
        continue
      }
      const isTurnOrIter =
        sibling.hasAttribute('data-message-id') ||
        sibling.hasAttribute('data-turn-id') ||
        sibling.hasAttribute('data-iter-count')
      const state = {
        depth,
        isTurnOrIter,
        rowContainerTag: rowContainer.tagName,
        rowContainerClass: typeof rowContainer.className === 'string' ? rowContainer.className : '',
        siblingTag: sibling.tagName,
        siblingClass: typeof sibling.className === 'string' ? sibling.className : '',
        siblingText: siblingText.slice(0, 200),
        siblingTurnID: sibling.getAttribute('data-turn-id') ?? null,
        siblingIterCount: sibling.getAttribute('data-iter-count') ?? null,
        siblingMessageID: sibling.getAttribute('data-message-id') ?? null,
        listContext: collectListContext(el),
        stack: new Error().stack,
      }
      if (isTurnOrIter) {
        // 强违规：思考中下方有 turn/iter 消息行 —— 排序/状态错乱，必须 error
        console.error('[THINKING_CONSISTENCY_VIOLATION] 思考中… 下方存在 turn/iter 消息行（强一致性违反）', state)
        break
      }
      // 普通同级 DOM（footer/AskUserPanel 等可能正常）→ warn 一次性，避免刷屏
      if (!warnedDomRef.current) {
        warnedDomRef.current = true
        console.warn('[THINKING_CONSISTENCY] 思考中… 下方存在同级 DOM', state)
      }
      break
    }
  })

  return (
    <div ref={ref} data-thinking-marker className="mt-1">
      <SweepText text={text} className="text-sm font-bold" />
    </div>
  )
})

/** 找到思考中所在的行级容器（其兄弟才是"思考中下方"的判定对象）。 */
function findThinkingRowContainer(el: HTMLElement): HTMLElement | null {
  // 消息行内（LiveIteration / AssistantMessage）：最近的 data-message-id 祖先
  const row = el.closest('[data-message-id]')
  if (row) return row as HTMLElement
  // busy placeholder（虚拟化容器外）：data-message-list-content 的直接子元素
  const list = el.closest('[data-message-list-content]')
  if (list) {
    let cur: HTMLElement | null = el
    while (cur && cur.parentElement !== list) {
      cur = cur.parentElement
    }
    return cur
  }
  return null
}

/** 收集思考中… 所在列表容器的状态快照（用于日志定位 bug）。 */
function collectListContext(el: HTMLElement): Record<string, unknown> | null {
  const list = el.closest('[data-message-list-content]')
  if (!list) return null
  const rows = Array.from(list.querySelectorAll('[data-message-id]'))
  return {
    renderedRowCount: rows.length,
    renderedTurnIDs: Array.from(new Set(rows.map((n) => n.getAttribute('data-turn-id')))).slice(-8),
    thinkingMarkerCount: list.querySelectorAll('[data-thinking-marker]').length,
    hasFooter: list.querySelector('[data-message-list-footer]') !== null,
  }
}

