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
import { render, screen, waitFor } from '@testing-library/react'
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
