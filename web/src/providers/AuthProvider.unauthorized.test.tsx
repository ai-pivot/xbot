/**
 * AuthProvider × 统一 401 拦截 —— 端到端判据（P0-2）。
 *
 * 现场（可复算）：daidai 实例「旧二进制 + 新库」⇒ 后端全 401，而前端把 401 当
 * 业务失败处理 ⇒ 用户看到"操作失败"，没人去重登。本文件的判据是**用户可见行为**：
 *
 *  ① 普通 API 401 ⇒ 提示「登录已失效，请重新登录」+ 切到 /login（AuthGuard 既有机制）
 *     + 调用方拿到的是 `UnauthorizedError`（不是业务失败）；
 *  ② 白名单探活（/api/settings）401 ⇒ 静默：不弹提示、不踢到登录页（会话仍在）；
 *  ③ 403 ⇒ 有可见出口（toast），且不把用户踢下线。
 *
 * 变异自证：把 lib/api.ts 的 401 分支去掉（或让它不触发）⇒ ① 必红。
 */
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useEffect, useState } from 'react'

import { AuthGuard } from '@/components/auth/AuthGuard'
import { postAPI, APIError, UnauthorizedError } from '@/lib/api'
import i18n from '@/i18n'
import { AuthProvider } from './AuthProvider'

// toast 是「可见出口」的观测点；sonner 需要 Toaster 才会真正渲染成 DOM，
// 单测里直接对 toast.error 取证据更稳（也避免依赖 Radix Portal）。
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn(), message: vi.fn() } }))

import { toast } from 'sonner'

const toastError = vi.mocked(toast.error)

/** 记录调用方实际拿到的东西 —— 用来证明 401 不是「业务失败」。 */
let lastError: unknown = null

/** 一个"普通业务调用"的探针：挂载即发一次请求。 */
function APICallProbe({ endpoint }: { endpoint: string }) {
  const [state, setState] = useState('waiting')
  useEffect(() => {
    void postAPI(endpoint)
      .then(() => setState('resolved'))
      .catch((err: unknown) => {
        lastError = err
        setState('rejected')
      })
  }, [endpoint])
  return <div data-testid="probe">{state}</div>
}

function ok(data: unknown): Response {
  return new Response(JSON.stringify({ ok: true, data, error: null }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function fail(status: number, message = 'boom'): Response {
  return new Response(JSON.stringify({ ok: false, data: null, error: { code: `http_${status}`, message } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/**
 * 挂载与生产一致的结构：AuthProvider 在路由**外层**（main.tsx 就是这样），
 * 受保护路由由 AuthGuard 把关 —— 401 清态后 AuthGuard 会 `<Navigate to="/login">`。
 */
function renderApp(probeEndpoint: string) {
  return render(
    <AuthProvider>
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/login" element={<div data-testid="login-page" />} />
          <Route
            path="/"
            element={(
              <AuthGuard>
                <div data-testid="app-page" />
                <APICallProbe endpoint={probeEndpoint} />
              </AuthGuard>
            )}
          />
        </Routes>
      </MemoryRouter>
    </AuthProvider>,
  )
}

beforeEach(async () => {
  lastError = null
  toastError.mockClear()
  localStorage.clear()
  // 断言中文文案：默认 locale 由浏览器语言推断（jsdom = en-US），显式钉到 zh-CN。
  await i18n.changeLanguage('zh-CN')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('AuthProvider × 401 / 403 可见行为', () => {
  it('普通 API 401 ⇒ 提示「登录已失效」+ 切登录页 + 调用方拿到 UnauthorizedError', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.startsWith('/api/auth/config')) return ok({ invite_only: false })
      if (url.startsWith('/api/settings')) return ok({ settings: {} })
      // 普通业务 API：后端全 401（daidai 现场）
      return fail(401, 'invalid or expired session')
    }))

    renderApp('/api/chats/list')

    // 401 到达后 AuthProvider 会立刻清态 ⇒ AuthGuard 把页面换成 /login（探针随之卸载），
    // 所以先等跳转，再核对调用方拿到的错误与用户可见提示。
    await waitFor(() => expect(screen.getByTestId('login-page')).toBeTruthy())

    // ① 调用方不当业务失败：拿到可识别的 UnauthorizedError / status=401
    expect(lastError).toBeInstanceOf(UnauthorizedError)
    expect(lastError).toBeInstanceOf(APIError)
    expect((lastError as APIError).status).toBe(401)

    // ② 用户可见提示：明确文案（不是"操作失败"）
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(
      '登录已失效，请重新登录',
      { id: 'session-expired' },
    ))

    // ③ 受保护页面消失（已切到登录页）
    expect(screen.queryByTestId('app-page')).toBeNull()
  })

  it('白名单探活（/api/settings）401 ⇒ 静默：不提示、不踢下线', async () => {
    let settingsCalls = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.startsWith('/api/auth/config')) return ok({ invite_only: false })
      if (url.startsWith('/api/settings')) {
        settingsCalls += 1
        // 第 1 次是初始化探活（已登录）；之后是组件里的主动探活 → 401
        return settingsCalls === 1 ? ok({ settings: {} }) : fail(401, 'not logged in')
      }
      return fail(404, 'unexpected')
    }))

    renderApp('/api/settings')

    await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('rejected'))

    // 探活 401 是「未登录」的正常语义，而不是「登录已失效」
    expect(lastError).toBeInstanceOf(APIError)
    expect(lastError).not.toBeInstanceOf(UnauthorizedError)
    expect(toastError).not.toHaveBeenCalled()
    // 会话仍在：仍停在受保护页面，没有被踢到 /login
    expect(screen.getByTestId('app-page')).toBeTruthy()
    expect(screen.queryByTestId('login-page')).toBeNull()
  })

  it('403 ⇒ 有可见出口（toast）且不把用户踢下线', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.startsWith('/api/auth/config')) return ok({ invite_only: false })
      if (url.startsWith('/api/settings')) return ok({ settings: {} })
      return fail(403, 'operator role required')
    }))

    renderApp('/api/chats/reorder')

    await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('rejected'))

    expect((lastError as APIError).status).toBe(403)
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(
      '没有权限执行该操作：operator role required',
      { id: 'forbidden-error' },
    ))
    expect(screen.getByTestId('app-page')).toBeTruthy()
    expect(screen.queryByTestId('login-page')).toBeNull()
  })
})
