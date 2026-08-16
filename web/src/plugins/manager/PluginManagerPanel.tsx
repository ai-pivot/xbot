/**
 * PluginManagerPanel —— 内置插件管理面板视图（自举）。
 *
 * 通过 usePluginRuntime() 访问运行时（这是公开的宿主消费方式，不是插件 API，
 * 而是宿主组件消费 PluginRuntime 的方式——面板作为内置插件贡献的 view，
 * 由宿主 ViewSlot 渲染，所以它能拿到 runtime）。
 *
 * 数据源：
 *   - runtime.registry.listStates() —— 已激活插件状态
 *   - runtime.rpc.call('plugin_status') —— 后端全部插件状态
 * 操作：重载/禁用/卸载 → 后端 RPC + 前端热加载。
 */
import { useCallback, useEffect, useState } from 'react'
import { usePluginRuntime } from '@/plugin-runtime'

interface BackendPlugin {
  id: string
  name: string
  version: string
  state: string
  runtime?: string
}

interface PluginStatusResponse {
  plugins?: BackendPlugin[]
  active?: number
  total?: number
}

export function PluginManagerPanel() {
  const runtime = usePluginRuntime()
  const [backendPlugins, setBackendPlugins] = useState<BackendPlugin[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await runtime.rpc.call('plugin_status' as never, {} as never) as unknown as PluginStatusResponse
      setBackendPlugins(res?.plugins ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [runtime])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const activeIds = new Set(runtime.registry.listStates().map((s) => s.id))

  const doReload = async (id: string) => {
    try {
      await runtime.rpc.call('plugin_reload' as never, { id } as never)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const doUninstall = async (id: string) => {
    try {
      await runtime.rpc.call('plugin_uninstall' as never, { id } as never)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div className="flex h-full flex-col gap-2 p-3 text-xs">
      <div className="flex items-center justify-between">
        <span className="font-semibold uppercase tracking-wide text-text-secondary">插件</span>
        <button
          onClick={() => void refresh()}
          className="rounded border border-border px-2 py-1 text-text-secondary hover:bg-bg-hover"
        >
          {loading ? '刷新中…' : '刷新'}
        </button>
      </div>

      {error && <div className="rounded border border-red-200 bg-red-50 p-2 text-red-600">{error}</div>}

      <div className="flex flex-1 flex-col gap-2 overflow-y-auto">
        {backendPlugins.length === 0 && !loading && (
          <div className="text-text-muted">暂无插件</div>
        )}
        {backendPlugins.map((p) => {
          const isActive = activeIds.has(p.id)
          return (
            <div
              key={p.id}
              className="rounded border border-border bg-bg-elevated p-2"
            >
              <div className="flex items-center gap-2">
                <span className={`inline-block h-2 w-2 rounded-full ${isActive ? 'bg-green-500' : 'bg-slate-400'}`} />
                <span className="truncate font-medium">{p.name}</span>
                <span className="ml-auto text-[10px] text-text-muted">v{p.version}</span>
              </div>
              <div className="mt-1 truncate text-[10px] text-text-muted">{p.id}</div>
              <div className="mt-2 flex gap-1.5">
                <button
                  onClick={() => void doReload(p.id)}
                  className="rounded border border-border px-2 py-0.5 text-text-secondary hover:bg-bg-hover"
                >
                  重载
                </button>
                <button
                  onClick={() => void doUninstall(p.id)}
                  className="rounded border border-red-200 px-2 py-0.5 text-red-600 hover:bg-red-50"
                >
                  卸载
                </button>
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

export default PluginManagerPanel
