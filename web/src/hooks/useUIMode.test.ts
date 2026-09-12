import { renderHook, act } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// syncSettingToServer 走网络（postAPI）——测试只关心 localStorage + 事件同步。
vi.mock('@/lib/userSettings', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/userSettings')>()
  return { ...actual, syncSettingToServer: vi.fn() }
})

import { SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'
import { UI_MODE_STORAGE_KEY, useUIMode } from './useUIMode'
import { useIsMobile } from './useIsMobile'

/** 覆写 matchMedia（test-setup 默认 matches:false = 宽视口）。 */
function mockViewport(isNarrow: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: isNarrow && query.includes('max-width'),
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }))
}

describe('useUIMode', () => {
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => store.set(key, value),
      removeItem: (key: string) => store.delete(key),
    })
    mockViewport(false)
  })

  it('默认 auto：宽视口解析为 desktop', () => {
    const { result } = renderHook(() => useUIMode())
    expect(result.current.mode).toBe('auto')
    expect(result.current.effective).toBe('desktop')
  })

  it('auto + 窄视口（≤767px）解析为 mobile', () => {
    mockViewport(true)
    const { result } = renderHook(() => useUIMode())
    expect(result.current.mode).toBe('auto')
    expect(result.current.effective).toBe('mobile')
  })

  it('强制 mobile：写 localStorage（服务端同步）并即时生效', () => {
    const { result } = renderHook(() => useUIMode())
    act(() => result.current.setMode('mobile'))
    expect(localStorage.getItem(UI_MODE_STORAGE_KEY)).toBe('mobile')
    expect(result.current.effective).toBe('mobile')
  })

  it('强制 mobile/desktop 覆盖视口断点（宽视口强制 mobile）', () => {
    localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
    const { result } = renderHook(() => useUIMode())
    expect(result.current.effective).toBe('mobile')

    // 窄视口强制 desktop —— 依然是桌面外壳
    mockViewport(true)
    localStorage.setItem(UI_MODE_STORAGE_KEY, 'desktop')
    const forced = renderHook(() => useUIMode())
    expect(forced.result.current.effective).toBe('desktop')
  })

  it('同窗口多实例同步：一处 setMode，另一处立即重渲染', () => {
    const a = renderHook(() => useUIMode())
    const b = renderHook(() => useUIMode())
    expect(b.result.current.mode).toBe('auto')
    act(() => a.result.current.setMode('mobile'))
    expect(b.result.current.mode).toBe('mobile')
    expect(b.result.current.effective).toBe('mobile')
  })

  it('服务端同步拉回值（SETTINGS_SYNCED_EVENT）后重新读取', () => {
    const { result } = renderHook(() => useUIMode())
    expect(result.current.mode).toBe('auto')
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
      window.dispatchEvent(new Event(SETTINGS_SYNCED_EVENT))
    })
    expect(result.current.mode).toBe('mobile')
  })

  it('非法存储值回退 auto', () => {
    localStorage.setItem(UI_MODE_STORAGE_KEY, 'phone')
    const { result } = renderHook(() => useUIMode())
    expect(result.current.mode).toBe('auto')
  })
})

describe('useIsMobile（派生自 useUIMode）', () => {
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => store.set(key, value),
      removeItem: (key: string) => store.delete(key),
    })
    mockViewport(false)
  })

  it('auto：跟随视口断点', () => {
    expect(renderHook(() => useIsMobile()).result.current).toBe(false)
    mockViewport(true)
    expect(renderHook(() => useIsMobile()).result.current).toBe(true)
  })

  it('强制 desktop：窄视口下依然不是移动端布局', () => {
    mockViewport(true)
    localStorage.setItem(UI_MODE_STORAGE_KEY, 'desktop')
    expect(renderHook(() => useIsMobile()).result.current).toBe(false)
  })

  it('强制 mobile：宽视口下也是移动端布局，且 setMode 后立即切换', () => {
    const { result } = renderHook(() => useIsMobile())
    expect(result.current).toBe(false)
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
      window.dispatchEvent(new Event(SETTINGS_SYNCED_EVENT))
    })
    expect(result.current).toBe(true)
  })
})
