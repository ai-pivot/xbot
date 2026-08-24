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
import { layoutRegistry, VIEW_CONTAINER_TO_SLOT } from '@/plugin-runtime/layoutRegistry'

/** 后端 web_plugin_list 返回的单个插件声明。 */
export interface WebPluginDecl {
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
    const comp = (mod.default ?? mod[view.id] ?? null) as unknown
    // 只接受函数组件，或带合法 $$typeof 的 memo/forwardRef 对象（React 18+ 支持）。
    // 绝不放行裸对象 —— React 渲染 `<Comp />` 时会对裸对象抛
    // "Element type is invalid... got: object"。
    if (typeof comp === 'function') return comp as React.ComponentType
    if (comp && typeof comp === 'object' && (comp as { $$typeof?: unknown }).$$typeof) {
      return comp as React.ComponentType
    }
    console.error(
      `[plugin-runtime] loadViewComponent 返回了非组件对象: plugin=${pluginId} view=${view.id} entry=${view.entry}`,
      { moduleKeys: Object.keys(mod), compType: typeof comp, comp },
    )
    return null
  } catch (error) {
    console.error(`[plugin-runtime] loadViewComponent 加载失败: plugin=${pluginId} view=${view.id} url=${url}`, error)
    return null
  }
}

// 内置视图组件：静态 import，随主 bundle 一起打包（不生成独立 chunk）。
// xbot.iteration-stats 已改为独立插件（后端 plugin.json + ESM 模块动态加载），
// 不再走 builtin: 路径。
import { PluginManagerPanel } from '@/plugins/manager/PluginManagerPanel'
import { GitStatusPanel } from '@/plugins/git-info/GitStatusPanel'
import { SkillManagerPanel } from '@/plugins/xbot-skill-manager/SkillManagerPanel'

const builtinViews = new Map<string, React.ComponentType>()
builtinViews.set('xbot.plugin-manager.panel', PluginManagerPanel)
builtinViews.set('git-info.status', GitStatusPanel)
builtinViews.set('xbot.skill-manager.panel', SkillManagerPanel)

async function loadBuiltinView(id: string): Promise<React.ComponentType | null> {
  const comp = builtinViews.get(id)
  return comp ?? null
}

/** 从后端声明构造 PluginManifest（供 registry 校验）。 */
export function toManifest(decl: WebPluginDecl): import('@/plugin-api').PluginManifest {
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
      //    xbot.iteration-stats 已改为独立插件（后端 plugin.json + ESM 模块），
      //    由 web_plugin_list 返回后走标准 activate 路径（动态 import）。
      try {
        const builtin = await import('@/plugins/manager/pluginManager')
        await runtime.activateBuiltin(builtin.manifest, builtin as unknown as import('./loader').PluginModule)
      } catch (error) {
        console.error('[plugin-runtime] 激活内置插件失败', error)
      }
      // 1b. 内置技能管理插件（同 plugin-manager 范式）。
      try {
        const skillManager = await import('@/plugins/xbot-skill-manager/skillManager')
        await runtime.activateBuiltin(
          skillManager.manifest,
          skillManager as unknown as import('./loader').PluginModule,
        )
      } catch (error) {
        console.error('[plugin-runtime] 激活内置技能管理插件失败', error)
      }
      // 2. 拉取第三方插件清单并激活（rescan=true：重新扫描磁盘发现新安装的插件）。
      //    仅激活 enabled 的插件 —— 禁用的插件（后端 State=StateInactive → enabled=false）
      //    必须跳过，否则纯前端插件在禁用后依然注册 view 并生效（严重 bug）。
      try {
        const res = await ws.rpc<{ plugins?: WebPluginDecl[] }>('web_plugin_list', { rescan: true })
        if (cancelled) return
        for (const decl of res?.plugins ?? []) {
          if (!decl.enabled) {
            console.debug(`[plugin-runtime] 跳过已禁用的插件 ${decl.id}（enabled=false）`)
            continue
          }
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

  // 同步插件 view 贡献点 → 布局注册表：每个 view 自动成为可移动布局项
  // （默认 slot 由 container 映射，用户可在布局设置中移到其他 slot）。
  // dynamic 视图（参数化动态视图）跳过——它们无静态入口，不进布局注册表。
  useEffect(() => {
    const synced = new Set<string>()
    const syncViews = () => {
      const views = runtime.listAllViews()
      const currentIds = new Set<string>()
      for (const { view } of views) {
        if (view.dynamic) continue
        currentIds.add(view.id)
        const slot = VIEW_CONTAINER_TO_SLOT[view.container] ?? 'desktop.sidebar'
        layoutRegistry.register({
          id: view.id,
          slot,
          title: view.title,
          icon: view.icon,
          weight: 100, // 插件项排在内置项之后
        })
      }
      // 注销已消失的 view 项（插件卸载/热加载移除贡献点时）。
      for (const id of synced) {
        if (!currentIds.has(id)) layoutRegistry.unregister(id)
      }
      synced.clear()
      for (const id of currentIds) synced.add(id)
    }
    syncViews()
    const unsub = runtime.subscribeViews(syncViews)
    return () => {
      unsub()
      for (const id of synced) layoutRegistry.unregister(id)
    }
  }, [runtime])

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
