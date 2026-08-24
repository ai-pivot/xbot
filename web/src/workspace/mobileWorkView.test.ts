/**
 * mobileWorkView —— 手机端全屏工作视图单例测试。
 */
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { closeMobileWorkView, pushMobileWorkView, useMobileWorkView } from './mobileWorkView'

afterEach(() => {
  closeMobileWorkView()
})

describe('mobileWorkView 单例', () => {
  it('push 后订阅者收到视图，close 后收到 null', () => {
    const { result } = renderHook(() => useMobileWorkView())
    expect(result.current).toBeNull()

    act(() => {
      pushMobileWorkView({ kind: 'file', title: 'a.go', filePath: '/repo/a.go' })
    })
    expect(result.current).toEqual({ kind: 'file', title: 'a.go', filePath: '/repo/a.go' })

    act(() => {
      closeMobileWorkView()
    })
    expect(result.current).toBeNull()
  })

  it('插件视图（openViewTab 的手机端路由）携带 viewId/viewKey/viewParams', () => {
    const { result } = renderHook(() => useMobileWorkView())
    act(() => {
      pushMobileWorkView({
        kind: 'plugin',
        title: 'src/a.go',
        viewId: 'xbot.git-fancy.diff',
        viewKey: 'git-diff:worktree:src/a.go',
        viewParams: { path: 'src/a.go', commit: '' },
      })
    })
    expect(result.current).toMatchObject({
      kind: 'plugin',
      viewId: 'xbot.git-fancy.diff',
      viewKey: 'git-diff:worktree:src/a.go',
      viewParams: { path: 'src/a.go', commit: '' },
    })
  })

  it('重复 push 替换当前视图（单值语义，非栈）', () => {
    const { result } = renderHook(() => useMobileWorkView())
    act(() => {
      pushMobileWorkView({ kind: 'file', title: 'a', filePath: '/a' })
    })
    act(() => {
      pushMobileWorkView({ kind: 'file', title: 'b', filePath: '/b' })
    })
    expect(result.current).toEqual({ kind: 'file', title: 'b', filePath: '/b' })
  })

  it('新订阅者挂载时同步单例现值', () => {
    pushMobileWorkView({ kind: 'file', title: 'x', filePath: '/x' })
    const { result } = renderHook(() => useMobileWorkView())
    expect(result.current).toEqual({ kind: 'file', title: 'x', filePath: '/x' })
  })
})
