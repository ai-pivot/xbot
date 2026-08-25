/**
 * xbot.skill-manager — 内置前端插件：技能管理面板。
 *
 * 形态：内置插件（静态 import + activateBuiltin + builtin: 视图），同 xbot.plugin-manager 范式。
 * - 4 个核心 skill RPC（skill_list / skill_set_enabled / skill_get_content / skill_validate_path）
 *   经 runtime.rpc.call 无点号直传 /api/rpc（handleRPC 从 cookie 注入 RPCIdentity，rpcAuthID(ctx) 解析身份）。
 * - install/uninstall 复用 master 通用市场 REST（/api/app/install-file + /api/app/uninstall）。
 * - export 走保留的薄 REST /api/skills/export（zip 二进制下载）。
 */
import type { PluginContext, PluginManifest, Disposable } from '@/plugin-api'

export const manifest = {
  id: 'xbot.skill-manager',
  name: 'Skill Manager',
  version: '0.1.0',
  description: '管理技能：查看/启用/禁用/导出/卸载/安装',
  permissions: ['rpc', 'ui'] as const,
  contributes: [
    {
      kind: 'view',
      id: 'xbot.skill-manager.panel',
      container: 'right_sidebar',
      title: '技能',
      icon: 'sparkles',
      entry: 'builtin:xbot.skill-manager.panel',
    },
  ],
} satisfies PluginManifest

export function activate(_ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  // 视图由宿主静态注册（builtinViews），无需运行时资源。
  return () => {}
}
