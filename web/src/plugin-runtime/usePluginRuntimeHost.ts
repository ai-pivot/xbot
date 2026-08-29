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
import { panelRegistry, buildPanelDefs } from '@/plugin-runtime/panelRegistry'
import { PluginView } from '@/plugin-runtime/PluginView'

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

// ── 插件模块热重载 cache-bust ────────────────────────────────────────────────
// 每次 activate（启动 + web_plugin_init 热加载/reload）bump 一个 per-plugin
// token，view 组件 import URL 带上它。关键机制：浏览器 ES module map 以完整
// URL 为缓存键 —— reload 后 URL 变化 → module map miss → 发网络请求 → 拿到
// 磁盘上的最新 index.js。没有它，热重载后 URL 不变 → module map 命中旧模块
// （连请求都不发）→ 无论磁盘怎么更新、按钮点多少次，前端永远跑旧代码。
// 同一会话内不重载则 token 不变 → URL 稳定 → module map 命中，不重复请求。
const pluginLoadTokens = new Map<string, string>()

function bumpPluginLoadToken(pluginId: string): void {
  pluginLoadTokens.set(pluginId, Date.now().toString(36))
}

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
  // cache-bust token：activate/reload 时 bump（见 pluginLoadTokens 注释）。
  // 无 token（异常路径）时退化为无参数 URL，行为同旧版。
  const token = pluginLoadTokens.get(pluginId)
  const bust = token ? `&_t=${token}` : ''
  try {
    const mod = await import(/* @vite-ignore */ `${url}?view=${encodeURIComponent(view.id)}${bust}`)
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
import { SessionStatsPanel } from '@/plugins/session-stats/SessionStatsPanel'

const builtinViews = new Map<string, React.ComponentType>()
builtinViews.set('xbot.plugin-manager.panel', PluginManagerPanel)
builtinViews.set('git-info.status', GitStatusPanel)
builtinViews.set('xbot.skill-manager.panel', SkillManagerPanel)
builtinViews.set('xbot.session-stats.panel', SessionStatsPanel)

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

  // useRef 让 getSession 始终读最新 activeSession，而不产生新闭包 → host 对象
  // 稳定（useMemo deps 不变）→ PluginRuntime 持有同一 host 引用，ctx.state.getSession()
  // 永远返回当前会话（而非创建时的初始会话）。
  const activeSessionRef = useRef(session.activeSession)
  activeSessionRef.current = session.activeSession

  const getSession = useCallback((): SessionSummary | null => {
    const active = activeSessionRef.current
    if (!active) return null
    return {
      chatID: active.chatID,
      title: active.chatID,
      model: '',
      busy: false,
      maxContext: 0,
      tokenUsage: { prompt: 0, completion: 0 },
    }
  }, [])

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
    // 所有依赖稳定（useCallback 空依赖或稳定引用），host 只创建一次。
    // getSession 用 ref 读最新值，无需在 deps 里跟 activeSession 变化。
    [getSession, getMessagesRaw, getBackendPlugins],
  )
}

/**
 * 启动时拉取插件清单并激活；监听 WS 消息驱动热加载/卸载。
 * 必须放在 PluginRuntimeProvider 内部（用 usePluginRuntime 拿实例）。
 *
 * 并行激活策略（对标 VSCode 扩展并行加载）：
 *  - 内置插件（pluginManager、skillManager）并行 import + activate
 *  - 第三方插件清单 RPC 与内置插件激活并行发起
 *  - 第三方插件之间并行 activate（不串行 await）
 * 主 view（AppShell）与插件激活天然并行（兄弟节点，React 同时 render）。
 */
export function PluginRuntimeBootstrap() {
  const runtime = usePluginRuntime()
  const ws = useWSConnection()
  const bootstrapped = useRef(false)

  // 启动拉取清单并激活（内置 + 第三方并行）。
  useEffect(() => {
    if (bootstrapped.current) return
    bootstrapped.current = true
    let cancelled = false

    // 内置插件激活（并行）：插件管理 + 技能管理 + 氛围壁纸，同时 import + activate。
    // ambience 插件激活后读取 manifest 的 AmbienceContribution 注册壁纸预设。
    const activateBuiltins = Promise.allSettled([
      (async () => {
        try {
          const builtin = await import('@/plugins/manager/pluginManager')
          await runtime.activateBuiltin(builtin.manifest, builtin as unknown as import('./loader').PluginModule)
        } catch (error) {
          console.error('[plugin-runtime] 激活内置插件失败', error)
        }
      })(),
      (async () => {
        try {
          const skillManager = await import('@/plugins/xbot-skill-manager/skillManager')
          await runtime.activateBuiltin(
            skillManager.manifest,
            skillManager as unknown as import('./loader').PluginModule,
          )
        } catch (error) {
          console.error('[plugin-runtime] 激活内置技能管理插件失败', error)
        }
      })(),
      (async () => {
        try {
          const ambience = await import('@/plugins/xbot-ambience')
          await runtime.activateBuiltin(
            ambience.manifest,
            ambience as unknown as import('./loader').PluginModule,
          )
          // 激活后从 manifest 读取壁纸预设注册到 ambienceStore。
          const contrib = ambience.manifest.contributes.find(
            (c) => c.kind === 'ambience',
          )
          if (contrib && 'wallpapers' in contrib) {
            const { ambienceStore } = await import('@/ambience/store')
            ambienceStore.syncPluginWallpapers([
              {
                pluginId: 'xbot.ambience',
                presets: contrib.wallpapers ?? [],
              },
            ])
          }
        } catch (error) {
          console.error('[plugin-runtime] 激活内置氛围插件失败', error)
        }
      })(),
      (async () => {
        try {
          const sessionStats = await import('@/plugins/session-stats/sessionStats')
          await runtime.activateBuiltin(
            sessionStats.manifest,
            sessionStats as unknown as import('./loader').PluginModule,
          )
        } catch (error) {
          console.error('[plugin-runtime] 激活内置会话统计插件失败', error)
        }
      })(),
    ])

    // 第三方插件清单 RPC（与内置插件激活并行发起）。
    const fetchThirdParty = ws
      .rpc<{ plugins?: WebPluginDecl[] }>('web_plugin_list', { rescan: true })
      .then((res) => res?.plugins ?? [])
      .catch((error) => {
        console.error('[plugin-runtime] 拉取插件清单失败', error)
        return [] as WebPluginDecl[]
      })

    // 内置插件激活完成 + 第三方清单就绪后，并行激活所有第三方插件。
    Promise.allSettled([activateBuiltins, fetchThirdParty]).then(([, fetchResult]) => {
      if (cancelled) return
      const plugins =
        fetchResult.status === 'fulfilled'
          ? (fetchResult.value as WebPluginDecl[])
          : []

      // 并行激活所有 enabled 的第三方插件（不串行 await）。
      // 仅激活 enabled 的插件 —— 禁用的插件（后端 State=StateInactive → enabled=false）
      // 必须跳过，否则纯前端插件在禁用后依然注册 view 并生效（严重 bug）。
      const activations = plugins
        .filter((decl) => decl.enabled)
        .map((decl) => {
          bumpPluginLoadToken(decl.id)
          const manifest = toManifest(decl)
          return runtime.activate(manifest, decl.module_url).catch((error) => {
            console.error(`[plugin-runtime] 激活第三方插件 ${decl.id} 失败:`, error)
          })
        })
      for (const decl of plugins) {
        if (!decl.enabled) {
          console.debug(`[plugin-runtime] 跳过已禁用的插件 ${decl.id}（enabled=false）`)
        }
      }
      return Promise.allSettled(activations)
    })

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
          // 热加载/reload：bump token 使 view 组件 import URL 变化，穿破浏览器
          // ES module map + HTTP 缓存 —— 否则 URL 不变时 module map 直接命中旧
          // 模块（不发请求），磁盘更新多少次前端都跑旧代码（热重载失效根因）。
          bumpPluginLoadToken(decl.id)
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
      } else if (msg.type === 'web_plugin_config_changed') {
        try {
          const evt = JSON.parse(msg.content ?? '{}') as {
            plugin_id?: string
            value?: Record<string, unknown>
          }
          if (evt.plugin_id) {
            runtime.notifyPluginConfigChanged(evt.plugin_id, evt.value ?? {})
          }
        } catch {
          /* ignore */
        }
      }
    })
    return off
  }, [runtime, ws])

  // 会话切换通知（通用机制，对标 VSCode onDidChangeActiveEditor）：
  // 1. 更新 window.__xbot_session__（插件 RPC 注入 cwd 的数据源，比 AgentPanel
  //    useEffect 更快——activeSession 变化即更新，不等 AgentPanel mount）
  // 2. 发射 session.switched 事件——任何插件可通过 ctx.events.on('session.switched')
  //    订阅，刷新会话相关数据（git 状态、文件树等），无需轮询或全局变量 hack
  const session = useSessionStore()
  const prevSessionKey = useRef<string | null>(null)
  useEffect(() => {
    const active = session.activeSession
    if (!active) return
    const key = `${active.channel}:${active.chatID}`
    if (prevSessionKey.current === key) return
    prevSessionKey.current = key

    // 更新全局 session（插件 RPC 通过 resolveChat() 读取此值注入 cwd）
    const w = window as unknown as { __xbot_session__?: { channel: string; chatID: string } }
    w.__xbot_session__ = { channel: active.channel ?? 'web', chatID: active.chatID }

    // 发射通用 session.switched 事件——插件通过 ctx.events.on('session.switched', ...) 订阅
    const summary: SessionSummary = {
      chatID: active.chatID,
      title: active.chatID,
      model: '',
      busy: false,
      maxContext: 0,
      tokenUsage: { prompt: 0, completion: 0 },
    }
    runtime.events.emit('session.switched', { session: summary })
  }, [session.activeSession, runtime])

  // 同步插件 view 贡献点 → 布局注册表 / 面板注册表：
  // - 面板类容器（right_sidebar 等，含未知兜底）→ zone 'side' 主面板：进
  //   panelRegistry（docked 语义）+ layoutRegistry（可移动布局项）。
  // - bar 类容器（status_bar_right 等）→ 徽章形态：同 pluginId 另有主 view
  //   时合并为主面板的 badgeRender（同 panelId），否则注册为独立徽章面板
  //   （zone 'top'/segment 'right'）——徽章面板不进 layoutRegistry（非侧栏
  //   堆叠项），旧直渲染点经 usePluginViewPanels 查询返回空（见该 shim）。
  // 通用 container 语义规则（buildPanelDefs 数据表驱动），框架零插件特化。
  // dynamic 视图（参数化动态视图）跳过——它们无静态入口，不进布局注册表。
  useEffect(() => {
    const syncedPanels = new Set<string>()
    const syncedLayout = new Set<string>()
    const syncViews = () => {
      const views = runtime.listAllViews().filter(({ view }) => !view.dynamic)
      const built = buildPanelDefs(views, (pluginId, view) =>
        createElement(PluginView, { pluginId, view }),
      )
      const currentPanelIds = new Set<string>()
      const currentLayoutIds = new Set<string>()
      for (const { def, view } of built) {
        currentPanelIds.add(def.id)
        panelRegistry.registerPanel(def)
        // 徽章面板（zone 'top'/'bottom'）不进布局栈——非侧栏堆叠项。
        if (def.location?.zone === 'side') {
          currentLayoutIds.add(def.id)
          layoutRegistry.register({
            id: def.id,
            slot: VIEW_CONTAINER_TO_SLOT[view.container] ?? 'desktop.sidebar',
            title: view.title,
            icon: view.icon,
            weight: 100, // 插件项排在内置项之后
          })
        }
      }
      // 注销已消失的项（插件卸载/热加载移除贡献点时）。
      for (const id of syncedPanels) {
        if (!currentPanelIds.has(id)) panelRegistry.unregisterPanel(id)
      }
      for (const id of syncedLayout) {
        if (!currentLayoutIds.has(id)) layoutRegistry.unregister(id)
      }
      syncedPanels.clear()
      for (const id of currentPanelIds) syncedPanels.add(id)
      syncedLayout.clear()
      for (const id of currentLayoutIds) syncedLayout.add(id)
    }
    syncViews()
    const unsub = runtime.subscribeViews(syncViews)
    return () => {
      unsub()
      for (const id of syncedPanels) {
        panelRegistry.unregisterPanel(id)
      }
      for (const id of syncedLayout) {
        layoutRegistry.unregister(id)
      }
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
