/**
 * measureOffscreen —— 「prepend 段插入」两阶段原子提交的 **Phase 1：离屏权威测量**。
 *
 * ⛔ 用户硬要求（2026-10-12）：「用户看到的东西必须是**一动不动**」，并且**不接受**事后
 * 用快照 + 时间窗（deadline）+「估算→实测」重试那种补偿式打法 —— 它天生存在「先画错
 * 一帧、再补回来」的窗口。
 *
 * 结构性做法：段数据到达后，先在本文件的**离屏容器**里用**同一行组件、同一宽度**渲染
 * 一次，拿到**真实高度**；把高度作为权威值写进 `heightMemory`（`estimateSize` 会优先命中，
 * 见 MessageList 的组合根），**然后**才提交进可见列表 —— 于是插入那一刻高度就是权威的，
 * 不会再有「二次长高」，补偿窗口可以整段删掉。
 *
 * 本模块只负责「离屏渲染 + 稳定高度采集 + 清理」，不碰任何列表/状态（可独立测试）。
 *
 * 稳定性判据：连续两帧测到相同高度即认为已稳定（字体/图片/wrap 可能首帧后才定型）。
 * 超时按已读到的高度返回并标记 `timedOut` —— **绝不死等**（宁可保守，也不卡住用户）。
 */
import * as React from 'react'
import { flushSync } from 'react-dom'
import { createRoot, type Root } from 'react-dom/client'

/**
 * 内部错误边界：离屏测量里**行组件抛错**绝不外泄到宿主 app 的错误上报（React 19 无边界
 * 时会把渲染错误当 uncaught 上报 —— 那会污染全局、也会让测试/CI 报失败）。这里把错误
 * **捕获成值**，flushSync 之后再自己抛回给调用方（清理仍走 `finally`）。
 */
class MeasureBoundary extends React.Component<
  { onError: (e: unknown) => void; children?: React.ReactNode },
  { failed: boolean }
> {
  state = { failed: false }
  static getDerivedStateFromError(): { failed: boolean } {
    return { failed: true }
  }
  componentDidCatch(error: unknown): void {
    this.props.onError(error)
  }
  render(): React.ReactNode {
    return this.state.failed ? null : this.props.children
  }
}

export interface MeasureOffscreenOptions {
  /** 离屏容器宽度（**必须**与真实列表内容宽一致：宽度不同 ⇒ 折行不同 ⇒ 高度不可信）。 */
  width: number
  /** 与真实行相同的排版 class（字号/行距/内边距等；缺省只保证宽度一致）。 */
  className?: string
  /** 单次测量最长等待（ms，默认 120 ≈ 两帧）。超时按已读到的高度返回。 */
  timeoutMs?: number
  /** 注入的测量函数（测试用；默认读元素 rect 高度）。 */
  measure?: (el: HTMLElement) => number
  /** 注入的帧调度（测试用；默认 requestAnimationFrame）。 */
  raf?: (cb: () => void) => void
}

export interface MeasuredHeights {
  /** 与传入 nodes **同序**的实测高度。 */
  heights: number[]
  /** true = 超时返回（高度可能仍是首帧值，调用方应保守处理，例如不据此做原子提交）。 */
  timedOut: boolean
}

/** 离屏容器的固定定位样式：移出视口、不可见、不参与交互，但**保留真实布局**。 */
const HOST_STYLE: Partial<CSSStyleDeclaration> = {
  position: 'absolute',
  left: '-100000px',
  top: '0',
  visibility: 'hidden',
  pointerEvents: 'none',
  contain: 'layout style',
  zIndex: '-1',
}

interface HostHandle {
  host: HTMLDivElement
  root: Root
}

function mountHost(width: number, className: string | undefined): HostHandle {
  const host = document.createElement('div')
  Object.assign(host.style, HOST_STYLE)
  host.style.width = `${Math.max(1, Math.round(width))}px`
  // aria-hidden：离屏内容绝不进无障碍树（它只是测量副本，不是真实内容）。
  host.setAttribute('aria-hidden', 'true')
  host.dataset.testid = 'measure-offscreen-host'
  if (className) host.className = className
  document.body.appendChild(host)

  const root = createRoot(host)
  return { host, root }
}

function defaultMeasure(el: HTMLElement): number {
  return el.getBoundingClientRect().height
}

function defaultRaf(cb: () => void): void {
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(() => cb())
  else setTimeout(cb, 16)
}

export async function measureOffscreen(
  nodes: React.ReactNode[],
  opts: MeasureOffscreenOptions,
): Promise<MeasuredHeights> {
  const measure = opts.measure ?? defaultMeasure
  const raf = opts.raf ?? defaultRaf
  const timeoutMs = opts.timeoutMs ?? 120
  if (nodes.length === 0) return { heights: [], timedOut: false }

  const { host, root } = mountHost(opts.width, opts.className)
  try {
    const captured: { err: unknown } = { err: null }
    flushSync(() => {
      root.render(
        <MeasureBoundary onError={(e) => { captured.err = e }}>
          {nodes.map((node, i) => (
            <div key={i} data-mo-node={i}>
              {node}
            </div>
          ))}
        </MeasureBoundary>,
      )
    })
    // 行组件抛错 ⇒ 先让 finally 做完清理，再原样抛给调用方（绝不吞掉）。
    if (captured.err) throw captured.err

    const readAll = (): number[] => {
      const els = host.querySelectorAll<HTMLElement>('[data-mo-node]')
      const out: number[] = []
      for (let i = 0; i < nodes.length; i++) {
        const el = els[i]
        out.push(el ? measure(el) : 0)
      }
      return out
    }

    const deadline = Date.now() + timeoutMs
    let prev = readAll()
    let timedOut = false
    // 连续两帧高度一致 ⇒ 已稳定（字体/图片/wrap 可能首帧后才定型）。
    for (;;) {
      if (Date.now() >= deadline) {
        timedOut = true
        break
      }
      await new Promise<void>((resolve) => raf(() => resolve()))
      const next = readAll()
      const stable = next.length === prev.length && next.every((h, i) => h === prev[i])
      prev = next
      if (stable) break
    }
    return { heights: prev, timedOut }
  } finally {
    // 离屏副本必须彻底销毁：卸载 React 树 + 摘除宿主节点（绝不残留 DOM / 监听器）。
    try {
      root.unmount()
    } catch {
      /* 卸载失败不应影响调用方 */
    }
    host.remove()
  }
}
