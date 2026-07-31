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
