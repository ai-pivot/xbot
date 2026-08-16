/**
 * PluginManagerPanel —— 内置插件管理面板视图（自举）。
 *
 * 通过 usePluginRuntime() 访问运行时（这是公开的宿主消费方式，不是插件 API，
 * 而是宿主组件消费 PluginRuntime 的方式——面板作为内置插件贡献的 view，
 * 由宿主 ViewSlot 渲染，所以它能拿到 runtime）。
 *
 * 数据源：runtime.registry.listStates() —— 前端**已激活**的插件（含内置
 * plugin-manager / git-info 与第三方前端插件）。内置插件随主 bundle 分发、
 * 静态 import，只存在于前端 registry，**不在后端 Go 插件列表里**，所以
 * 面板必须读前端 registry 而非后端 plugin_status RPC（那是纯后端插件）。
 */
import { useCallback, useEffect, useState } from 'react'
import { usePluginRuntime } from '@/plugin-runtime'
import type { PluginRuntimeState } from '@/plugin-runtime/registry'

export function PluginManagerPanel() {
  const runtime = usePluginRuntime()
  const [states, setStates] = useState<PluginRuntimeState[]>([])

  const refresh = useCallback(() => {
    setStates(runtime.registry.listStates())
  }, [runtime])

  useEffect(() => {
    refresh()
    // 插件状态变化（注册/卸载/热加载）时刷新列表。
    return runtime.registry.subscribeViews(refresh)
  }, [refresh, runtime])

  const doReload = async () => {
    // 前端插件无独立重载 RPC；刷新列表即可反映运行时最新状态。
    refresh()
  }

  const doUninstall = async (id: string) => {
    runtime.deactivate(id)
    refresh()
  }

  return (
    <div className="flex h-full flex-col gap-2 p-3 text-xs">
      <div className="flex items-center justify-between">
        <span className="font-semibold uppercase tracking-wide text-text-secondary">插件</span>
        <button
          onClick={refresh}
          className="rounded border border-border px-2 py-1 text-text-secondary hover:bg-bg-hover"
        >
          刷新
        </button>
      </div>

      <div className="flex flex-1 flex-col gap-2 overflow-y-auto">
        {states.length === 0 && <div className="text-text-muted">暂无插件</div>}
        {states.map((s) => {
          const isActive = s.enabled
          return (
            <div
              key={s.id}
              className="rounded border border-border bg-bg-elevated p-2"
            >
              <div className="flex items-center gap-2">
                <span className={`inline-block h-2 w-2 rounded-full ${isActive ? 'bg-green-500' : 'bg-slate-400'}`} />
                <span className="truncate font-medium">{s.name}</span>
                <span className="ml-auto text-[10px] text-text-muted">v{s.version}</span>
              </div>
              <div className="mt-1 truncate text-[10px] text-text-muted">{s.id}</div>
              <div className="mt-2 flex gap-1.5">
                <button
                  onClick={() => void doReload()}
                  className="rounded border border-border px-2 py-0.5 text-text-secondary hover:bg-bg-hover"
                >
                  重载
                </button>
                <button
                  onClick={() => void doUninstall(s.id)}
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
