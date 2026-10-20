/**
 * StructuredMessageStream —— 结构化消息流（虚拟列表 + 卡片懒渲染）。
 *
 * - >500 条消息时启用 @tanstack/react-virtual 虚拟滚动（只渲染可视区）；
 * - 卡片内容用 IntersectionObserver 懒挂载（进入视口附近才渲染真实卡片）；
 * - 按 created_at + message_id 排序（乱序消息稳定收敛）；
 * - 顶部/底部状态：加载、错误、空态、加载更多。
 */
import { useEffect, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'

import { StructuredCard, type AnswerFn, type DecideFn } from './StructuredCards'
import type { StructuredMessage } from './types'

function LazyMount({ children }: { children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement | null>(null)
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    if (visible) return
    const el = ref.current
    if (!el || typeof IntersectionObserver === 'undefined') {
      setVisible(true)
      return
    }
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setVisible(true)
          io.disconnect()
        }
      },
      { rootMargin: '600px 0px' },
    )
    io.observe(el)
    // 兜底：IntersectionObserver 在虚拟列表的 transformed 容器里偶发不回调
    // （截图实测首屏全是骨架），150ms 后强制渲染可见行。
    const timer = window.setTimeout(() => setVisible(true), 150)
    return () => {
      io.disconnect()
      window.clearTimeout(timer)
    }
  }, [visible])
  return (
    <div ref={ref} data-lazy={visible ? '1' : '0'}>
      {visible ? children : <div className="h-16 animate-pulse rounded-xl bg-slate-300/20" />}
    </div>
  )
}

export function StructuredMessageStream({
  messages,
  loading,
  error,
  hasMore,
  onLoadMore,
  onAnswer,
  onDecide,
}: {
  messages: StructuredMessage[]
  loading: boolean
  error: string | null
  hasMore: boolean
  onLoadMore: () => void
  onAnswer: AnswerFn
  onDecide: DecideFn
}) {
  const parentRef = useRef<HTMLDivElement | null>(null)
  const virtual = messages.length > 500
  const virtualizer = useVirtualizer({
    count: messages.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 180,
    overscan: 6,
    enabled: virtual,
  })

  useEffect(() => {
    if (messages.length === 0) return
    const el = parentRef.current
    if (!el) return
    // 消息流默认看最新：加载完成后滚到底部（虚拟列表用 scrollToIndex）。
    if (virtual) {
      virtualizer.scrollToIndex(messages.length - 1, { align: 'end' })
    } else {
      el.scrollTop = el.scrollHeight
    }
  }, [messages.length, virtual]) // eslint-disable-line react-hooks/exhaustive-deps

  const body =
    messages.length === 0 ? (
      loading ? (
        <p data-testid="stream-loading" className="p-4 text-sm opacity-60">
          加载中…
        </p>
      ) : (
        <p data-testid="stream-empty" className="p-4 text-sm opacity-60">
          还没有结构化消息。
        </p>
      )
    ) : virtual ? (
      <div style={{ height: virtualizer.getTotalSize(), position: 'relative', width: '100%' }}>
        {virtualizer.getVirtualItems().map((row) => (
          <div
            key={messages[row.index].message_id}
            data-index={row.index}
            ref={virtualizer.measureElement}
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              width: '100%',
              transform: `translateY(${row.start}px)`,
              paddingBottom: 10,
            }}
          >
            <LazyMount>
              <StructuredCard
                message={messages[row.index]}
                onAnswer={onAnswer}
                onDecide={onDecide}
              />
            </LazyMount>
          </div>
        ))}
      </div>
    ) : (
      <div className="space-y-2.5">
        {messages.map((m) => (
          <StructuredCard key={m.message_id} message={m} onAnswer={onAnswer} onDecide={onDecide} />
        ))}
      </div>
    )

  return (
    <section className="flex h-full min-h-0 flex-1 flex-col">
      <header className="flex items-center justify-between border-b border-slate-300/40 px-3 py-2 dark:border-slate-700/50">
        <div className="flex items-baseline gap-2">
          <h2 className="text-sm font-semibold">消息流</h2>
          <span data-testid="stream-count" className="text-[11px] opacity-60">
            {messages.length} 条
          </span>
          {virtual ? (
            <span className="rounded bg-sky-500/10 px-1.5 py-0.5 text-[10px] text-sky-500">
              虚拟滚动
            </span>
          ) : null}
        </div>
        {hasMore ? (
          <button
            type="button"
            onClick={onLoadMore}
            className="text-[11px] text-sky-500 hover:underline"
          >
            加载更早
          </button>
        ) : null}
      </header>
      {error ? (
        <p data-testid="stream-error" className="border-b border-red-500/20 bg-red-500/10 px-3 py-1.5 text-xs text-red-500">
          {error}
        </p>
      ) : null}
      <div ref={parentRef} data-testid="stream-scroll" className="min-h-0 flex-1 overflow-y-auto p-3">
        {body}
      </div>
    </section>
  )
}
