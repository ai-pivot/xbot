/**
 * 插件模块加载器——ESM 动态 import + 版本化 URL。
 *
 * 热加载：同一插件 ID 重新 init 时用新版本 URL（?v=content-hash）强制重新抓取，
 * 绕过 import map / 浏览器模块缓存。
 */
import type { PluginManifest } from '@/plugin-api'

/** 插件模块导出形状（与类型包的插件契约一致）。 */
export interface PluginModule {
  manifest: PluginManifest
  activate?: (ctx: unknown) => void | Promise<void> | (() => void)
  deactivate?: () => void
  /** Exports API：命名导出即公共 API（§3.7）。 */
  [key: string]: unknown
}

/** 版本化 URL：`/plugins/<id>/web/...` 拼接 ?v=<hash>。 */
export function versionedUrl(baseUrl: string, version: string, hash?: string): string {
  const sep = baseUrl.includes('?') ? '&' : '?'
  const v = hash ?? version.replace(/[^\w.-]/g, '_')
  return `${baseUrl}${sep}v=${encodeURIComponent(v)}`
}

/**
 * 加载插件模块（动态 import）。失败抛错（含模块语法错误、网络错误）。
 */
export async function loadPluginModule(entryUrl: string): Promise<PluginModule> {
  const mod = await import(/* @vite-ignore */ entryUrl)
  return mod as PluginModule
}
