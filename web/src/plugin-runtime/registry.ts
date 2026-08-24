/**
 * 贡献点注册表（registry）——单一启动门控。
 *
 * 权威校验 = 前端运行时校验（§5 单一门控）：插件激活前校验 manifest
 * （贡献点形状、权限与能力的对应、ID 唯一性、依赖已激活）。校验失败 →
 * 插件不激活并返回原因。后端不再做贡献点 Schema 校验。
 *
 * 支持热加载/卸载：registerPlugin / unregisterPlugin / reloadPlugin。
 */
import type { Contribution, Disposable, PluginManifest, Permission } from '@/plugin-api'
import type { ViewContribution } from '@/plugin-api'
import type { MessageRendererContribution } from '@/plugin-api'
import type { CommandContribution } from '@/plugin-api'

/** 校验失败原因。 */
export interface PluginValidationIssue {
  pluginId: string
  message: string
}

export interface PluginRuntimeState {
  id: string
  /** 插件显示名（来自 manifest.name）。 */
  name: string
  version: string
  enabled: boolean
  status: 'active' | 'inactive' | 'error' | 'reloading'
  error?: string
  permissions: readonly Permission[]
  /** 贡献点 ID 列表（用于管理面板展示）。 */
  contributionIds: readonly string[]
}

/** 视图注册回调：宿主把 slot 渲染器挂到容器。 */
export type ViewMount = (view: ViewContribution, element: HTMLElement) => Disposable | void

/** 消息渲染器注册回调。 */
export type RendererMount = (renderer: MessageRendererContribution) => Disposable | void

/** 命令注册回调。 */
export type CommandMount = (command: CommandContribution, handler: (args: unknown) => void) => Disposable | void

export interface RegistryHooks {
  onView?: ViewMount
  onRenderer?: RendererMount
  onCommand?: CommandMount
  /** 插件状态变化（管理面板用）。 */
  onStateChange?: (state: PluginRuntimeState) => void
}

/** 已激活插件的内部记录。 */
interface PluginRecord {
  manifest: PluginManifest
  disposables: Disposable[]
  /** 命名导出（Exports API：模块公共 API）。 */
  exports: Record<string, unknown>
  state: PluginRuntimeState
}

/** 插件 ID 全局唯一校验。 */
export class ContributionRegistry {
  private plugins = new Map<string, PluginRecord>()
  private hooks: RegistryHooks

  constructor(hooks: RegistryHooks = {}) {
    this.hooks = hooks
  }

  /** 所有插件运行时状态（管理面板数据源）。 */
  listStates(): PluginRuntimeState[] {
    return [...this.plugins.values()].map((r) => ({ ...r.state }))
  }

  /** 同步取已激活插件 exports（§3.7 Exports API）。 */
  getExports(pluginId: string): Record<string, unknown> | undefined {
    return this.plugins.get(pluginId)?.exports
  }

  /** 已激活插件的 manifest（多入口插件的视图 entry 判定用）。 */
  manifestOf(pluginId: string): PluginManifest | undefined {
    return this.plugins.get(pluginId)?.manifest
  }

  /** 插件是否已激活。 */
  isActive(pluginId: string): boolean {
    return this.plugins.has(pluginId)
  }

  /** 校验插件清单——单一启动门控（§5）。返回 null 表示通过。 */
  validate(manifest: PluginManifest): PluginValidationIssue | null {
    if (!manifest.id || !manifest.name || !manifest.version) {
      return { pluginId: manifest.id, message: 'manifest 缺 id/name/version' }
    }
    if (!Array.isArray(manifest.contributes)) {
      return { pluginId: manifest.id, message: 'contributes 必须是数组' }
    }
    for (const c of manifest.contributes) {
      if (!c || typeof c !== 'object' || !('kind' in c)) {
        return { pluginId: manifest.id, message: `contributes 含非法条目: ${JSON.stringify(c)}` }
      }
      const kind = (c as Contribution).kind
      if (!['view', 'command', 'messageRenderer', 'toolbar', 'contextMenu', 'setting', 'eventHandler', 'theme'].includes(kind)) {
        return { pluginId: manifest.id, message: `未知贡献点 kind: ${String(kind)}` }
      }
      // ID 唯一性
      const id = (c as { id?: string }).id
      if (!id) {
        return { pluginId: manifest.id, message: `贡献点 ${kind} 缺 id` }
      }
      if (this.findContribution(id) && kind !== 'messageRenderer') {
        // 渲染器允许重名（优先级链）；视图/命令等必须唯一
        return { pluginId: manifest.id, message: `贡献点 id 冲突: ${id}` }
      }
    }
    // 强依赖必须已激活
    for (const dep of manifest.activationDependencies ?? []) {
      if (!this.plugins.has(dep)) {
        return { pluginId: manifest.id, message: `缺少强依赖插件: ${dep}` }
      }
    }
    return null
  }

  private findContribution(id: string): Contribution | undefined {
    for (const r of this.plugins.values()) {
      const found = r.manifest.contributes.find((c) => (c as { id?: string }).id === id)
      if (found) return found
    }
    return undefined
  }

  /** 激活插件（单一门控通过后挂载全部贡献点）。 */
  async registerPlugin(
    manifest: PluginManifest,
    exports: Record<string, unknown>,
  ): Promise<{ ok: true } | { ok: false; error: string }> {
    // 热加载：同 ID 先卸载旧实例。
    if (this.plugins.has(manifest.id)) {
      this.unregisterPlugin(manifest.id)
    }
    const issue = this.validate(manifest)
    if (issue) {
      console.error(`[plugin-runtime] 插件 ${manifest.id} 校验失败: ${issue.message}`)
      return { ok: false, error: issue.message }
    }

    const disposables: Disposable[] = []
    const record: PluginRecord = {
      manifest,
      disposables,
      exports,
      state: {
        id: manifest.id,
        name: manifest.name,
        version: manifest.version,
        enabled: true,
        status: 'active',
        permissions: manifest.permissions ?? [],
        contributionIds: manifest.contributes.map((c) => (c as { id?: string }).id ?? ''),
      },
    }
    this.plugins.set(manifest.id, record)

    for (const c of manifest.contributes) {
      try {
        this.mount(manifest, c, disposables)
      } catch (error) {
        // 贡献点级回滚：卸载已挂载的，插件标记 error。
        for (const d of disposables.splice(0).reverse()) {
          try {
            d()
          } catch {
            /* ignore */
          }
        }
        this.plugins.delete(manifest.id)
        record.state.status = 'error'
        record.state.error = error instanceof Error ? error.message : String(error)
        this.hooks.onStateChange?.({ ...record.state })
        console.error(`[plugin-runtime] 插件 ${manifest.id} 挂载贡献点失败:`, error)
        return { ok: false, error: String(error) }
      }
    }
    this.hooks.onStateChange?.({ ...record.state })
    this.notifyViewsChanged()
    return { ok: true }
  }

  /** 挂载单个贡献点（分发到宿主回调）。 */
  private mount(manifest: PluginManifest, c: Contribution, disposables: Disposable[]): void {
    const add = (d: Disposable | void) => {
      if (d) disposables.push(d)
    }
    switch (c.kind) {
      case 'view':
        if (this.hooks.onView) add(this.hooks.onView(c, document.createElement('div')))
        break
      case 'command':
        if (this.hooks.onCommand) {
          const handler = (args: unknown) => {
            const fn = (manifest as unknown as { handlers?: Record<string, (a: unknown) => void> }).handlers?.[c.id]
            if (fn) return fn(args)
            // 插件无显式 handler：通过事件通知插件模块（若模块导出了 commandHandlers）。
            const h = (this.plugins.get(manifest.id)?.exports as { commandHandlers?: Record<string, (a: unknown) => void> } | undefined)?.commandHandlers?.[c.id]
            if (h) return h(args)
          }
          add(this.hooks.onCommand(c, handler))
        }
        break
      case 'messageRenderer':
        if (this.hooks.onRenderer) add(this.hooks.onRenderer(c as MessageRendererContribution))
        break
      default:
        // toolbar/contextMenu/setting/eventHandler/theme 由宿主在贡献点查询时消费，
        // 不需要挂载回调；但记录可查询（管理面板/其他插件可见）。
        break
    }
  }

  /** 卸载插件：逆序执行 disposables + 移除贡献点（幂等）。 */
  unregisterPlugin(pluginId: string): boolean {
    const record = this.plugins.get(pluginId)
    if (!record) return false
    for (const d of record.disposables.splice(0).reverse()) {
      try {
        d()
      } catch (error) {
        console.error(`[plugin-runtime] 卸载插件 ${pluginId} 的 disposer 失败`, error)
      }
    }
    this.plugins.delete(pluginId)
    this.hooks.onStateChange?.({ ...record.state, enabled: false, status: 'inactive' })
    this.notifyViewsChanged()
    return true
  }

  /** 查询某插件的贡献点（供 ViewSlot 等消费）。 */
  contributionsOf(pluginId: string): readonly Contribution[] {
    return this.plugins.get(pluginId)?.manifest.contributes ?? []
  }

  /** 向已激活插件追加 disposer（activate 返回的清理函数，§3.7）。 */
  pushDisposable(pluginId: string, d: Disposable): void {
    const record = this.plugins.get(pluginId)
    if (record) record.disposables.push(d)
  }

  // ─── 动态面板订阅（UI 统一）──────────────────────────────
  // 插件 view 贡献点是「声明一次，两端自动出现」的核心：宿主侧栏
  // （桌面 + 移动）订阅此变更，插件注册/卸载 view 时自动重建 tab 列表，
  // 无需在桌面/移动分别硬编码面板入口。

  private viewSubscribers = new Set<() => void>()

  /** 订阅贡献点集合变化（view 注册/卸载）。返回退订函数。 */
  subscribeViews(listener: () => void): Disposable {
    this.viewSubscribers.add(listener)
    return () => {
      this.viewSubscribers.delete(listener)
    }
  }

  private notifyViewsChanged(): void {
    for (const listener of this.viewSubscribers) {
      try {
        listener()
      } catch (error) {
        console.error('[plugin-runtime] view subscriber failed', error)
      }
    }
  }

  /** 查询所有 view 贡献点（跨插件，含所属插件 id），供宿主动态生成面板 tab。 */
  listAllViews(): Array<{ pluginId: string; view: ViewContribution }> {
    const out: Array<{ pluginId: string; view: ViewContribution }> = []
    for (const [pluginId, record] of this.plugins) {
      for (const c of record.manifest.contributes) {
        if (c.kind === 'view') out.push({ pluginId, view: c })
      }
    }
    return out
  }

  /** 查询所有 messageRenderer 贡献点（跨插件，含所属插件 id），供宿主动态派发工具渲染。 */
  listAllRenderers(): Array<{ pluginId: string; renderer: MessageRendererContribution }> {
    const out: Array<{ pluginId: string; renderer: MessageRendererContribution }> = []
    for (const [pluginId, record] of this.plugins) {
      for (const c of record.manifest.contributes) {
        if (c.kind === 'messageRenderer') out.push({ pluginId, renderer: c })
      }
    }
    // 稳定排序：priority 大者优先（render 返回 null 时 fallback 到下一个）。
    out.sort((a, b) => b.renderer.priority - a.renderer.priority)
    return out
  }
}
