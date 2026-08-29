/**
 * ambience API — ctx.ui.ambience 宿主实现（AmbienceAPI 接口的运行时）。
 *
 * 组合三层：ambienceStore（profile/插件壁纸/overlay）+ IndexedDB 用户壁纸
 * （wallpapers.ts 持久层）+ 上传管线（canvas 压缩）。
 * usePluginRuntimeHost 把它注入 PluginUI（host.ui.ambience）——插件经
 * ctx.ui.ambience.* 调用（见 plugin-api/ambience.ts 类型契约）。
 */
import type { AmbienceAPI, WallpaperPreset } from '@/plugin-api'
import { ambienceStore } from './store'
import { listUserWallpapers, removeUserWallpaper, uploadUserWallpaper } from './wallpapers'

/** dataURL → CSS background（center/cover，无拉伸）。 */
function dataUrlCss(dataUrl: string): string {
  return `url(${JSON.stringify(dataUrl)}) center/cover no-repeat`
}

/**
 * 预载 IndexedDB 用户壁纸 → store 内存缓存（幂等，App 启动调用一次）。
 * AmbienceBackground 挂载时执行；预载失败静默（IndexedDB 不可用/为空）。
 */
let preloadStarted = false
export async function initUserWallpaperSync(): Promise<void> {
  if (preloadStarted) return
  preloadStarted = true
  try {
    const all = await listUserWallpapers()
    for (const w of all) {
      ambienceStore.registerUserWallpaper({ id: w.id, name: w.name, css: dataUrlCss(w.dataUrl) })
    }
  } catch (err) {
    console.warn('[ambience] 用户壁纸预载失败（IndexedDB）', err)
  }
}

/** AmbienceAPI 宿主实现（注入 PluginUI → ctx.ui.ambience）。 */
export const ambienceAPI: AmbienceAPI = {
  /** 插件预设（resolvedId = pluginId:presetId，src 解析为完整 URL）+ 用户上传合并。 */
  listWallpapers(): WallpaperPreset[] {
    return [
      ...ambienceStore.listWallpapers().map(({ resolvedId, url, ...preset }) => ({
        ...preset,
        id: resolvedId,
        // 插件资产相对路径 → 完整 URL（插件展示/调试用；resolveWallpaper 消费 resolvedId）。
        src: url ?? preset.src,
      })),
      ...ambienceStore.listUserWallpaperMeta().map((u) => ({ id: u.id, name: u.name })),
    ]
  },

  /** 上传壁纸（浏览器端 canvas 压缩 + IndexedDB + store 缓存）。 */
  async uploadWallpaper(file, opts) {
    const rec = await uploadUserWallpaper(file, opts?.maxDim ?? 1600)
    ambienceStore.registerUserWallpaper({ id: rec.id, name: rec.name, css: dataUrlCss(rec.dataUrl) })
    return { id: rec.id, name: rec.name }
  },

  /** 删除用户上传壁纸（插件预设不可删）；当前壁纸被删时回落无壁纸。 */
  removeWallpaper(id) {
    if (!id.startsWith('user:')) return false
    if (ambienceStore.activeProfile().wallpaper === id) {
      ambienceStore.apply({ wallpaper: null })
    }
    ambienceStore.removeUserWallpaper(id)
    void removeUserWallpaper(id)
    return true
  },

  apply(profile) {
    ambienceStore.apply(profile)
  },

  getActive() {
    return ambienceStore.activeProfile()
  },

  setSessionProfile(chatID, profile) {
    ambienceStore.setSessionProfile(chatID, profile)
  },
}
