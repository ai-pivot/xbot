/**
 * Plugin widget API — web_ui_action RPC and related calls.
 *
 * web_ui_action: user interaction with a plugin web UI component. The backend
 * routes it to the owning channel plugin (via its transport), a native plugin
 * handler, or falls back to injecting the action into the agent loop.
 */

import { APIError, reportAPIError } from '@/lib/api'

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
    if (!res.ok) {
      // 允许「降级返回 {}」但**不许静默**：401/403 是全局状态问题，走统一出口
      // （401 → 提示「登录已失效」+ 切登录页；403 → 可见提示）。
      // 其它状态码是插件业务失败，这里保持原语义（返回 {}）不打扰用户。
      reportAPIError(new APIError(res.statusText || `rpc failed with status ${res.status}`,
        `http_${res.status}`, res.status))
      return {}
    }
    const data = (await res.json()) as WebUIActionResult
    return data
  } catch (err) {
    // 允许吞：网络抖动/JSON 解析失败不阻断插件面板；但 401/403 已在上面上报。
    reportAPIError(err)
    return {}
  }
}
