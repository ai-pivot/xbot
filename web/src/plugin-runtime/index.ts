/**
 * PluginRuntime——前端插件运行时主入口。
 *
 * 职责：
 *  - 激活/卸载/热加载插件（web_plugin_init / web_plugin_deactivate）
 *  - 单一启动门控（registry.validate）
 *  - 贡献点挂载（视图/命令/渲染器 → 宿主回调）
 *  - 事件总线 + RPC 桥 + 状态快照 + 插件互操作
 *  - 向宿主暴露 PluginRuntimeProvider 供 React 消费
 *
 * 设计约束（反 cordis）：不叫 effect/fiber/epoch；disposable 就叫 disposable。
 */
import type { ReactNode } from 'react'
import { createContext, createElement, useContext, useMemo, useRef } from 'react'

import type { Contribution, Disposable, PluginManifest, PluginMeta } from '@/plugin-api'
import type { ViewContainer, ViewContribution } from '@/plugin-api'
import type { Matcher, MessageRendererContribution, RenderContext } from '@/plugin-api'

import { ContributionRegistry } from './registry'
import { PluginEventBus } from './events'
import { CommandRegistry } from './commands'
import { PluginRpcBridge, type RpcTransport } from './rpc'
import { PluginState } from './state'
import { PluginUI, type UIServices } from './ui'
import { PluginInterop } from './plugins'
import { PluginConfigService } from './config'
import { panelRegistry } from './panelRegistry'
import type { PanelsAPI } from '@/plugin-api'
import { buildContext } from './context'
import { loadPluginModule, versionedUrl, type PluginModule } from './loader'
import { toSafeMessage } from './sanitize'
import type { SessionSummary } from '@/plugin-api'

export interface PluginRuntimeHost {
  /** RPC 传输（复用现有 /api/rpc）。 */
  rpcTransport: RpcTransport
  /** 插件 web 产物 base URL（如 /plugins/xbot.git/web）。 */
  moduleBaseUrl: (pluginId: string) => string
  /** 视图组件加载（宿主实现：动态 import entry + 缓存）。 */
  loadViewComponent: (pluginId: string, view: ViewContribution) => Promise<React.ComponentType | null>
  /** UI 服务（toast/面板）。 */
  ui: UIServices
  /** 会话快照来源。 */
  getSession: () => SessionSummary | null
  getMessagesRaw: () => readonly unknown[]
  /** 后端插件清单（管理面板数据源）。 */
  getBackendPlugins: () => Promise<readonly { id: string; version: string; enabled: boolean }[]>
  /** 贡献点消费：宿主把 view 挂到布局位。 */
  mountView: (view: ViewContribution) => Disposable | void
  /** 渲染器注册（宿主接到渲染器调度器）。 */
  mountRenderer: (renderer: MessageRendererContribution) => Disposable | void
  /** 命令挂载（宿主把命令接入命令面板/快捷键）。 */
  mountCommand: (id: string, handler: (args: unknown) => void) => Disposable | void
  /** 状态变化通知（管理面板）。 */
  onPluginStateChange?: (state: import('./registry').PluginRuntimeState) => void
}

/**
 * 构建某插件的 ctx.panels 能力（'ui' 权限）。全部操作经 panelRegistry，
 * update/unregister 带 ownership 校验——插件只能动 source 为自己的面板。
 */
function makePanelsAPI(pluginId: string): PanelsAPI {
  return {
    register: (def) => {
      panelRegistry.registerPanel({ ...def, source: pluginId })
      return () => {
        const cur = panelRegistry.getPanel(def.id)
        if (cur && cur.source === pluginId) panelRegistry.unregisterPanel(def.id)
      }
    },
    update: (id, patch) => {
      const cur = panelRegistry.getPanel(id)
      if (!cur || cur.source !== pluginId) return
      panelRegistry.registerPanel({ ...cur, ...patch })
    },
    unregister: (id) => {
      const cur = panelRegistry.getPanel(id)
      if (!cur || cur.source !== pluginId) return
      panelRegistry.unregisterPanel(id)
    },
  }
}

export class PluginRuntime {
  registry: ContributionRegistry
  events: PluginEventBus
  commands: CommandRegistry
  rpc: PluginRpcBridge
  state: PluginState
  ui: PluginUI
  plugins: PluginInterop
  config: PluginConfigService
  private host: PluginRuntimeHost
  private viewCache = new Map<string, Promise<React.ComponentType | null>>()
  /** 已激活插件的 module 引用（热加载时更新）。 */
  private modules = new Map<string, PluginModule>()
  /** 宿主注册的内置渲染器（如 GenUI —— 宿主组件，不走 ESM 插件模块）。 */
  private builtinRenderers: MessageRendererContribution[] = []

  constructor(host: PluginRuntimeHost) {
    this.host = host
    this.registry = new ContributionRegistry({
      onView: (view) => host.mountView(view),
      onRenderer: (renderer) => host.mountRenderer(renderer),
      onCommand: (command, handler) => host.mountCommand(command.id, handler),
      onStateChange: (s) => host.onPluginStateChange?.(s),
    })
    this.events = new PluginEventBus()
    this.commands = new CommandRegistry()
    this.rpc = new PluginRpcBridge(host.rpcTransport)
    this.state = new PluginState({
      getSession: () => host.getSession(),
      getMessagesRaw: () => host.getMessagesRaw(),
      toSafeMessage,
      getPlugins: () => [],
    })
    this.ui = new PluginUI(host.ui)
    this.plugins = new PluginInterop(this.registry)
    this.config = new PluginConfigService(this.rpc)
    // 让 require 支持按需激活（从后端拉清单）。
    ;(this.registry as unknown as { ensureActive: (id: string) => Promise<void> }).ensureActive =
      async (id: string) => {
        const decl = await this.fetchPluginDecl(id)
        if (decl) await this.activate(decl.manifest, decl.moduleUrl)
      }
  }

  /** 从后端拉单个插件声明（懒加载 require 用）。 */
  private async fetchPluginDecl(
    id: string,
  ): Promise<{ manifest: PluginManifest; moduleUrl: string } | null> {
    // 后端 RPC 返回插件清单（web_plugin_list）。
    const list = (await this.host.rpcTransport.call('web_plugin_list', {})) as Array<{
      id: string
      manifest: PluginManifest
      module_url?: string
    }>
    const found = list?.find((p) => p.id === id)
    if (!found) return null
    const moduleUrl = found.module_url ?? `${this.host.moduleBaseUrl(id)}/index.js`
    return { manifest: found.manifest, moduleUrl }
  }

  /** 激活插件：加载模块 → 单一门控 → 挂载贡献点 → 调 activate。 */
  async activate(manifest: PluginManifest, moduleUrl?: string): Promise<{ ok: boolean; error?: string }> {
    // 热加载：同 ID 先卸载旧实例。
    if (this.registry.isActive(manifest.id)) {
      this.deactivate(manifest.id)
    }
    const url = moduleUrl ?? `${this.host.moduleBaseUrl(manifest.id)}/index.js`
    const versioned = versionedUrl(url, manifest.version)
    let mod: PluginModule
    try {
      mod = await loadPluginModule(versioned)
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error)
      console.error(`[plugin-runtime] 加载插件 ${manifest.id} 失败: ${msg}`)
      return { ok: false, error: `模块加载失败: ${msg}` }
    }
    return this.activateModule(manifest, mod)
  }

  /**
   * 激活内置插件（模块随前端分发，静态 import，不走 URL 加载）。
   * 与 activate 走同一条注册/校验/activate 路径，只是模块来源不同——
   * 内置插件与第三方插件无特权差异（自举纪律）。
   */
  async activateBuiltin(manifest: PluginManifest, mod: PluginModule): Promise<{ ok: boolean; error?: string }> {
    // 热加载：同 ID 先卸载旧实例。
    if (this.registry.isActive(manifest.id)) {
      this.deactivate(manifest.id)
    }
    return this.activateModule(manifest, mod)
  }

  /** 共享激活流程：模块已加载，做单一门控 + 贡献点挂载 + activate 调用。 */
  private async activateModule(
    manifest: PluginManifest,
    mod: PluginModule,
  ): Promise<{ ok: boolean; error?: string }> {
    // manifest 以模块导出为准（声明即契约），允许后端下发的被模块内覆盖。
    const effective = mod.manifest ?? manifest
    const exports = collectPluginExports(mod)
    const reg = await this.registry.registerPlugin(effective, exports)
    if (!reg.ok) {
      return { ok: false, error: reg.error }
    }
    this.modules.set(effective.id, mod)
    // 构建 ctx 并调用 activate。
    const perms = effective.permissions ?? []
    const meta: PluginMeta = { id: effective.id, version: effective.version }
    const ctx = buildContext(perms, {
      meta,
      events: this.events,
      commands: this.commands,
      rpc: this.rpc,
      state: this.state,
      ui: this.ui,
      panels: makePanelsAPI(effective.id),
      plugins: this.plugins,
      config: this.config.forPlugin(effective.id),
      registerContribution: (c: Contribution) => {
        // 动态贡献点：经 registry 挂载，返回 disposable。
        this.registry.registerPlugin(
          { ...effective, contributes: [c] },
          exports,
        ).then((r) => {
          if (!r.ok) console.error(`[plugin-runtime] 动态贡献点失败: ${r.error}`)
        })
        return () => {}
      },
    })
    try {
      const result = mod.activate?.(ctx)
      if (typeof result === 'function') {
        // activate 返回 disposer（等价 disposable，诚实命名）。
        this.registry.pushDisposable?.(effective.id, result)
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error)
      this.deactivate(effective.id)
      return { ok: false, error: `activate 失败: ${msg}` }
    }
    this.plugins.notifyActivated(effective.id)
    return { ok: true }
  }

  /** 卸载插件：disposables 逆序清理 + 移除贡献点（幂等）。 */
  deactivate(pluginId: string): boolean {
    const removed = this.registry.unregisterPlugin(pluginId)
    if (removed) {
      this.events.unsubscribePlugin(pluginId)
      this.commands.removePlugin(pluginId)
      this.plugins.notifyDeactivated(pluginId)
      this.modules.delete(pluginId)
      // 清掉视图缓存（热加载后重新加载模块）。
      for (const key of [...this.viewCache.keys()]) {
        if (key.startsWith(pluginId)) this.viewCache.delete(key)
      }
    }
    return removed
  }

  /** 热加载：同 ID 重新 init（新版本 URL）。 */
  async reload(manifest: PluginManifest, moduleUrl?: string): Promise<{ ok: boolean; error?: string }> {
    return this.activate(manifest, moduleUrl)
  }

  /** 宿主 ViewSlot 数据源：直接查 registry（无 host 循环依赖）。 */
  getViewsByContainer(container: ViewContainer): readonly ViewContribution[] {
    const views: ViewContribution[] = []
    for (const st of this.registry.listStates()) {
      for (const c of this.registry.contributionsOf(st.id)) {
        if (c.kind === 'view' && c.container === container) views.push(c)
      }
    }
    return views
  }

  /** 查询所有 view 贡献点（含所属插件 id）——供宿主动态生成侧栏 tab。 */
  listAllViews(): Array<{ pluginId: string; view: ViewContribution }> {
    return this.registry.listAllViews()
  }

  /**
   * 工具渲染派发（messageRenderer 调度器）——把「工具 → 渲染」从宿主硬编码
   * 迁移到插件的 messageRenderer 声明。按 priority 降序匹配，render 返回 null
   * 时 fallback 到下一个；无匹配返回 null（宿主走默认渲染）。
   *
   * metadata-driven（§9）：matches 的 { uiMode } 让插件按 UIDecl.mode 匹配，
   * 而非工具名——这是「删除 display_html 工具名硬编码」的核心。
   */
  renderTool(tool: ToolRenderInput, ctx: RenderContext): ReactNode | null {
    // 内置渲染器（宿主注册）与插件渲染器统一派发，按 priority 降序。
    const renderers = [
      ...this.builtinRenderers,
      ...this.registry.listAllRenderers().map((r) => r.renderer),
    ].sort((a, b) => b.priority - a.priority)

    for (const renderer of renderers) {
      if (!matchesTool(renderer.matches, tool)) continue
      const msg = {
        tool: {
          name: tool.name ?? '',
          uiMode: tool.uiMode,
          result: tool,
        },
      }
      try {
        const node = renderer.render(msg as never, ctx)
        if (node != null) return node
      } catch (error) {
        // 渲染器崩溃只降级到默认渲染，不崩整个消息。
        console.error(`[plugin-runtime] 渲染器 ${renderer.id} 崩溃:`, error)
      }
    }
    return null
  }

  /**
   * 注册宿主内置渲染器（如 GenUI —— 宿主组件，不走 ESM 插件模块）。
   * 返回 disposable 用于注销。内置渲染器与插件渲染器在 renderTool 里统一派发。
   */
  registerBuiltinRenderer(renderer: MessageRendererContribution): Disposable {
    this.builtinRenderers.push(renderer)
    return () => {
      const i = this.builtinRenderers.indexOf(renderer)
      if (i >= 0) this.builtinRenderers.splice(i, 1)
    }
  }

  /** 订阅插件 view 集合变化（插件注册/卸载 view）。返回退订函数。 */
  subscribeViews(listener: () => void): Disposable {
    return this.registry.subscribeViews(listener)
  }

  /** 宿主视图组件加载（缓存）。 */
  loadViewComponent(pluginId: string, view: ViewContribution): Promise<React.ComponentType | null> {
    const key = `${pluginId}:${view.id}`
    let p = this.viewCache.get(key)
    if (!p) {
      // 优先复用已激活的模块实例：activate() 通过 versionedUrl（?v=）加载并
      // 调用 mod.activate(ctx)（注入 ctx.rpc 等）。若这里再用不同 URL
      // （?view=）重新 import，浏览器 ESM 缓存会产生第二个模块实例——模块级
      // 变量（如 entry.tsx 里的 rpc）不共享，view 会显示"插件未初始化"。
      //
      // 但模块复用只对该插件的**主模块入口**视图有效：mod.default 是主入口
      // （manifest.entry，如 index.js）的视图组件。多入口插件的其他视图
      // （view.entry !== manifest.entry，如 git-fancy 的 diff.js/commit.js）
      // 必须按 view.entry 走 host 独立 import——否则任何视图都错误拿到主
      // 模块的 default（"diff tab 渲染成插件 panel"的根因）。命名导出
      // mod[view.id] 无 entry 约束（主模块按 view id 导出必是该视图组件，
      // 多视图放主模块时单例最优）。
      const mod = this.modules.get(pluginId)
      const mainEntry = this.registry.manifestOf(pluginId)?.entry
      const modMap = mod as unknown as Record<string, unknown> | undefined
      const namedComp = modMap?.[view.id]
      const namedIsComp =
        typeof namedComp === 'function' ||
        (namedComp != null && typeof namedComp === 'object' && (namedComp as { $$typeof?: unknown }).$$typeof != null)
      const defaultServesView = view.entry == null || view.entry === mainEntry
      if (mod && (namedIsComp || defaultServesView)) {
        const comp = (namedIsComp ? namedComp : modMap?.default) as unknown
        if (typeof comp === 'function') {
          p = Promise.resolve(comp as React.ComponentType)
        } else if (comp && typeof comp === 'object' && (comp as { $$typeof?: unknown }).$$typeof) {
          p = Promise.resolve(comp as React.ComponentType)
        } else {
          console.error(
            `[plugin-runtime] 已激活模块不含有效组件: plugin=${pluginId} view=${view.id}`,
            { moduleKeys: Object.keys(mod), compType: typeof comp },
          )
          p = Promise.resolve(null)
        }
      } else {
        p = this.host.loadViewComponent(pluginId, view)
      }
      this.viewCache.set(key, p)
    }
    return p
  }

  /** 当前插件状态列表（管理面板）。 */
  listPluginStates() {
    return this.registry.listStates()
  }

  /** 后端推送插件配置变更（web_plugin_config_changed）时分发到对应插件。 */
  notifyPluginConfigChanged(pluginId: string, config: Record<string, unknown>): void {
    this.config.notifyChanged(pluginId, config)
  }
}

/** 插件模块命名导出 = 公共 API（保留 manifest/activate/deactivate）。 */
function collectPluginExports(mod: PluginModule): Record<string, unknown> {
  const reserved = new Set(['manifest', 'activate', 'deactivate'])
  const out: Record<string, unknown> = {}
  for (const key of Object.keys(mod)) {
    if (!reserved.has(key)) out[key] = mod[key]
  }
  return out
}

/**
 * 工具渲染输入（宿主 ToolRender 传入的最小视图，不含内部状态）。
 * 对应的完整类型是 WebToolProgress（@/types/shared），这里只取渲染器
 * matches 需要的字段，避免 plugin-runtime 依赖 agent 组件层。
 */
export interface ToolRenderInput {
  name?: string
  /** UI 能力模式（来自 UIDecl 元数据，如 "genui"）。 */
  uiMode?: string
  detail?: string
  args?: string
  summary?: string
}

/**
 * messageRenderer matches 匹配（metadata-driven）。
 * 顺序：{tool} 按工具名、{uiMode} 按 UIDecl.mode（「删除工具名硬编码」的核心）、
 * {role} 不适用于工具（工具不是消息角色）、空对象通用匹配。
 */
export function matchesTool(matcher: Matcher, tool: ToolRenderInput): boolean {
  if ('tool' in matcher) return tool.name === matcher.tool
  if ('uiMode' in matcher) return tool.uiMode === matcher.uiMode
  if ('role' in matcher) return false
  return true
}

// ─── React Provider ───────────────────────────────────────────────

const PluginRuntimeContext = createContext<PluginRuntime | null>(null)

/** Dockview 隔离 root 桥接用：面板组件内读取 PluginRuntime（IterationSlot 等）。 */
export { PluginRuntimeContext }

export function PluginRuntimeProvider({
  host,
  children,
}: {
  host: PluginRuntimeHost
  children: ReactNode
}) {
  const ref = useRef<PluginRuntime | null>(null)
  const runtime = useMemo(() => {
    if (!ref.current) ref.current = new PluginRuntime(host)
    return ref.current
    // host 是 bootstrap 时的稳定对象；ref 保证只构造一次（单例语义）。
  }, [])
  return createElement(PluginRuntimeContext.Provider, { value: runtime }, children)
}

/** 消费 PluginRuntime（宿主组件内）。无 Provider 时抛错。 */
export function usePluginRuntime(): PluginRuntime {
  const rt = useContext(PluginRuntimeContext)
  if (!rt) throw new Error('usePluginRuntime 必须在 PluginRuntimeProvider 内使用')
  return rt
}

/**
 * 可选消费 PluginRuntime：无 Provider 时返回 null（用于渲染层的注入点，如
 * IterationSlot —— 组件在未挂载 PluginRuntimeProvider 的单元测试/降级场景下
 * 优雅返回，不抛错）。
 */
export function useOptionalPluginRuntime(): PluginRuntime | null {
  return useContext(PluginRuntimeContext)
}
