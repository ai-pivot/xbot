---
title: "类型化 RPC"
weight: 6
---

前端插件通过**方法表驱动**的类型化 RPC 调用后端方法：参数与返回类型编译期校验。后端插件发布 `.d.ts` 类型包（声明合并）扩展方法表。定义于 `web/src/plugin-api/rpc.ts`。

## BackendRPC —— 方法表

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
  // ---- xbot.git-fancy：fancy Git 插件数据源 ----
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
  // ---- 会话用量统计（iteration_history v59 聚合）----
  'get_session_usage_stats': {
    params: { channel?: string; chat_id: string; limit?: number }
    result: TenantUsageStats
  }
}
```

`TenantUsageStats`（`web/src/plugin-api/rpc.ts`，与 Go `sqlite.TenantUsageStats` 对齐）——当前会话的 token / cache 命中 / TTFT / TPOT 聚合 + per-model 分组 + 最近迭代明细：

```ts
export interface TenantUsageStats {
  iteration_count: number   // 迭代数
  turn_count: number        // turn 数
  input_tokens: number      // 输入 tokens（v59 起 per-iteration 入库）
  output_tokens: number     // 输出 tokens
  cached_tokens: number     // prompt cache 命中 tokens
  llm_total_ms: number      // LLM 流式总时长
  avg_ttft_ms: number       // 平均首 token 延迟（NULLIF 过滤 0 值）
  avg_tpot_ms: number       // 平均每 token 延迟
  avg_tokens_per_sec: number
  last_prompt_tokens: number    // 当前上下文水位（tenant_state）
  current_model: string
  by_model: UsageModelRow[] | null          // 按模型分组
  recent_iterations: UsageIterationRow[] | null  // 最近 N 条迭代明细
}
```


## RPCAPI

```ts
export interface RPCAPI {
  /** 调用后端方法；方法名/参数/返回类型由 BackendRPC 驱动。 */
  call<K extends keyof BackendRPC>(
    method: K,
    params: BackendRPC[K]['params'],
  ): Promise<BackendRPC[K]['result']>
  /** 单向通知（不等待结果）。 */
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void
}
```

使用——编译器校验两侧：

```ts
const res = await ctx.rpc.call('agent.send', { chatID: 'x', content: 'hi' })
// res: { turnID: number; queued: boolean }

// ✗ 编译错误：参数必须是 { chatID: string; content: string }
// await ctx.rpc.call('agent.send', { chatID: 42 })
```

## 传输：/api/rpc envelope + web_plugin_rpc 包装

`FetchRpcTransport`（`web/src/plugin-runtime/rpc.ts`）复用现有 `/api/rpc` fetch 通道（不依赖 WS 连接就绪）：

```ts
export class FetchRpcTransport implements RpcTransport {
  async call(method: string, params: unknown): Promise<unknown> {
    // 插件方法（pluginId.method）→ 包装成 web_plugin_rpc；核心方法直传。
    if (method.includes('.')) {
      return postAPI('/api/rpc', { method: 'web_plugin_rpc', params: { method, params } })
    }
    return postAPI('/api/rpc', { method, params })
  }
}
```

两个关键细节：

1. **envelope 解包** —— `/api/rpc` 返回 `{ok, data, error}` envelope；`postAPI` 负责解包。不解包的话 `res.plugins` 读到 undefined（真实数据在 `res.data.plugins`）。
2. **`web_plugin_rpc` 包装** —— 后端 RPC 表里插件方法只有一个入口（`web_plugin_rpc`），handler 内部按 `pluginId.method` 路由到对应插件进程。把插件方法名直接当外层 method 传会得到 `unknown RPC method: xbot.git-fancy.status`（表里没有这个键）。

`PluginRpcBridge` 在传输之上实现 `RPCAPI`；`notify` 吞掉失败（fire-and-forget）：

```ts
export class PluginRpcBridge implements RPCAPI {
  async call<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): Promise<BackendRPC[K]['result']> {
    return (await this.transport.call(method as string, params)) as BackendRPC[K]['result']
  }
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void {
    void this.transport.call(method as string, params).catch(() => { /* notify 失败静默 */ })
  }
}
```

## 声明合并扩展

暴露 RPC 方法的后端插件（如 Git 数据源）发布类型包合并进 `BackendRPC`。前端插件 import 后以精确类型调 `ctx.rpc.call('plugin.method', …)`——上表已展示真实的 `xbot.git-fancy` 扩展（`git.status`/`git.log`/`git.diff`/`git.branches`）。

## 会话类型

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

## 接线

宿主在 `PluginRuntime` 构造函数中构造一次桥：

```ts
this.rpc = new PluginRpcBridge(host.rpcTransport)
```

`usePluginRuntimeHost` 提供 `rpcTransport: new FetchRpcTransport()`。所有插件共享同一个桥实例——方法路由按调用、不按插件。
