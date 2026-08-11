/**
 * Plugin widget API — web_ui_action RPC and related calls.
 *
 * web_ui_action: user interaction with a plugin web UI component. The backend
 * routes it to the owning channel plugin (via its transport), a native plugin
 * handler, or falls back to injecting the action into the agent loop.
 */

export interface WebUIActionResult {
  ok?: boolean
  routed?: string
  result?: string
}

/** Send a web UI action (click / input) to the backend. */
export async function sendWebUIAction(params: {
  chat_id?: string
  widget_id: string
  action: string
  data?: unknown
}): Promise<WebUIActionResult> {
  const body: Record<string, unknown> = {
    method: 'web_ui_action',
    params: {
      widget_id: params.widget_id,
      action: params.action,
    },
  }
  if (params.chat_id) {
    ;(body.params as Record<string, unknown>).chat_id = params.chat_id
  }
  if (params.data !== undefined) {
    ;(body.params as Record<string, unknown>).data = JSON.stringify(params.data)
  }
  try {
    const res = await fetch('/api/rpc', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    if (!res.ok) return {}
    const data = (await res.json()) as WebUIActionResult
    return data
  } catch {
    return {}
  }
}
