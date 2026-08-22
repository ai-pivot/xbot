/**
 * RPC 桥——前端插件调用后端方法（JSON-RPC 2.0 走现有 /api/rpc 通道）。
 *
 * 类型由 @xbot/plugin-api 的 BackendRPC 方法表驱动；运行时转发到现有
 * `ws.rpc` / fetch('/api/rpc')。方法名带 `pluginId.` 前缀的由后端路由到
 * 对应后端插件进程。
 */
import type { BackendRPC, RPCAPI } from '@/plugin-api'
import { postAPI } from '@/lib/api'

export interface RpcTransport {
  call(method: string, params: unknown): Promise<unknown>
}

/**
 * 基于现有 /api/rpc fetch 通道的传输（不依赖 WS 连接就绪）。
 *
 * 后端 /api/rpc 返回 `{ok, data, error}` envelope，这里复用 postAPI 做
 * envelope 解包——否则返回整个 envelope，`res.plugins` 会读到 undefined
 * （真实数据在 `res.data.plugins`）。
 *
 * 插件方法（method 含 `.` 前缀，如 `xbot.git-fancy.status`）必须包装成
 * `web_plugin_rpc` 调用：后端 RPC 表里只有 `web_plugin_rpc` 这一个入口，
 * handler 内部再按 `pluginId.method` 路由到对应插件进程。直接把插件方法名
 * 当外层 method 传会导致后端 `unknown RPC method: xbot.git-fancy.status`
 * （RPC 表里没有这个键）。
 */
export class FetchRpcTransport implements RpcTransport {
  async call(method: string, params: unknown): Promise<unknown> {
    // 插件方法（pluginId.method）→ 包装成 web_plugin_rpc；核心方法直传。
    if (method.includes('.')) {
      return postAPI('/api/rpc', { method: 'web_plugin_rpc', params: { method, params } })
    }
    return postAPI('/api/rpc', { method, params })
  }
}

/** 实现 RPCAPI 的桥接对象。 */
export class PluginRpcBridge implements RPCAPI {
  private readonly transport: RpcTransport

  constructor(transport: RpcTransport) {
    this.transport = transport
  }

  async call<K extends keyof BackendRPC>(
    method: K,
    params: BackendRPC[K]['params'],
  ): Promise<BackendRPC[K]['result']> {
    return (await this.transport.call(method as string, params)) as BackendRPC[K]['result']
  }

  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void {
    void this.transport.call(method as string, params).catch(() => {
      /* notify 失败静默 */
    })
  }
}
