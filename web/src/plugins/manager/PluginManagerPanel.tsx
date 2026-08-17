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
import { AnimatePresence, motion } from 'framer-motion'
import { Loader2 } from 'lucide-react'
import { usePluginRuntime } from '@/plugin-runtime'
import { toManifest, type WebPluginDecl } from '@/plugin-runtime/usePluginRuntimeHost'
import { installPluginFile } from '@/components/agent/api'
import { Skeleton } from '@/components/ui/skeleton'

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

type PendingAction = 'toggle' | 'reload'

export function PluginManagerPanel() {
  const runtime = usePluginRuntime()
  const [backendPlugins, setBackendPlugins] = useState<BackendPlugin[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [installing, setInstalling] = useState(false)
  // 每个插件的行级 pending 操作（key: plugin id → 进行中的动作）
  const [pending, setPending] = useState<Record<string, PendingAction>>({})
  const fileInputRef = useRef<HTMLInputElement>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      // rescan=true：重新扫描磁盘插件目录 + 重新激活（发现新安装的插件）。
      const res = await runtime.rpc.call('plugin_status' as never, { rescan: true } as never) as unknown as PluginStatusResponse
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
    setPending((p) => ({ ...p, [id]: 'reload' }))
    try {
      await runtime.rpc.call('plugin_reload' as never, { id } as never)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setPending((p) => {
        const next = { ...p }
        delete next[id]
        return next
      })
    }
  }

  const doToggleEnabled = async (id: string, enabled: boolean) => {
    setPending((p) => ({ ...p, [id]: 'toggle' }))
    try {
      await runtime.rpc.call('plugin_set_enabled' as never, { id, enabled } as never)
      // 主动同步前端 runtime（不依赖 WS web_plugin_init 广播——广播链路不可靠）。
      // 拉最新 web_plugin_list，对该插件 activate / deactivate。
      try {
        const res = await runtime.rpc.call('web_plugin_list' as never, {} as never) as { plugins?: WebPluginDecl[] }
        const decl = res?.plugins?.find((p) => p.id === id)
        if (decl) {
          if (enabled && decl.enabled) {
            await runtime.activate(toManifest(decl), decl.module_url)
          } else if (!enabled) {
            runtime.deactivate(id)
          }
        }
      } catch (syncErr) {
        console.error(`[plugin-manager] 同步前端插件 ${id} 激活状态失败`, syncErr)
      }
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setPending((p) => {
        const next = { ...p }
        delete next[id]
        return next
      })
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
            className="flex items-center gap-1.5 rounded border border-border px-2 py-1 text-text-secondary hover:bg-bg-hover disabled:opacity-50"
          >
            {installing && <Loader2 className="h-3 w-3 animate-spin" />}
            {installing ? '安装中…' : '安装'}
          </button>
          <button
            onClick={() => void refresh()}
            disabled={loading}
            className="flex items-center gap-1.5 rounded border border-border px-2 py-1 text-text-secondary hover:bg-bg-hover disabled:opacity-50"
          >
            {loading && <Loader2 className="h-3 w-3 animate-spin" />}
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

      {error && (
        <motion.div
          initial={{ opacity: 0, y: -4 }}
          animate={{ opacity: 1, y: 0 }}
          className="rounded border border-red-200 bg-red-50 p-2 text-red-600"
        >
          {error}
        </motion.div>
      )}

      <div className="flex flex-1 flex-col gap-2 overflow-y-auto">
        {loading ? (
          // 加载骨架屏：列表项结构占位，避免空白等待
          Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="rounded border border-border bg-bg-elevated p-2">
              <div className="flex items-center gap-2">
                <Skeleton className="h-2 w-2 rounded-full" />
                <Skeleton className="h-3 w-24" />
                <Skeleton className="ml-auto h-2 w-8" />
              </div>
              <Skeleton className="mt-2 h-2 w-32" />
              <div className="mt-2 flex gap-1.5">
                <Skeleton className="h-5 w-12 rounded" />
                <Skeleton className="h-5 w-12 rounded" />
              </div>
            </div>
          ))
        ) : backendPlugins.length === 0 ? (
          <div className="py-6 text-center text-text-muted">暂无插件</div>
        ) : (
          <AnimatePresence initial={false}>
            {backendPlugins.map((p) => {
              const isActive = p.state === 'active'
              const isPending = pending[p.id]
              return (
                <motion.div
                  key={p.id}
                  layout
                  initial={{ opacity: 0, y: 8 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={{ opacity: 0, scale: 0.98 }}
                  transition={{ duration: 0.18, ease: 'easeOut' }}
                  className="rounded border border-border bg-bg-elevated p-2"
                >
                  <div className="flex items-center gap-2">
                    <motion.span
                      animate={{ opacity: isPending ? 0.4 : 1 }}
                      className={`inline-block h-2 w-2 rounded-full ${isActive ? 'bg-green-500' : 'bg-slate-400'}`}
                    />
                    <span className="truncate font-medium">{p.name}</span>
                    <span className="ml-auto text-[10px] text-text-muted">v{p.version}</span>
                  </div>
                  <div className="mt-1 truncate text-[10px] text-text-muted">{p.id}</div>
                  <div className="mt-2 flex gap-1.5">
                    <button
                      onClick={() => void doToggleEnabled(p.id, !isActive)}
                      disabled={isPending === 'toggle'}
                      className={isActive
                        ? 'flex items-center gap-1 rounded border border-red-200 px-2 py-0.5 text-red-600 hover:bg-red-50 disabled:opacity-50'
                        : 'flex items-center gap-1 rounded border border-green-200 px-2 py-0.5 text-green-600 hover:bg-green-50 disabled:opacity-50'}
                    >
                      {isPending === 'toggle' && <Loader2 className="h-3 w-3 animate-spin" />}
                      {isPending === 'toggle' ? '处理中…' : (isActive ? '禁用' : '启用')}
                    </button>
                    <button
                      onClick={() => void doReload(p.id)}
                      disabled={isPending === 'reload'}
                      className="flex items-center gap-1 rounded border border-border px-2 py-0.5 text-text-secondary hover:bg-bg-hover disabled:opacity-50"
                    >
                      {isPending === 'reload' && <Loader2 className="h-3 w-3 animate-spin" />}
                      {isPending === 'reload' ? '重载中…' : '重载'}
                    </button>
                  </div>
                </motion.div>
              )
            })}
          </AnimatePresence>
        )}
      </div>
    </div>
  )
}

export default PluginManagerPanel
