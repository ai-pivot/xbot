/**
 * PluginManagerPanel —— 内置插件管理面板视图（自举）。
 *
 * 通过 usePluginRuntime() 访问运行时（公开的宿主消费方式）。
 *
 * 数据源：后端 `plugin_status` RPC（pm.ListPlugins()）——返回**后端 Go
 * 插件系统**的全部插件（script/grpc runtime，如 daily-jokes、dashboard、
 * git-info、github、theme-party 等）。这与前端 Web 插件 v2（registry 里
 * 的 plugin-manager）是两套系统；本面板展示后端插件全貌。
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { usePluginRuntime } from '@/plugin-runtime'
import { installPluginFile } from '@/components/agent/api'

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
  const [installing, setInstalling] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

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

  const doReload = async (id: string) => {
    try {
      await runtime.rpc.call('plugin_reload' as never, { id } as never)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const doToggleEnabled = async (id: string, enabled: boolean) => {
    try {
      await runtime.rpc.call('plugin_set_enabled' as never, { id, enabled } as never)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const onPickZip = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    setInstalling(true)
    setError(null)
    try {
      await installPluginFile(file)
      await refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setInstalling(false)
    }
  }

  return (
    <div className="flex h-full flex-col gap-2 p-3 text-xs">
      <div className="flex items-center justify-between">
        <span className="font-semibold uppercase tracking-wide text-text-secondary">插件</span>
        <div className="flex items-center gap-1.5">
          <button
            onClick={() => fileInputRef.current?.click()}
            disabled={installing}
            className="rounded border border-border px-2 py-1 text-text-secondary hover:bg-bg-hover disabled:opacity-50"
          >
            {installing ? '安装中…' : '安装'}
          </button>
          <button
            onClick={() => void refresh()}
            className="rounded border border-border px-2 py-1 text-text-secondary hover:bg-bg-hover"
          >
            {loading ? '刷新中…' : '刷新'}
          </button>
          <input
            ref={fileInputRef}
            type="file"
            accept=".zip"
            className="hidden"
            onChange={onPickZip}
          />
        </div>
      </div>

      {error && <div className="rounded border border-red-200 bg-red-50 p-2 text-red-600">{error}</div>}

      <div className="flex flex-1 flex-col gap-2 overflow-y-auto">
        {backendPlugins.length === 0 && !loading && (
          <div className="text-text-muted">暂无插件</div>
        )}
        {backendPlugins.map((p) => {
          const isActive = p.state === 'active'
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
                  onClick={() => void doToggleEnabled(p.id, !isActive)}
                  className={isActive
                    ? 'rounded border border-red-200 px-2 py-0.5 text-red-600 hover:bg-red-50'
                    : 'rounded border border-green-200 px-2 py-0.5 text-green-600 hover:bg-green-50'}
                >
                  {isActive ? '禁用' : '启用'}
                </button>
                <button
                  onClick={() => void doReload(p.id)}
                  className="rounded border border-border px-2 py-0.5 text-text-secondary hover:bg-bg-hover"
                >
                  重载
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
