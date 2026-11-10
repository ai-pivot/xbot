import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  APIError,
  onAPIError,
  postAPI,
  reportAPIError,
  UnauthorizedError,
} from './api'

afterEach(() => {
  vi.unstubAllGlobals()
})

function envelopeResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('postAPI', () => {
  it('sends JSON POST requests and unwraps response data', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({
      ok: true,
      data: { value: 42 },
      error: null,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(postAPI<{ value: number }>('/api/example', { name: 'xbot' }))
      .resolves.toEqual({ value: 42 })
    expect(fetchMock).toHaveBeenCalledWith('/api/example', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'xbot' }),
      headers: expect.objectContaining({ 'Content-Type': 'application/json' }),
    }))
  })

  it('throws the structured server error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      ok: false,
      data: null,
      error: { code: 'not_found', message: 'missing session' },
    }), { status: 404, headers: { 'Content-Type': 'application/json' } })))

    const error = await postAPI('/api/example').catch((reason: unknown) => reason)
    expect(error).toBeInstanceOf(APIError)
    expect(error).toMatchObject({
      message: 'missing session',
      code: 'not_found',
      status: 404,
    })
  })

  it('accepts null data in a successful response', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      ok: true,
      data: null,
      error: null,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))

    await expect(postAPI<null>('/api/example')).resolves.toBeNull()
  })
})

/**
 * 401 / 403 统一拦截（P0-2）。
 *
 * 病根：daidai 实例「旧二进制 + 新库」⇒ 后端全 401，前端把 401 当业务失败 ⇒
 * 用户只看到"操作失败"，没人去重登。判据必须双向钉死：
 *  ① 普通 API 401 ⇒ UnauthorizedError + 统一出口被调用（调用方能识别，不是业务失败）；
 *  ② 白名单探活 401 ⇒ 静默（不进统一出口、不是 UnauthorizedError）；
 *  ③ 403 ⇒ 不许静默（进统一出口），且照常抛给调用方。
 */
describe('401 / 403 统一拦截', () => {
  it('普通 API 的 401 ⇒ 抛 UnauthorizedError 且进统一出口（不伪装成业务失败）', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => envelopeResponse(401, {
      ok: false,
      data: null,
      error: { code: 'unauthorized', message: 'invalid or expired session' },
    })))

    const seen: APIError[] = []
    const off = onAPIError((e) => seen.push(e))
    const error = await postAPI('/api/chats/list').catch((reason: unknown) => reason)
    off()

    // 调用方拿到的是可识别的「登录失效」，不是通用业务失败。
    expect(error).toBeInstanceOf(UnauthorizedError)
    expect(error).toBeInstanceOf(APIError)
    expect(error).toMatchObject({
      name: 'UnauthorizedError',
      status: 401,
      code: 'unauthorized',
      message: 'invalid or expired session',
    })
    // 统一出口收到同一个错误对象（AuthProvider 靠它清态 + 提示 + 切登录页）。
    expect(seen).toHaveLength(1)
    expect(seen[0]).toBe(error)
  })

  it('带 query 的普通 API 401 同样拦截（白名单按路径匹配，不能被 ?ts= 绕过）', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => envelopeResponse(401, {
      ok: false,
      data: null,
      error: { code: 'unauthorized', message: 'gone' },
    })))

    const seen: APIError[] = []
    const off = onAPIError((e) => seen.push(e))
    const error = await postAPI('/api/sessions?limit=20').catch((reason: unknown) => reason)
    off()

    expect(error).toBeInstanceOf(UnauthorizedError)
    expect(seen).toHaveLength(1)
  })

  it('白名单探活（/api/settings）401 ⇒ 静默：不进统一出口、不是 UnauthorizedError', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => envelopeResponse(401, {
      ok: false,
      data: null,
      error: { code: 'unauthorized', message: 'not logged in' },
    })))

    const seen: APIError[] = []
    const off = onAPIError((e) => seen.push(e))
    const error = await postAPI('/api/settings').catch((reason: unknown) => reason)
    off()

    // 仍是 APIError（userSettings.ts 依赖 status===401 判定"未登录"），
    // 但**不是** UnauthorizedError ⇒ 不会触发「登录已失效」弹窗/跳转。
    expect(error).toBeInstanceOf(APIError)
    expect(error).not.toBeInstanceOf(UnauthorizedError)
    expect((error as APIError).status).toBe(401)
    expect(seen).toHaveLength(0)
  })

  it('登录接口 401 属业务失败（用户名或密码错误）⇒ 静默，不提示会话失效', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => envelopeResponse(401, {
      ok: false,
      data: null,
      error: { code: 'unauthorized', message: 'invalid credentials' },
    })))

    const seen: APIError[] = []
    const off = onAPIError((e) => seen.push(e))
    const error = await postAPI('/api/auth/login', { username: 'a', password: 'b' })
      .catch((reason: unknown) => reason)
    off()

    expect(error).not.toBeInstanceOf(UnauthorizedError)
    expect(seen).toHaveLength(0)
  })

  it('403 ⇒ 不许静默：进统一出口，并照常抛给调用方', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => envelopeResponse(403, {
      ok: false,
      data: null,
      error: { code: 'forbidden', message: 'operator role required' },
    })))

    const seen: APIError[] = []
    const off = onAPIError((e) => seen.push(e))
    const error = await postAPI('/api/chats/reorder', { channel: 'web', orders: {} })
      .catch((reason: unknown) => reason)
    off()

    expect(error).toBeInstanceOf(APIError)
    expect((error as APIError).status).toBe(403)
    // 可见出口必须被触达 —— 静默 `catch { return }` 也不再是无声失败。
    expect(seen).toHaveLength(1)
    expect(seen[0]).toBe(error)
  })

  it('同一个错误重复上报只触发一次（调用方 catch 里再报不会弹两次）', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => envelopeResponse(403, {
      ok: false,
      data: null,
      error: { code: 'forbidden', message: 'nope' },
    })))

    const seen: APIError[] = []
    const off = onAPIError((e) => seen.push(e))
    const error = await postAPI('/api/thing').catch((reason: unknown) => reason)
    reportAPIError(error)
    reportAPIError(error)
    off()

    expect(seen).toHaveLength(1)
  })
})
