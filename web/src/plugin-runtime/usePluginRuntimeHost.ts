/**
 * usePluginRuntimeHost —— 把 PluginRuntime 接到宿主（AppShell）。
 *
 * 职责：
 *  - usePluginRuntimeHost() 构造 PluginRuntimeHost（不依赖 runtime 实例）
 *  - PluginRuntimeBootstrap 组件在 Provider 内启动时拉取 web_plugin_list 并激活，
 *    监听 WS 的 web_plugin_init/deactivate/event/push 驱动热加载/卸载
 *
 * 单一门控：激活前 registry.validate 校验贡献点（后端只做传输层检查）。
 */
import { createElement, Fragment, useCallback, useEffect, useMemo, useRef } from 'react'

import type { ViewContribution } from '@/plugin-api'
import type { MessageRendererContribution } from '@/plugin-api'
import type { SessionSummary } from '@/plugin-api'

import { useWSConnection } from '@/hooks/useWSConnection'
import { useSessionStore } from '@/hooks/useSessionStore'
import { PluginRuntimeProvider, usePluginRuntime, type PluginRuntimeHost } from '@/plugin-runtime'
import { FetchRpcTransport } from '@/plugin-runtime/rpc'

/** 后端 web_plugin_list 返回的单个插件声明。 */
interface WebPluginDecl {
  id: string
  name: string
  version: string
  state: string
  enabled: boolean
  permissions: string[]
  entry: string
  module_url: string
  contributes?: unknown
}

/** 视图组件动态 import（第三方插件模块经 versioned URL 加载）。 */
async function loadPluginViewComponent(
  pluginId: string,
  view: ViewContribution,
): Promise<React.ComponentType | null> {
  if (!view.entry) return null
  // 内置视图标记：随前端分发的静态组件（自举插件管理面板等）。
  // 注意：内置视图必须静态 import（而非动态 import）打包进主 bundle，
  // 否则 rolldown 会把它们的 useState 等 hook 符号错误绑定到 framer-motion
  // 的导出，触发 React #311（hooks 数量不一致）。
  if (view.entry.startsWith('builtin:')) {
    return loadBuiltinView(view.entry.slice('builtin:'.length))
  }
  const base = `/plugins/${pluginId}/web`
  const url = view.entry.startsWith('/') ? view.entry : `${base}/${view.entry}`
  try {
    const mod = await import(/* @vite-ignore */ `${url}?view=${encodeURIComponent(view.id)}`)
    const comp = mod.default ?? mod[view.id] ?? null
    return typeof comp === 'function' || (comp && typeof comp === 'object') ? (comp as React.ComponentType) : null
  } catch {
    return null
  }
}

// 内置视图组件：静态 import，随主 bundle 一起打包（不生成独立 chunk）。
import { PluginManagerPanel } from '@/plugins/manager/PluginManagerPanel'
import { GitStatusPanel } from '@/plugins/git-info/GitStatusPanel'

const builtinViews = new Map<string, React.ComponentType>()
builtinViews.set('xbot.plugin-manager.panel', PluginManagerPanel)
builtinViews.set('git-info.status', GitStatusPanel)

async function loadBuiltinView(id: string): Promise<React.ComponentType | null> {
  const comp = builtinViews.get(id)
  return comp ?? null
}

/** 从后端声明构造 PluginManifest（供 registry 校验）。 */
function toManifest(decl: WebPluginDecl): import('@/plugin-api').PluginManifest {
  const contributes = Array.isArray(decl.contributes)
    ? (decl.contributes as import('@/plugin-api').Contribution[])
    : []
  return {
    id: decl.id,
    name: decl.name,
    version: decl.version,
    permissions: (decl.permissions as import('@/plugin-api').Permission[]) ?? [],
    contributes,
    entry: decl.entry,
  }
}

/** 构造 PluginRuntimeHost（不依赖 runtime 实例，无循环依赖）。 */
export function usePluginRuntimeHost(): PluginRuntimeHost {
  const ws = useWSConnection()
  const session = useSessionStore()

  const getSession = useCallback((): SessionSummary | null => {
    const active = session.activeSession
    if (!active) return null
    return {
      chatID: active.chatID,
      title: active.chatID,
      model: '',
      busy: false,
      maxContext: 0,
      tokenUsage: { prompt: 0, completion: 0 },
    }
  }, [session.activeSession])

  const getMessagesRaw = useCallback((): readonly unknown[] => [], [])

  const getBackendPlugins = useCallback(async () => {
    try {
      const res = await ws.rpc<{ plugins?: WebPluginDecl[] }>('web_plugin_list', {})
      return (res?.plugins ?? []).map((p) => ({ id: p.id, version: p.version, enabled: p.enabled }))
    } catch {
      return []
    }
  }, [ws])

  return useMemo<PluginRuntimeHost>(
    () => ({
      rpcTransport: new FetchRpcTransport(),
      moduleBaseUrl: (pluginId: string) => `/plugins/${pluginId}/web`,
      loadViewComponent: (pluginId, view) => loadPluginViewComponent(pluginId, view),
      ui: {
        showToast: () => {},
        openPanel: () => {},
        closePanel: () => {},
      },
      getSession,
      getMessagesRaw,
      getBackendPlugins,
      // 声明式贡献点渲染走 ViewSlot 查询 registry；mount 回调为 no-op（Phase 3
      // MessageRenderer 调度器接入时 mountRenderer 才有真实逻辑）。
      mountView: () => () => {},
      mountRenderer: (_r: MessageRendererContribution) => () => {},
      mountCommand: () => () => {},
    }),
    [getSession, getMessagesRaw, getBackendPlugins],
  )
}

/**
 * 启动时拉取插件清单并激活；监听 WS 消息驱动热加载/卸载。
 * 必须放在 PluginRuntimeProvider 内部（用 usePluginRuntime 拿实例）。
 */
export function PluginRuntimeBootstrap() {
  const runtime = usePluginRuntime()
  const ws = useWSConnection()
  const bootstrapped = useRef(false)

  // 启动拉取清单并激活（先内置插件，后第三方插件）。
  useEffect(() => {
    if (bootstrapped.current) return
    bootstrapped.current = true
    let cancelled = false
    ;(async () => {
      // 1. 激活内置插件（随前端分发，静态 import，不走 URL）。
      try {
        const builtin = await import('@/plugins/manager/pluginManager')
        await runtime.activateBuiltin(builtin.manifest, builtin as unknown as import('./loader').PluginModule)
      } catch (error) {
        console.error('[plugin-runtime] 激活内置插件失败', error)
      }
      // 2. 拉取第三方插件清单并激活。
      try {
        const res = await ws.rpc<{ plugins?: WebPluginDecl[] }>('web_plugin_list', {})
        if (cancelled) return
        for (const decl of res?.plugins ?? []) {
          const manifest = toManifest(decl)
          await runtime.activate(manifest, decl.module_url)
        }
      } catch (error) {
        console.error('[plugin-runtime] 拉取插件清单失败', error)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [runtime, ws])

  // 监听 WS：web_plugin_init（热加载）/ deactivate（卸载）/ event / push。
  useEffect(() => {
    const off = ws.onMessage((msg) => {
      if (msg.type === 'web_plugin_init') {
        try {
          const decl = JSON.parse(msg.content ?? '{}') as WebPluginDecl & { module_url?: string }
          const manifest = toManifest(decl)
          void runtime.activate(manifest, decl.module_url)
        } catch (error) {
          console.error('[plugin-runtime] web_plugin_init 解析失败', error)
        }
      } else if (msg.type === 'web_plugin_deactivate') {
        try {
          const decl = JSON.parse(msg.content ?? '{}') as { plugin_id?: string }
          if (decl.plugin_id) runtime.deactivate(decl.plugin_id)
        } catch {
          /* ignore */
        }
      } else if (msg.type === 'web_plugin_event') {
        try {
          const evt = JSON.parse(msg.content ?? '{}') as { name?: string; payload?: unknown }
          if (evt.name) {
            // 后端事件载荷是运行时 JSON，类型在插件订阅侧由 EventMap 保证；
            // 这里以 unknown 擦除后投递（与 PluginEventBus.emit 的宽类型存储一致）。
            runtime.events.emit(evt.name as keyof import('@/plugin-api').EventMap, evt.payload as never)
          }
        } catch {
          /* ignore */
        }
      }
    })
    return off
  }, [runtime, ws])

  return null
}

/**
 * PluginRuntimeRoot —— 插件运行时根组件。
 *
 * 组装链路：usePluginRuntimeHost() → PluginRuntimeProvider → PluginRuntimeBootstrap。
 * 放在 WSProvider + SessionStoreProvider 内部、AppShell 外部。
 */
export function PluginRuntimeRoot({ children }: { children: React.ReactNode }) {
  const host = usePluginRuntimeHost()
  return createElement(
    PluginRuntimeProvider,
    { host, children: createElement(Fragment, null, createElement(PluginRuntimeBootstrap), children) },
  )
}
