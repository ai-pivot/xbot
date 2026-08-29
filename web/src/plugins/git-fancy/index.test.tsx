/**
 * git-fancy 插件面板测试 —— hooks 顺序回归守护。
 *
 * 复现 React #310（Rendered fewer hooks than expected）：组件的条件提前
 * return（loading/error/not-repo 分支）之后不得再调用任何 hook——否则
 * loading→loaded 状态切换时 hooks 数量变化，React 直接卸载组件树。
 *
 * 插件模块从 window.React 取 React（不 import 宿主 react）——测试先注入
 * 真实 React，再动态 import 插件模块（静态 import 会被提升到注入之前）。
 */
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import * as RealReact from 'react'
import { describe, expect, it, vi } from 'vitest'

// 插件模块在模块初始化时读 window.React —— 必须先注入再动态 import。
;(window as unknown as { React: unknown }).React = RealReact

describe('GitFancyPanel hooks 顺序', () => {
  it('loading→loaded 状态切换不触发 React #310（提前 return 之后不得有 hook）', async () => {
    const { setSharedApi } = await import('@/plugins/git-fancy/shared')
    // mock rpc：status + log 返回有效数据，驱动 loading→loaded 切换。
    setSharedApi(vi.fn((method: string) => {
      if (method.endsWith('status')) {
        return Promise.resolve({ repo: true, branch: 'main', clean: true, changes: [], ahead: 0, behind: 0 })
      }
      if (method.endsWith('log')) {
        return Promise.resolve({ commits: [], total: 0 })
      }
      return Promise.resolve({})
    }) as never)

    const { GitFancyPanel } = await import('@/plugins/git-fancy/index')

    // 初始渲染（loading 提前 return 分支）→ refresh 完成 → loaded 全量渲染
    // （含 commit 列表区）。若 expandedHash 之类的 hook 在提前 return 之后，
    // 这里会抛 "Rendered fewer hooks than expected"（React #310）。
    expect(() => render(<RealReact.Suspense fallback={null}><GitFancyPanel /></RealReact.Suspense>)).not.toThrow()
    await waitFor(() => {
      expect(screen.getByText(/提交/)).toBeInTheDocument()
    })
  })
})

// ── Loop2 F6：useSplitRatio 的 localStorage 副作用必须移出 setState updater ──
// setState updater 必须是纯函数（React StrictMode 双调用 updater 副作用检测；
// 并发渲染重放会重复执行 updater）。旧实现把 localStorage.setItem 放在
// setTopPct(cur => {...setItem; return cur}) 的 updater 里 —— StrictMode 下
// setItem 被调用两次。修复后副作用在 pointerup 事件处理器（拖拽结束时
// 恰好执行一次），行为不变：localStorage 记录最终拖拽比例。
describe('GitFancyPanel useSplitRatio（Loop2 F6）', () => {
  it('拖拽结束时 localStorage.setItem 只调用一次（updater 纯函数，StrictMode 双 updater 下不双写）', async () => {
    const { setSharedApi } = await import('@/plugins/git-fancy/shared')
    setSharedApi(vi.fn((method: string) => {
      if (method.endsWith('status')) {
        return Promise.resolve({ repo: true, branch: 'main', clean: true, changes: [], ahead: 0, behind: 0 })
      }
      if (method.endsWith('log')) {
        return Promise.resolve({ commits: [], total: 0 })
      }
      return Promise.resolve({})
    }) as never)

    const { GitFancyPanel } = await import('@/plugins/git-fancy/index')

    localStorage.removeItem('git-fancy:split-ratio')
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')

    // StrictMode：React 18 dev 下 setState updater 会被双调用（副作用检测）——
    // 修复前 setItem 在 updater 内 → 调用 2 次；修复后在事件处理器内 → 恰好 1 次。
    render(
      <RealReact.StrictMode>
        <RealReact.Suspense fallback={null}>
          <GitFancyPanel />
        </RealReact.Suspense>
      </RealReact.StrictMode>,
    )
    await waitFor(() => {
      expect(screen.getByText(/提交/)).toBeInTheDocument()
    })

    const handle = screen.getByTitle('拖拽调整上下区域比例')
    const container = handle.parentElement as HTMLElement
    // jsdom 无布局 —— mock 容器 rect（height=200：deltaY 60 → +30%）
    vi.spyOn(container, 'getBoundingClientRect').mockReturnValue({
      height: 200, width: 100, top: 0, left: 0, bottom: 200, right: 100, x: 0, y: 0,
      toJSON: () => ({}),
    } as DOMRect)

    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientY: 100 })
    // 40%（initial）+ (160-100)/200*100 = 70%
    fireEvent.pointerMove(handle, { pointerId: 1, clientY: 160 })
    fireEvent.pointerUp(handle, { pointerId: 1 })

    // 副作用恰好一次（事件处理器，不是 updater —— StrictMode 双 updater 不双写）
    expect(setItemSpy).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('git-fancy:split-ratio')).toBe('70')
    setItemSpy.mockRestore()
    localStorage.removeItem('git-fancy:split-ratio')
  })

  it('拖拽 clamp（10-90）+ 无拖动 pointerup 仍持久化（行为保持）', async () => {
    const { setSharedApi } = await import('@/plugins/git-fancy/shared')
    setSharedApi(vi.fn((method: string) => {
      if (method.endsWith('status')) {
        return Promise.resolve({ repo: true, branch: 'main', clean: true, changes: [], ahead: 0, behind: 0 })
      }
      if (method.endsWith('log')) {
        return Promise.resolve({ commits: [], total: 0 })
      }
      return Promise.resolve({})
    }) as never)

    const { GitFancyPanel } = await import('@/plugins/git-fancy/index')

    localStorage.removeItem('git-fancy:split-ratio')
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')

    render(
      <RealReact.StrictMode>
        <RealReact.Suspense fallback={null}>
          <GitFancyPanel />
        </RealReact.Suspense>
      </RealReact.StrictMode>,
    )
    await waitFor(() => {
      expect(screen.getByText(/提交/)).toBeInTheDocument()
    })

    const handle = screen.getByTitle('拖拽调整上下区域比例')
    const container = handle.parentElement as HTMLElement
    vi.spyOn(container, 'getBoundingClientRect').mockReturnValue({
      height: 200, width: 100, top: 0, left: 0, bottom: 200, right: 100, x: 0, y: 0,
      toJSON: () => ({}),
    } as DOMRect)

    // 拖过头：40% + 500/200*100 → clamp 90
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientY: 100 })
    fireEvent.pointerMove(handle, { pointerId: 1, clientY: 600 })
    fireEvent.pointerUp(handle, { pointerId: 1 })
    expect(localStorage.getItem('git-fancy:split-ratio')).toBe('90')

    setItemSpy.mockRestore()
    localStorage.removeItem('git-fancy:split-ratio')
  })
})
