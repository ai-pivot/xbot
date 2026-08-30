/**
 * xbot.ambience —— 氛围装饰壁纸插件（声明式设置 + 文件上传 + AmbienceContribution）。
 *
 * 纯声明式：壁纸预设（AmbienceContribution）+ 设置（contributes.configuration）。
 * activate 读 ctx.config → 推到 ambienceStore → AmbienceBackground 渲染。
 * 用户上传壁纸走 ctx.files（plugin files API → 服务器存储 + 鉴权 serve）。
 */
import type { PluginManifest } from '@/plugin-api'
import { ambienceStore } from '@/ambience/store'

export const manifest = {
  id: 'xbot.ambience',
  name: 'Ambience',
  version: '0.3.0',
  description: '壁纸 + 玻璃拟态（Ambience Layer）',
  permissions: ['config'] as const,
  contributes: [
    {
      kind: 'ambience',
      id: 'presets',
      wallpapers: [
        { id: 'aurora', name: '星夜极光', css: 'linear-gradient(165deg, #070d24 0%, #101638 40%, #221a4e 75%, #150e38 100%)' },
        { id: 'ember', name: '暮色余烬', css: 'radial-gradient(130% 110% at 72% 18%, #46201a 0%, #2a0f0d 42%, #150808 78%, #0d0505 100%)' },
        { id: 'sakura', name: '樱花和纸', css: 'linear-gradient(170deg, #f8f2e9 0%, #f7e7ef 55%, #efdccd 100%)' },
        { id: 'focus', name: '专注素色', css: 'linear-gradient(180deg, #14161b 0%, #191b21 100%)' },
      ],
    },
  ] as const,
} satisfies PluginManifest

export function activate(ctx: {
  config: {
    get: () => Promise<Record<string, unknown>>
    onConfigChange: (cb: (c: Record<string, unknown>) => void) => () => void
  }
}): void | (() => void) {
  /** 配置 → ambienceStore 推送（声明式设置的值映射到 ambienceStore 状态）。 */
  const pushToAmbience = (cfg: Record<string, unknown>) => {
    const wallpaper = typeof cfg.wallpaper === 'string' ? cfg.wallpaper : ''
    const glassOpacity = typeof cfg.glassOpacity === 'number' ? cfg.glassOpacity : 0.82
    const wallpaperOpacity = typeof cfg.wallpaperOpacity === 'number' ? cfg.wallpaperOpacity : 1
    const glassBlur = typeof cfg.glassBlur === 'number' ? cfg.glassBlur : 0

    // 解析壁纸值 → ambienceStore 可用的 wallpaper ID。
    // 'preset:aurora' → 'xbot.ambience:aurora'（AmbienceContribution 注册的 ID）
    // '/api/plugin-files/...' → 服务器 URL（ctx.files 上传的文件）
    let wallpaperId: string | null = null
    if (wallpaper.startsWith('preset:')) {
      wallpaperId = `xbot.ambience:${wallpaper.slice(7)}`
    } else if (wallpaper.startsWith('/api/plugin-files/')) {
      wallpaperId = wallpaper
    }

    ambienceStore.apply({
      enabled: wallpaperId !== null,
      wallpaper: wallpaperId,
      glass: { opacity: glassOpacity, blur: glassBlur, wallpaperOpacity },
    })
  }

  // 初始读取 + 推送。
  void ctx.config.get().then(pushToAmbience).catch(() => {/* RPC 未就绪时跳过 */})

  // 配置变更 → 实时更新（SettingsPlugins 表单切换 → plugin_config_set → WS → 此处）。
  const offConfig = ctx.config.onConfigChange(pushToAmbience)

  return () => {
    offConfig()
  }
}
