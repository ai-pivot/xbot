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
 */
export class FetchRpcTransport implements RpcTransport {
  async call(method: string, params: unknown): Promise<unknown> {
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
