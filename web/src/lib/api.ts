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
    const error = envelope.error
    throw new APIError(
      error?.message || `request failed with status ${response.status}`,
      error?.code || 'invalid_response',
      response.status,
    )
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
  throw new APIError(
    envelope.error?.message || `request failed with status ${response.status}`,
    envelope.error?.code || 'invalid_response',
    response.status,
  )
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
