---
title: "Typed RPC"
weight: 6
---

Frontend plugins call backend methods through a **method-table-driven** typed RPC: parameter and return types are checked at compile time. Backend plugins extend the table by publishing `.d.ts` type packages (declaration merging). Defined in `web/src/plugin-api/rpc.ts`.

## BackendRPC — the method table

```ts
export interface BackendRPC {
  'session.get': { params: { chatID: string }; result: SessionDetail }
  'session.list': { params: Record<string, never>; result: SessionSummary[] }
  'agent.send': {
    params: { chatID: string; content: string }
    result: { turnID: number; queued: boolean }
  }
  'agent.cancel': { params: { chatID: string }; result: Record<string, never> }
  'plugin.list': { params: Record<string, never>; result: PluginInfo[] }
  'plugin.get_config': {
    params: { id: string }
    result: { configuration: PluginConfigSchema; values: Record<string, unknown> }
  }
  'plugin.set_config': {
    params: { id: string; key: string; value: unknown }
    result: { status: string; key: string }
  }
  // ---- xbot.git-fancy: fancy Git plugin data source ----
  'git.status': {
    params: { channel: string; chatID: string }
    result: {
      branch: string
      repo_name: string
      changes: Array<{ path: string; status: string; added: number; deleted: number }>
      ahead: number
      behind: number
      commit_hash: string
      commit_msg: string
      is_repo: boolean
    }
  }
  'git.log': {
    params: { channel: string; chatID: string; limit?: number }
    result: { commits: Array<{ hash: string; author: string; when: string; subject: string }> }
  }
  'git.diff': {
    params: { channel: string; chatID: string; path: string }
    result: { path: string; content: string }
  }
  'git.branches': {
    params: { channel: string; chatID: string }
    result: { current: string; branches: string[] }
  }
  // ---- Session usage stats (iteration_history v59 aggregate) ----
  'get_session_usage_stats': {
    params: { channel?: string; chat_id: string; limit?: number }
    result: TenantUsageStats
  }
}
```

`TenantUsageStats` (`web/src/plugin-api/rpc.ts`, mirrors Go `sqlite.TenantUsageStats`) — aggregated token / cache-hit / TTFT / TPOT stats for the current session, with per-model breakdown and recent iteration rows:

```ts
export interface TenantUsageStats {
  iteration_count: number
  turn_count: number
  input_tokens: number   // prompt tokens (persisted per-iteration since v59)
  output_tokens: number
  cached_tokens: number  // prompt-cache hit tokens
  llm_total_ms: number
  avg_ttft_ms: number    // NULLIF-filtered averages (zero rows excluded)
  avg_tpot_ms: number
  avg_tokens_per_sec: number
  last_prompt_tokens: number  // current context watermark (tenant_state)
  current_model: string
  by_model: UsageModelRow[] | null
  recent_iterations: UsageIterationRow[] | null
}
```


## RPCAPI

```ts
export interface RPCAPI {
  /** Call a backend method; name/params/result driven by BackendRPC. */
  call<K extends keyof BackendRPC>(
    method: K,
    params: BackendRPC[K]['params'],
  ): Promise<BackendRPC[K]['result']>
  /** One-way notification (no result awaited). */
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void
}
```

Usage — the compiler verifies both sides:

```ts
const res = await ctx.rpc.call('agent.send', { chatID: 'x', content: 'hi' })
// res: { turnID: number; queued: boolean }

// ✗ compile error: params must be { chatID: string; content: string }
// await ctx.rpc.call('agent.send', { chatID: 42 })
```

## Transport: /api/rpc envelope + web_plugin_rpc wrapper

`FetchRpcTransport` (`web/src/plugin-runtime/rpc.ts`) reuses the existing `/api/rpc` fetch channel (it does not depend on the WS connection being ready):

```ts
export class FetchRpcTransport implements RpcTransport {
  async call(method: string, params: unknown): Promise<unknown> {
    // Plugin methods (pluginId.method) → wrap as web_plugin_rpc; core methods pass through.
    if (method.includes('.')) {
      return postAPI('/api/rpc', { method: 'web_plugin_rpc', params: { method, params } })
    }
    return postAPI('/api/rpc', { method, params })
  }
}
```

Two critical details:

1. **Envelope unwrapping** — `/api/rpc` returns a `{ok, data, error}` envelope; `postAPI` unwraps it. Without this, `res.plugins` would read `undefined` (the real data is in `res.data.plugins`).
2. **`web_plugin_rpc` wrapping** — the backend RPC table has exactly ONE entry for plugin methods (`web_plugin_rpc`); its handler routes by `pluginId.method` to the owning backend plugin process. Passing the plugin method name as the outer method yields `unknown RPC method: xbot.git-fancy.status` (no such key in the table).

`PluginRpcBridge` implements `RPCAPI` on top of the transport; `notify` swallows failures (fire-and-forget):

```ts
export class PluginRpcBridge implements RPCAPI {
  async call<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): Promise<BackendRPC[K]['result']> {
    return (await this.transport.call(method as string, params)) as BackendRPC[K]['result']
  }
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void {
    void this.transport.call(method as string, params).catch(() => { /* notify failure is silent */ })
  }
}
```

## Extension via declaration merging

A backend plugin that exposes RPC methods (e.g. a Git data source) publishes a type package merging into `BackendRPC`. Frontend plugins importing it call `ctx.rpc.call('plugin.method', …)` with precise types — the table above already shows the real `xbot.git-fancy` extension (`git.status`/`git.log`/`git.diff`/`git.branches`).

## Session types

```ts
export interface SessionDetail {
  chatID: string
  title: string
  model: string
  busy: boolean
  maxContext: number
  maxOutput: number
  tokenUsage: { prompt: number; completion: number }
  createdAt: string
}

export interface PluginInfo {
  id: string
  name: string
  version: string
  enabled: boolean
}
```

## Wiring

The host constructs the bridge once in the `PluginRuntime` constructor:

```ts
this.rpc = new PluginRpcBridge(host.rpcTransport)
```

`usePluginRuntimeHost` supplies `rpcTransport: new FetchRpcTransport()`. All plugins share the same bridge instance — method routing is per-call, not per-plugin.
