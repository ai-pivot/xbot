/**
 * RPC 桥——前端插件调用后端方法（JSON-RPC 2.0 走现有 /api/rpc 通道）。
 *
 * 类型由 @xbot/plugin-api 的 BackendRPC 方法表驱动；运行时转发到现有
 * `ws.rpc` / fetch('/api/rpc')。方法名带 `pluginId.` 前缀的由后端路由到
 * 对应后端插件进程。
 */
import type { BackendRPC, RPCAPI } from '@/plugin-api'

export interface RpcTransport {
  call(method: string, params: unknown): Promise<unknown>
}

/** 基于现有 /api/rpc fetch 通道的传输（不依赖 WS 连接就绪）。 */
export class FetchRpcTransport implements RpcTransport {
  async call(method: string, params: unknown): Promise<unknown> {
    const res = await fetch('/api/rpc', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ method, params }),
    })
    if (!res.ok) throw new Error(`RPC ${method} 失败: HTTP ${res.status}`)
    return (await res.json()) as unknown
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
