/**
 * commandRouter — 通用命令 / 面板路由协议。
 *
 * 一个「命令」= id + handler + 元数据。任何 UI（内置组件、插件、引导卡、
 * 深链）都能：
 *   1. 注册命令：`commands.register({ id: 'settings.open', handler })`
 *   2. 按 id 执行：`commands.execute('settings.open', { section: 'llm' })`
 *   3. 通过 URL scheme 导航：`commands.navigate('xbot://settings.open?section=llm')`
 *
 * 为什么需要它：
 *   - 引导卡 / 提示条 / 文档里的「点击这里」必须能**直接唤起对应面板**，
 *     而不是只写一句「点击右下角齿轮」让用户自己找。
 *   - 插件需要统一的能力去打开宿主面板（设置、会话、标签页），不必各自
 *     发明一套回调。
 *   - 深链（`xbot://...`）让「把用户送到某个面板」变成可分享、可脚本化的
 *     一等操作 —— 插件输出、通知、webhook 都能直接给出可点击的 URL。
 *
 * 设计约束：
 *   - **单例**：模块级导出，任何组件（含 lazy 加载的）都能直接 import，
 *     不需要 props 层层透传。
 *   - **命令 id 用点号命名空间**（`settings.open` / `panel.open`），与
 *     插件 id 的命名风格一致。
 *   - **handler 拿到的 args 是 string map**（URL query 的原生形态），需要
 *     类型的调用方自行收窄；这样 URL 与代码调用共用同一条路径。
 *   - **未注册命令抛错**（不静默）—— 拼错命令名应该立刻暴露。
 */
import type { Disposable } from '@/plugin-api'

/** 命令参数：URL query 的原生形态（全部字符串）。 */
export type CommandArgs = Record<string, string>

export interface CommandDef {
  /** 命令 id，点号命名空间，例如 `settings.open`。 */
  id: string
  /** i18n key（优先）—— 用于命令面板显示。 */
  titleKey?: string
  /** 纯文本标题（无 i18n 时使用）。 */
  title?: string
  /** 分组（命令面板按 category 分组显示）。 */
  category?: string
  /** 默认快捷键（如 `ctrl+shift+p`）；由宿主统一分发。 */
  keybinding?: string
  /** 命令体。args 来自 URL query 或调用方。 */
  handler: (args: CommandArgs) => void | Promise<void>
}

interface CommandEntry extends CommandDef {
  /** 注册来源（`''` = 内置宿主命令）。 */
  owner: string
}

/**
 * URL scheme 前缀。支持三种等价写法：
 *   xbot://settings.open?section=llm
 *   xbot:settings.open?section=llm
 *   /command/settings.open?section=llm   （浏览器内深链，可被 router 接管）
 */
const SCHEME = 'xbot:'
const HASH_COMMAND_PREFIX = '/command/'

export class CommandRouter {
  private commands = new Map<string, CommandEntry>()
  private keybindings = new Map<string, string>()

  /** 注册命令。重复注册同一 id 会覆盖并告警（热重载场景）。 */
  register(def: CommandDef, owner = ''): Disposable {
    if (this.commands.has(def.id)) {
      console.warn(`[commands] 命令 ${def.id} 重复注册，覆盖旧 handler`)
    }
    this.commands.set(def.id, { ...def, owner })
    if (def.keybinding) this.keybindings.set(def.keybinding, def.id)
    return () => {
      // 只有当前 owner 仍然是它时才删除（避免热重载误删新注册的）。
      if (this.commands.get(def.id)?.owner === owner) {
        this.commands.delete(def.id)
        if (def.keybinding) this.keybindings.delete(def.keybinding)
      }
    }
  }

  /** 批量注册（返回一个合并的 Disposable）。 */
  registerAll(defs: CommandDef[], owner = ''): Disposable {
    const disposables = defs.map((d) => this.register(d, owner))
    return () => disposables.forEach((d) => d())
  }

  /** 执行命令。未注册则抛错（拼错命令名要立刻暴露，不静默失败）。 */
  async execute(id: string, args: CommandArgs = {}): Promise<void> {
    const entry = this.commands.get(id)
    if (!entry) throw new Error(`[commands] 未注册命令: ${id}`)
    await entry.handler(args)
  }

  /** 命令是否已注册（用于 UI 条件渲染，不抛错）。 */
  has(id: string): boolean {
    return this.commands.has(id)
  }

  /** 列出全部命令（命令面板用），按 category + id 稳定排序。 */
  list(): CommandDef[] {
    return [...this.commands.values()]
      .map(({ owner: _owner, ...def }) => def)
      .sort((a, b) => (a.category ?? '').localeCompare(b.category ?? '') || a.id.localeCompare(b.id))
  }

  /**
   * 解析命令 URI。支持：
   *   xbot://settings.open?section=llm
   *   xbot:settings.open?section=llm
   *   /command/settings.open?section=llm
   * 返回 null 表示不是命令 URI。
   */
  parseUri(uri: string): { id: string; args: CommandArgs } | null {
    let rest: string | null = null
    if (uri.startsWith(SCHEME)) {
      rest = uri.slice(SCHEME.length)
      // 吃掉 `//`（xbot://foo → foo）
      rest = rest.replace(/^\/+/, '')
    } else if (uri.startsWith(HASH_COMMAND_PREFIX)) {
      rest = uri.slice(HASH_COMMAND_PREFIX.length)
    }
    if (rest === null || rest === '') return null

    const qIndex = rest.indexOf('?')
    const id = (qIndex >= 0 ? rest.slice(0, qIndex) : rest).replace(/\/+$/, '')
    if (!id) return null

    const args: CommandArgs = {}
    if (qIndex >= 0) {
      for (const [k, v] of new URLSearchParams(rest.slice(qIndex + 1))) args[k] = v
    }
    return { id, args }
  }

  /** 解析并执行命令 URI。返回 false 表示不是命令 URI（调用方自行处理）。 */
  async navigate(uri: string): Promise<boolean> {
    const parsed = this.parseUri(uri)
    if (!parsed) return false
    await this.execute(parsed.id, parsed.args)
    return true
  }

  /** 由宿主在 keydown 时调用；命中返回 true。 */
  dispatchKey(event: KeyboardEvent): boolean {
    const kb = keybindingFromEvent(event)
    const id = kb ? this.keybindings.get(kb) : undefined
    if (!id) return false
    void this.execute(id)
    return true
  }

  /** 测试/卸载用。 */
  clear(): void {
    this.commands.clear()
    this.keybindings.clear()
  }
}

/** 把键盘事件归一化成 `ctrl+shift+p` 形式的 keybinding 字符串。 */
export function keybindingFromEvent(e: KeyboardEvent): string {
  const parts: string[] = []
  if (e.ctrlKey || e.metaKey) parts.push('ctrl')
  if (e.altKey) parts.push('alt')
  if (e.shiftKey) parts.push('shift')
  const key = e.key.length === 1 ? e.key.toLowerCase() : e.key.toLowerCase()
  if (key && key !== 'control' && key !== 'meta' && key !== 'alt' && key !== 'shift') parts.push(key)
  return parts.join('+')
}

/** 模块级单例 —— 任何组件（含 lazy chunk）直接 import 使用。 */
export const commands = new CommandRouter()
