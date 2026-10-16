/**
 * xbot.marketplace — 内置前端插件：组件市场面板。
 *
 * 形态：内置插件（静态 import + builtin: 视图），同 xbot.skill-manager 范式。
 * - search/install/list-installed 经 REST /api/registry/* 调用。
 */
import type { PluginContext, PluginManifest, Disposable } from '@/plugin-api'
import i18n from '@/i18n'

export const manifest = {
  id: 'xbot.marketplace',
  get name() {
    return i18n.t('marketplace.manifest.name', { defaultValue: '组件市场' })
  },
  version: '0.1.0',
  get description() {
    return i18n.t('marketplace.manifest.description', {
      defaultValue: '浏览、搜索并安装技能、代理、插件',
    })
  },
  permissions: ['rpc', 'ui'] as const,
  contributes: [
    {
      kind: 'view',
      id: 'xbot.marketplace.panel',
      container: 'right_sidebar',
      get title() {
        return i18n.t('marketplace.manifest.title', { defaultValue: '市场' })
      },
      icon: 'store',
      entry: 'builtin:xbot.marketplace.panel',
    },
  ],
} satisfies PluginManifest

export function activate(_ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  return () => {}
}
