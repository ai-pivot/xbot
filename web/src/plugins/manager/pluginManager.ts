/**
 * xbot.plugin-manager —— 内置插件管理面板（自举实现）。
 *
 * 这个"插件"本身消费 @xbot/plugin-api 的公开能力：
 *   - 贡献一个 view（right_sidebar 容器）
 *   - 通过 ctx.rpc.call('plugin.list'/'plugin.setEnabled'/…) 管理插件
 *   - 通过 ctx.plugins 读运行时状态
 *
 * 它不访问宿主内部（无 window 直达、无注册表后门），只走公开 API——
 * 是插件系统能力模型的高保真演示（dogfooding）。第三方可写更好的面板覆盖它。
 */
import type { PluginContext, PluginManifest, Disposable } from '@/plugin-api'
import i18n from '@/i18n'

export const manifest = {
  id: 'xbot.plugin-manager',
  // 内置插件随主 bundle 打包（拿不到清单 web.i18n）⇒ 名称/描述/标题走宿主 i18n。
  // ⛔ 必须用 getter（**读取时**求值）：模块 import 时求值一次会把名称/标题定格在
  // 加载时的语言 —— 切语言后 tab/面板/卡片文案永不更新（用户实测根因之一）。
  // manifest 是这些文案的权威来源，每次读都按当前宿主语言解析。
  get name() {
    return i18n.t('plugins.manager.manifest.name', { defaultValue: '插件管理' })
  },
  version: '0.1.0',
  get description() {
    return i18n.t('plugins.manager.manifest.description', {
      defaultValue: '管理插件：查看/启用/禁用/卸载/重载（自举实现，本身也是一个插件）',
    })
  },
  permissions: ['rpc', 'plugins', 'ui'] as const,
  contributes: [
    {
      kind: 'view',
      id: 'xbot.plugin-manager.panel',
      container: 'right_sidebar',
      // 内置插件随主 bundle 打包（拿不到插件清单的 web.i18n）⇒ 走宿主 i18n（同上：getter）。
      get title() {
        return i18n.t('plugins.manager.manifest.title', { defaultValue: '插件' })
      },
      icon: 'blocks',
      // 内置视图标记：宿主 host.loadViewComponent 识别此标记直接返回静态组件。
      entry: 'builtin:xbot.plugin-manager.panel',
    },
  ],
} satisfies PluginManifest

/**
 * activate：内置插件无需初始化副作用（视图由贡献点渲染）。
 * 返回 no-op disposer 以符合生命周期契约。
 */
export function activate(_ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  return () => {}
}
