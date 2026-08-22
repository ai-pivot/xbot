/**
 * xbot.iteration-stats —— 内置迭代指标插件 manifest。
 *
 * 声明一个 `container: 'iteration'` 的 view，入口用 `builtin:` 标记由宿主
 * 静态渲染（随主 bundle 分发，不产生独立 chunk——避免 React #311）。
 */
import type { PluginContext, PluginManifest, Disposable } from '@/plugin-api'

export const manifest = {
  id: 'xbot.iteration-stats',
  name: 'Iteration Stats',
  version: '0.1.0',
  description: '在每个迭代显示 token 数 / TTFT / 工具耗时，并动态显示当前 tokens/s',
  permissions: [] as const,
  contributes: [
    {
      kind: 'view',
      id: 'xbot.iteration-stats.iteration',
      container: 'iteration',
      title: '迭代指标',
      icon: 'activity',
      entry: 'builtin:xbot.iteration-stats.iteration',
    },
  ],
} satisfies PluginManifest

export function activate(_ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  return () => {}
}