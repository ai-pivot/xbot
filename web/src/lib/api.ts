export interface APIErrorBody {
  code: string
  message: string
}

interface APIResponse<T> {
  ok: boolean
  data: T | null
  error: APIErrorBody | null
}

export interface PostAPIOptions {
  signal?: AbortSignal
}

export class APIError extends Error {
  readonly code: string
  readonly status: number

  constructor(message: string, code: string, status: number) {
    super(message)
    this.name = 'APIError'
    this.code = code
    this.status = status
  }
}

/**
 * 401 —— **会话失效**（cookie 过期 / 后端换了库不认旧 token / 服务端重启换了签名）。
 *
 * 故意继承 `APIError`：既有 `instanceof APIError` 的调用点不受影响；但 `name`
 * 与 `status` 让调用方能区分「登录失效」和「业务失败」，**不得**再把 401 当
 * 业务错误静默吞掉（那正是 daidai 现场的病：用户看到"操作失败"，没人去重登）。
 *
 * 语义保证：抛出本错误之前，统一错误出口（`onAPIError`）已经收到同一个错误，
 * 由订阅方（AuthProvider）负责清登录态 + 提示 + 切登录页。
 */
export class UnauthorizedError extends APIError {
  constructor(message = 'unauthorized', code = 'unauthorized', status = 401) {
    super(message, code, status)
    this.name = 'UnauthorizedError'
  }
}

/**
 * 401 静默白名单：这些端点的 401 是**正常语义**，不是「登录已失效」。
 *
 * - `/api/settings` —— 主动探活端点。`AuthProvider` 初始化与
 *   `userSettings.syncAndMigrateSettings()` 用它判断「是否已登录」
 *   （200 = 已登录，401 = 未登录），未登录时 401 是预期结果，弹「登录已失效」
 *   属于误判。代价：会话过期后设置同步（debounce 写）的 401 也不会立刻弹窗，
 *   由下一次普通 API 调用拦截 —— 有意的取舍（探活正确性优先）。
 * - `/api/auth/config` —— 公开配置（无需登录）。
 * - `/api/auth/login` / `/api/auth/register` —— 401 = 用户名或密码错误，
 *   是**业务失败**，必须由登录页显示「用户名或密码错误」，不能提示会话失效。
 * - `/api/auth/logout` —— 会话已失效时登出仍必须成功清理本地态。
 */
export const UNAUTHORIZED_SILENT_ENDPOINTS: ReadonlySet<string> = new Set([
  '/api/settings',
  '/api/auth/config',
  '/api/auth/login',
  '/api/auth/register',
  '/api/auth/logout',
])

/** 去掉 query / hash，只按路径匹配白名单（`/api/push/config?ts=…` 之类）。 */
function endpointPath(endpoint: string): string {
  const cut = endpoint.search(/[?#]/)
  return cut === -1 ? endpoint : endpoint.slice(0, cut)
}

/** 该端点的 401 是否应静默（白名单探活）。 */
export function isUnauthorizedSilenced(endpoint: string): boolean {
  return UNAUTHORIZED_SILENT_ENDPOINTS.has(endpointPath(endpoint))
}

// ── 统一错误出口（401 / 403）────────────────────────────────────────────────

export type APIErrorListener = (error: APIError) => void

const apiErrorListeners = new Set<APIErrorListener>()
/** 同一个 error 只上报一次：postAPI 已上报过的错误，调用方再 reportAPIError 不会重复弹窗。 */
const reportedErrors = new WeakSet<APIError>()

/** 订阅统一错误出口（401 会话失效 / 403 无权限）。返回反订阅函数。 */
export function onAPIError(listener: APIErrorListener): () => void {
  apiErrorListeners.add(listener)
  return () => { apiErrorListeners.delete(listener) }
}

/**
 * 把错误上报到统一出口（幂等）。只处理 401/403 —— 它们是「全局状态问题」，
 * 不该由几十个调用点各写一遍提示。返回 true 表示是全局类错误。
 */
export function reportAPIError(error: unknown): boolean {
  if (!(error instanceof APIError)) return false
  if (error.status !== 401 && error.status !== 403) return false
  if (reportedErrors.has(error)) return true
  reportedErrors.add(error)
  for (const listener of apiErrorListeners) {
    try {
      listener(error)
    } catch (listenerError) {
      // 订阅方崩了不许影响请求本身。
      console.error('[api] onAPIError listener failed:', listenerError)
    }
  }
  return true
}

/**
 * 唯一的失败收敛点：所有走本模块的请求（含自带信封解析的层）都从这里抛错。
 * 401 非白名单 ⇒ `UnauthorizedError` + 上报；403 ⇒ 上报（不许静默）+ 照常抛出。
 */
export function throwResponseError(
  endpoint: string,
  response: Response,
  envelope: { error?: { code?: string; message?: string } | null },
): never {
  const status = response.status
  const message = envelope.error?.message || `request failed with status ${status}`
  const code = envelope.error?.code || 'invalid_response'

  if (status === 401 && !isUnauthorizedSilenced(endpoint)) {
    const unauthorized = new UnauthorizedError(message, code, status)
    reportAPIError(unauthorized)
    throw unauthorized
  }

  const error = new APIError(message, code, status)
  // 403 不许静默：进统一出口（可见提示由订阅方负责），再照常抛给调用方。
  if (status === 403) reportAPIError(error)
  throw error
}

/** Send a JSON or multipart POST and unwrap the shared {ok,data,error} envelope. */
export async function postAPI<T>(
  endpoint: string,
  body: unknown = {},
  options: PostAPIOptions = {},
): Promise<T> {
  const isForm = body instanceof FormData
  const response = await fetch(endpoint, {
    method: 'POST',
    headers: isForm
      ? { Accept: 'application/json' }
      : { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: isForm ? body : JSON.stringify(body),
    signal: options.signal,
  })

  const envelope = await readEnvelope<T>(response)
  if (!response.ok || !envelope.ok) {
    // 唯一拦截点：401（非白名单）→ UnauthorizedError + 全局提示；403 → 上报。
    throwResponseError(endpoint, response, envelope)
  }
  return envelope.data as T
}

/** POST variant for endpoints that intentionally return raw bytes (fs/read raw=true). */
export async function postRawAPI(
  endpoint: string,
  body: unknown,
  options: PostAPIOptions = {},
): Promise<Response> {
  const response = await fetch(endpoint, {
    method: 'POST',
    headers: { Accept: '*/*', 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: options.signal,
  })
  if (response.ok) return response

  const envelope = await readEnvelope<unknown>(response)
  throwResponseError(endpoint, response, envelope)
}

export interface WebPushSubscription {
  endpoint: string
  keys?: { p256dh?: string; auth?: string }
}

export interface PushConfig {
  webpush?: { enabled: boolean; public_key?: string }
}

/** Public push config (VAPID key); enabled=false means WebPush is off.
 *  The timestamp bypasses a stale service-worker `api-cache` entry. */
export function getPushConfig(): Promise<PushConfig> {
  return postAPI<PushConfig>(`/api/push/config?ts=${Date.now()}`)
}

export interface PushFCMConfig {
  enable: boolean
  project_id: string
  credentials_file: string
  mode: string
}

export interface PushWebPushConfig {
  enable: boolean
  vapid_public: string
  vapid_private?: string
  subject: string
  endpoint_allowlist: string[]
}

export interface PushAPNsConfig {
  enable: boolean
  key_file: string
  key_id: string
  team_id: string
  topic: string
  env: string
}

export interface PushFeishuConfig {
  enable: boolean
}

export interface PushSettings {
  enable: boolean
  include_preview: boolean
  fcm?: PushFCMConfig
  webpush?: PushWebPushConfig
  apns?: PushAPNsConfig
  feishu?: PushFeishuConfig
  has_vapid_private?: boolean
}

/** Persisted push configuration (VAPID private key masked by the server). */
export async function getPushSettings(): Promise<PushSettings> {
  const response = await fetch('/api/push/settings', {
    headers: { Accept: 'application/json' },
  })
  const envelope = await readEnvelope<PushSettings>(response)
  if (!response.ok || !envelope.ok) {
    throwResponseError('/api/push/settings', response, envelope)
  }
  return (envelope.data || { enable: false, include_preview: false }) as PushSettings
}

/** Save settings; the server persists them and hot-swaps the push layer. */
export function savePushSettings(settings: PushSettings): Promise<unknown> {
  return postAPI('/api/push/settings', settings)
}

/** Register this browser's PushSubscription as a webpush target. */
export function registerWebPush(
  sub: WebPushSubscription,
  deviceName = '',
): Promise<unknown> {
  return postAPI('/api/push/register', {
    provider: 'webpush',
    platform: 'web',
    address: sub.endpoint,
    metadata: {
      p256dh: sub.keys?.p256dh || '',
      auth: sub.keys?.auth || '',
    },
    device_name: deviceName.slice(0, 120),
    app_version: '',
  })
}

/** Remove this browser's target before unsubscribing. */
export function unregisterWebPush(endpoint: string): Promise<unknown> {
  return postAPI('/api/push/unregister', {
    provider: 'webpush',
    address: endpoint,
  })
}

async function readEnvelope<T>(response: Response): Promise<APIResponse<T>> {
  try {
    return await response.json() as APIResponse<T>
  } catch {
    return {
      ok: false,
      data: null,
      error: { code: 'invalid_response', message: 'server returned invalid JSON' },
    }
  }
}
