/**
 * 命令系统——注册/执行/快捷键。
 *
 * 插件经 ctx.commands.register 注册命令；贡献点声明的 command 经 registry
 * 挂载（handler 由插件 exports.commandHandlers 提供）。快捷键绑定由宿主
 * 监听键盘事件分发。
 */
import type { Disposable } from '@/plugin-api'
import type { CommandsAPI } from '@/plugin-api'

export interface CommandEntry {
  id: string
  pluginId: string
  handler: (args: unknown) => void | Promise<void>
}

export class CommandRegistry implements CommandsAPI {
  private commands = new Map<string, CommandEntry>()
  private keybindings = new Map<string, string>() // keybinding → commandId

  register(id: string, handler: (args: unknown) => void | Promise<void>): Disposable {
    return this.registerFor('', id, handler)
  }

  registerFor(pluginId: string, id: string, handler: (args: unknown) => void | Promise<void>): Disposable {
    if (this.commands.has(id)) {
      console.warn(`[plugin-runtime] 命令 ${id} 重复注册，覆盖旧 handler`)
    }
    this.commands.set(id, { id, pluginId, handler })
    return () => {
      if (this.commands.get(id)?.pluginId === pluginId) this.commands.delete(id)
    }
  }

  async execute(id: string, args?: unknown): Promise<void> {
    const entry = this.commands.get(id)
    if (!entry) throw new Error(`未注册命令: ${id}`)
    await entry.handler(args)
  }

  registerKeybinding(keybinding: string, commandId: string): Disposable {
    this.keybindings.set(keybinding, commandId)
    return () => {
      this.keybindings.delete(keybinding)
    }
  }

  /** 键盘事件分发（宿主在 window keydown 调用）。 */
  dispatchKey(event: KeyboardEvent): boolean {
    const kb = keybindingFromEvent(event)
    const cmd = this.keybindings.get(kb)
    if (!cmd) return false
    void this.execute(cmd)
    return true
  }

  /** 卸载插件：移除该插件注册的全部命令与快捷键。 */
  removePlugin(pluginId: string): void {
    for (const [id, entry] of this.commands) {
      if (entry.pluginId === pluginId) this.commands.delete(id)
    }
    for (const [kb, cmd] of this.keybindings) {
      if (this.commands.get(cmd)?.pluginId === pluginId) this.keybindings.delete(kb)
    }
  }
}

/** 把 KeyboardEvent 规范化为 keybinding 字符串（如 "ctrl+shift+h"）。 */
export function keybindingFromEvent(e: KeyboardEvent): string {
  const parts: string[] = []
  if (e.ctrlKey || e.metaKey) parts.push('ctrl')
  if (e.altKey) parts.push('alt')
  if (e.shiftKey) parts.push('shift')
  const key = e.key.length === 1 ? e.key.toLowerCase() : e.key.toLowerCase()
  parts.push(key)
  return parts.join('+')
}
