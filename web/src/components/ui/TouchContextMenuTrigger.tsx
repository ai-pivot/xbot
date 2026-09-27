/**
 * TouchContextMenuTrigger —— 触屏长按增强的 ContextMenuTrigger。
 *
 * 为什么需要它（2026-09-27 安卓长按菜单失效根因）：
 *   Radix `ContextMenuTrigger` 的触屏实现（node_modules 源码实证）是
 *   `onPointerDown(touch) → 700ms timer` + `onPointerMove → clearLongPress`
 *   （**无位移容差**）+ `onPointerCancel → clearLongPress`。在安卓上这条链
 *   会断在两处：
 *     1. **原生文本选择抢手势**：触发元素（会话行/文件行）没有
 *        `user-select:none` 时，Chrome Android 长按 ~500ms 判定为文本选择
 *        → 派发 `pointercancel`（timer 被杀）且**不派发** `contextmenu`
 *        （选择优先于上下文菜单）→ 菜单永远不弹。iOS 上 Radix 自动设置的
 *        `WebkitTouchCallout:none` 恰好压制了原生接管，所以「只有苹果能长按」。
 *     2. **触摸噪声**：无容差的 move 清计时在部分安卓浏览器上被 1~2px 噪声
 *        击穿（与 MessageActions.useLongPress 2026-09-16 修复的同款坑）。
 *
 * 修法（不 patch node_modules）：
 *   - 触屏时给 trigger 包一层 div：`select-none`（断根因 1）+
 *     `[-webkit-touch-callout:none]`（iOS 原生 callout，与 Radix 行为对齐）。
 *   - 自建**带容差**（12px）的长按计时，比 Radix 的 700ms 更早（500ms）触发；
 *     触发时先派发 synthetic `pointerup`（杀掉 Radix 自己的无容差计时器，防
 *     双重触发），再派发 synthetic `contextmenu`（带触点坐标）→ 走 Radix 正常
 *     的 `onContextMenu` 路径开菜单（handleOpen + preventDefault 一步到位，
 *     不依赖 Radix 内部 API）。
 *   - 长按触发后拦截随后抬手产生的 `click`（安卓长按抬手仍会派发 click，会
 *     把「长按菜单」变成「点击切会话」）。
 *
 * 桌面（hover 可用）零变化：直接透传 Radix trigger（asChild 合并到原元素，
 * 不引入额外包裹层，布局/语义/可拖拽完全不动）。
 */
import { useCallback, useRef, type PointerEvent as ReactPointerEvent, type MouseEvent as ReactMouseEvent, type ComponentProps } from 'react'

import { ContextMenuTrigger } from '@/components/ui/context-menu'
import { useIsTouch } from '@/hooks/useIsMobile'
import { cn } from '@/lib/utils'

/** 长按判定时长：比 Radix 内部 700ms 更早，先发制人接管。 */
const LONG_PRESS_MS = 500
/** 长按判定容差（px）：手指抖动不超过它就不算"划动"（安卓触摸噪声防护）。 */
const LONG_PRESS_TOLERANCE = 12

type TouchContextMenuTriggerProps = ComponentProps<typeof ContextMenuTrigger>

export function TouchContextMenuTrigger({ children, className, ...rest }: TouchContextMenuTriggerProps) {
    const isTouch = useIsTouch()

    const timer = useRef<number | null>(null)
    const fired = useRef(false)
    /** 按下起点：用位移是否超过容差来判断"抖动"还是"划动"。 */
    const origin = useRef<{ x: number; y: number } | null>(null)
    /** 包裹层元素：长按触发时向它派发 synthetic contextmenu。 */
    const wrapRef = useRef<HTMLDivElement | null>(null)

    const clear = useCallback(() => {
      if (timer.current != null) window.clearTimeout(timer.current)
      timer.current = null
      origin.current = null
    }, [])

    const onPointerDown = useCallback(
      (e: ReactPointerEvent) => {
        // 鼠标走右键（Radix 自己处理 contextmenu）；只接管触屏/笔。
        if (e.pointerType === 'mouse') return
        fired.current = false
        const { clientX, clientY } = e
        clear() // clear() 会重置 origin，因此在其之后再记录起点
        origin.current = { x: clientX, y: clientY }
        timer.current = window.setTimeout(() => {
          const el = wrapRef.current
          if (!el) return
          fired.current = true
          // ① 先杀 Radix 自己的 700ms 无容差计时器（pointerup → clearLongPress），
          //    防止它随后二次 handleOpen（菜单已开时重定位会造成视觉跳动）。
          el.dispatchEvent(new PointerEvent('pointerup', { bubbles: true }))
          // ② 走 Radix 的 contextmenu 路径开菜单（触点坐标 = 菜单落点）。
          //    PointerEvent pointerType 默认 ''，Radix 的 whenTouchOrPen 不会
          //    把它当鼠标过滤掉 —— 但 contextmenu handler 无类型过滤，直通。
          el.dispatchEvent(
            new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX, clientY }),
          )
        }, LONG_PRESS_MS)
      },
      [clear],
    )

    const onPointerMove = useCallback(
      (e: ReactPointerEvent) => {
        const o = origin.current
        if (!o) return
        if (Math.abs(e.clientX - o.x) > LONG_PRESS_TOLERANCE || Math.abs(e.clientY - o.y) > LONG_PRESS_TOLERANCE) {
          clear()
        }
      },
      [clear],
    )

    // 长按已触发 → 拦截随后抬手的 click（安卓长按抬手仍派发 click，会把
    // 「长按菜单」误变成「点击选中/切会话」）。
    const onClickCapture = useCallback((e: ReactMouseEvent) => {
      if (fired.current) {
        e.preventDefault()
        e.stopPropagation()
        fired.current = false
      }
    }, [])

    // 桌面（hover 可用）：零包裹、零行为变化 —— asChild 合并到原元素。
    if (!isTouch) {
      return (
        <ContextMenuTrigger asChild {...rest} className={className}>
          {children}
        </ContextMenuTrigger>
      )
    }

    return (
      <ContextMenuTrigger asChild {...rest}>
        <div
          ref={wrapRef}
          data-testid="touch-context-trigger"
          // select-none：断「原生文本选择抢手势」这个根因（安卓长按可选文本
          // ⇒ pointercancel 杀计时且不派发 contextmenu ⇒ 菜单永远不弹）。
          // touch-callout：iOS 原生长按放大镜/callout 关闭（与 Radix 行为一致）。
          className={cn('select-none [-webkit-touch-callout:none]', className)}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={clear}
          onPointerCancel={clear}
          onPointerLeave={clear}
          onClickCapture={onClickCapture}
        >
          {children}
        </div>
      </ContextMenuTrigger>
    )
}
