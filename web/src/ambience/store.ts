/**
 * ambience store — 氛围装饰层单例（模块级，照 layoutRegistry 模式）。
 *
 * 职责（Ambience Layer 方案 · 四层装饰模型）：
 *   z:0  wallpaper  壁纸层（AmbienceRoot 渲染；CSS 变量覆盖法玻璃化内容层）
 *   z:10 app        现有 UI（--bg-primary/secondary/tertiary alpha 化自动透明）
 *   z:50 decoration 装饰层（粒子/氛围光，pointer-events:none）
 *   z:60 hud        悬浮交互层（桌宠/挂件，可拖拽 + 位置记忆）
 *
 * 数据流：
 *   全局 profile + 会话覆盖（localStorage 'xbot:ambience'，user_settings 跨设备同步）
 *   插件壁纸预设（registry 激活/卸载 → syncPluginWallpapers 全量同步）
 *   用户上传壁纸（IndexedDB 持久化 → 内存缓存 dataURL → 'user:' 前缀解析）
 *   overlay 挂载表（ctx.ui.mountOverlay → store → AmbienceRoot 渲染）
 *
 * React 桥：useSyncExternalStore（useAmbience）——AmbienceRoot 消费快照渲染。
 */
import { useSyncExternalStore, type ComponentType } from 'react'
import type {
  AmbienceProfile,
  GlassParams,
  OverlayHandle,
  OverlayLayer,
  OverlayMountOptions,
  WallpaperPreset,
} from '@/plugin-api'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'

/** overlay 挂载项（store 内部；AmbienceRoot 渲染）。 */
export interface OverlayEntry {
  id: string
  layer: OverlayLayer
  component: ComponentType
  props: Record<string, unknown>
  visible: boolean
  options: OverlayMountOptions
  /** 挂载方插件 id（归属追踪——插件卸载时清理残留 overlay）。 */
  pluginId: string
}

/** 用户上传壁纸元数据（dataURL 大对象不入快照，仅元数据）。 */
export interface UserWallpaperMeta {
  id: string
  name: string
}

export interface AmbienceSnapshot {
  global: AmbienceProfile
  sessionOverrides: Record<string, Partial<AmbienceProfile>>
  /** 插件贡献的壁纸预设（pluginId 前缀 id 已去重合并）。 */
  wallpapers: { pluginId: string; preset: WallpaperPreset }[]
  /** 用户上传壁纸元数据（IndexedDB 预载）。 */
  userWallpapers: UserWallpaperMeta[]
  overlays: OverlayEntry[]
  activeChatID: string | null
}

// ── 默认值 ────────────────────────────────────────────────────────────────────

export const DEFAULT_GLASS: Required<Pick<GlassParams, 'opacity' | 'blur' | 'wallpaperOpacity'>> = {
  opacity: 0.82,
  // 柔焦默认 0（2026-08-29 性能事故）：壁纸全屏 filter: blur 是 GPU 大纹理
  // 每帧重合成的大户（Layerize/GPUTask 全速）——用户显式调滑杆才开启。
  blur: 0,
  // 壁纸不透明度默认 1（完全显示）——用户上传花哨图片时可降低避免遮挡文字。
  wallpaperOpacity: 1,
}

const DEFAULT_PROFILE: AmbienceProfile = {
  enabled: false,
  wallpaper: null,
  glass: {},
}

// ── 持久化（localStorage + user_settings 跨设备同步）────────────────────────

const LS_KEY = 'xbot:ambience'

interface PersistedShape {
  global: AmbienceProfile
  sessionOverrides: Record<string, Partial<AmbienceProfile>>
}

function loadPersisted(): PersistedShape {
  try {
    const raw = localStorage.getItem(LS_KEY)
    if (!raw) return { global: DEFAULT_PROFILE, sessionOverrides: {} }
    const parsed = JSON.parse(raw) as Partial<PersistedShape>
    return {
      global: { ...DEFAULT_PROFILE, ...parsed.global, glass: { ...parsed.global?.glass } },
      sessionOverrides: parsed.sessionOverrides ?? {},
    }
  } catch {
    return { global: DEFAULT_PROFILE, sessionOverrides: {} }
  }
}

function persist(): void {
  try {
    const shape: PersistedShape = { global: state.global, sessionOverrides: state.sessionOverrides }
    localStorage.setItem(LS_KEY, JSON.stringify(shape))
    syncSettingToServer(LS_KEY, JSON.stringify(shape))
  } catch { /* ignore quota errors */ }
}

// ── store 核心（模块级单例）──────────────────────────────────────────────────

interface UserWallpaperEntry extends UserWallpaperMeta {
  /** dataURL 直接作 CSS background（内存缓存，IndexedDB 为持久层）。 */
  css: string
}

const state: PersistedShape & {
  pluginWallpapers: { pluginId: string; presets: readonly WallpaperPreset[] }[]
  /** 用户上传壁纸（id → dataURL 缓存；'user:' 前缀解析）。 */
  userWallpapers: Map<string, UserWallpaperEntry>
  overlays: OverlayEntry[]
  activeChatID: string | null
} = {
  ...loadPersisted(),
  pluginWallpapers: [],
  userWallpapers: new Map(),
  overlays: [],
  activeChatID: null,
}

const listeners = new Set<() => void>()

function emit(): void {
  for (const l of listeners) l()
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

/** 合并后的全局快照（不可变引用——useSyncExternalStore 要求）。
 *
 * 集合引用稳定（2026-08-29 桌宠事件风暴 bug 修复）：wallpapers/userWallpapers
 * 按数据源身份缓存数组引用——无关 mutate（overlay setProps 高频）不换引用，
 * 消费方 useMemo deps（AmbienceBackground 的 wp → 玻璃化 effect）不失效。
 * 否则每条 progress 事件重跑 getComputedStyle + setProperty --bg-*（全站
 * CSS 变量失效）→ layout/paint 风暴（git RPC 饿死、UI 全局卡顿）。
 */
let snapshotCache: AmbienceSnapshot | null = null
let wallpapersArr: AmbienceSnapshot['wallpapers'] | null = null
let wallpapersSrc: typeof state.pluginWallpapers | null = null
let userWallpapersArr: AmbienceSnapshot['userWallpapers'] | null = null
let userWallpapersSrc: Map<string, UserWallpaperEntry> | null = null

function snapshot(): AmbienceSnapshot {
  if (!snapshotCache) {
    if (wallpapersSrc !== state.pluginWallpapers || !wallpapersArr) {
      wallpapersSrc = state.pluginWallpapers
      wallpapersArr = state.pluginWallpapers.flatMap(({ pluginId, presets }) =>
        presets.map((preset) => ({ pluginId, preset })),
      )
    }
    if (userWallpapersSrc !== state.userWallpapers || !userWallpapersArr) {
      userWallpapersSrc = state.userWallpapers
      userWallpapersArr = [...state.userWallpapers.values()].map(({ id, name }) => ({ id, name }))
    }
    snapshotCache = {
      global: state.global,
      sessionOverrides: state.sessionOverrides,
      wallpapers: wallpapersArr,
      userWallpapers: userWallpapersArr,
      overlays: state.overlays,
      activeChatID: state.activeChatID,
    }
  }
  return snapshotCache
}

function mutate(fn: () => void): void {
  fn()
  snapshotCache = null
  emit()
}

/** props 浅比较（overlay props 均为原始值——PetWidget mood/emoji 等字符串）。 */
function shallowEqualProps(a: Record<string, unknown>, b: Record<string, unknown>): boolean {
  if (a === b) return true
  const ka = Object.keys(a)
  if (ka.length !== Object.keys(b).length) return false
  return ka.every((k) => a[k] === b[k])
}

// ── 公开 API ─────────────────────────────────────────────────────────────────

/** 订阅（组件外也可用；React 组件用 useAmbience）。 */
export const ambienceStore = {
  subscribe,
  get: snapshot,

  /** 当前生效 profile：全局 + 会话覆盖深合并（glass 逐字段）。 */
  activeProfile(): AmbienceProfile {
    const ov = state.activeChatID ? state.sessionOverrides[state.activeChatID] : undefined
    if (!ov) return state.global
    return {
      enabled: ov.wallpaper !== undefined || ov.enabled !== undefined
        ? (ov.enabled ?? state.global.enabled)
        : state.global.enabled,
      wallpaper: ov.wallpaper !== undefined ? ov.wallpaper : state.global.wallpaper,
      glass: { ...state.global.glass, ...ov.glass },
    }
  },

  /** 应用全局 profile 变更（Partial 深合并 glass）。 */
  apply(patch: Partial<AmbienceProfile>): void {
    mutate(() => {
      state.global = {
        enabled: patch.enabled ?? state.global.enabled,
        wallpaper: patch.wallpaper !== undefined ? patch.wallpaper : state.global.wallpaper,
        glass: { ...state.global.glass, ...patch.glass },
      }
      persist()
    })
  },

  /** 会话级覆盖：chatID=null 清除当前会话的覆盖（回落全局）；profile=null 删除该会话覆盖。 */
  setSessionProfile(chatID: string | null, profile: Partial<AmbienceProfile> | null): void {
    mutate(() => {
      const target = chatID ?? state.activeChatID
      if (!target) return
      if (profile === null) {
        delete state.sessionOverrides[target]
      } else {
        state.sessionOverrides[target] = {
          ...state.sessionOverrides[target],
          ...profile,
          glass: { ...state.sessionOverrides[target]?.glass, ...profile.glass },
        }
      }
      persist()
    })
  },

  /** 会话覆盖是否存在（设置 UI 显示「本会话已锁定」徽章用）。 */
  hasSessionOverride(chatID: string): boolean {
    return Boolean(state.sessionOverrides[chatID])
  },

  /** 当前活跃会话（AmbienceRoot 每次 activeSession 变化设置）。 */
  setActiveSession(chatID: string | null): void {
    if (state.activeChatID === chatID) return
    mutate(() => {
      state.activeChatID = chatID
    })
  },

  // ── 插件壁纸（registry 激活/卸载 → 全量同步；usePluginRuntimeHost 消费）──

  /**
   * 全量同步插件壁纸（diff 内部处理）：传入当前激活插件的贡献集合，
   * 消失的插件移除其壁纸 + 清理其 overlay 残留（插件卸载/热加载）。
   */
  syncPluginWallpapers(all: { pluginId: string; presets: readonly WallpaperPreset[] }[]): void {
    mutate(() => {
      const activeIds = new Set(all.map((a) => a.pluginId))
      // 清理已消失插件的 overlay 残留（'builtin' 宿主挂载的不清）。
      if (state.overlays.some((o) => !activeIds.has(o.pluginId) && o.pluginId !== 'builtin')) {
        state.overlays = state.overlays.filter((o) => activeIds.has(o.pluginId) || o.pluginId === 'builtin')
      }
      state.pluginWallpapers = all.filter((a) => a.presets.length > 0)
    })
  },

  /** 列出插件壁纸预设（resolvedId = `pluginId:presetId` + src URL 解析）。 */
  listWallpapers(): (WallpaperPreset & { resolvedId: string; url: string | null })[] {
    return state.pluginWallpapers.flatMap(({ pluginId, presets }) =>
      presets.map((preset) => ({
        ...preset,
        resolvedId: `${pluginId}:${preset.id}`,
        url: preset.src ? `/plugins/${pluginId}/web/${preset.src.replace(/^\/+/, '')}` : null,
      })),
    )
  },

  /** 用户上传壁纸元数据（上传后由 api 层注册进缓存）。 */
  listUserWallpaperMeta(): UserWallpaperMeta[] {
    return [...state.userWallpapers.values()].map(({ id, name }) => ({ id, name }))
  },

  /** 注册用户上传壁纸（wallpapers.ts 上传/预载后调用——dataURL 进内存缓存）。 */
  registerUserWallpaper(entry: UserWallpaperMeta & { css: string }): void {
    mutate(() => {
      // 不可变替换（新 Map 身份）：快照集合身份比较（userWallpapersSrc）依赖
      // 引用变化感知更新——可变 set 不换身份会让快照漏更新。
      state.userWallpapers = new Map(state.userWallpapers)
      state.userWallpapers.set(entry.id, { id: entry.id, name: entry.name, css: entry.css })
    })
  },

  /** 移除用户上传壁纸（IndexedDB 删除由 api 层负责）。 */
  removeUserWallpaper(id: string): void {
    mutate(() => {
      if (!state.userWallpapers.has(id)) return
      state.userWallpapers = new Map(state.userWallpapers)
      state.userWallpapers.delete(id)
    })
  },

  /** 解析壁纸 id（`pluginId:presetId` / `user:<id>` / URL / null）→ 渲染描述。 */
  resolveWallpaper(id: string | null): { css: string; focus: string } | null {
    if (!id) return null
    // 服务器文件 URL（ctx.files 上传——/api/plugin-files/{plugin_id}/{filename}）。
    if (id.startsWith('/api/plugin-files/') || id.startsWith('http://') || id.startsWith('https://')) {
      return { css: `url(${JSON.stringify(id)}) center/cover no-repeat`, focus: 'center' }
    }
    // 用户上传（dataURL 内存缓存）。
    if (id.startsWith('user:')) {
      const u = state.userWallpapers.get(id)
      if (!u) return null
      return { css: `url(${JSON.stringify(u.css)}) center/cover no-repeat`, focus: 'center' }
    }
    // 插件预设。
    for (const { pluginId, presets } of state.pluginWallpapers) {
      for (const preset of presets) {
        if (`${pluginId}:${preset.id}` === id) {
          const url = preset.src ? `/plugins/${pluginId}/web/${preset.src.replace(/^\/+/, '')}` : null
          return {
            css: url ? `url(${JSON.stringify(url)}) center/cover no-repeat` : (preset.css ?? ''),
            focus: preset.focus ?? 'center',
          }
        }
      }
    }
    return null
  },

  // ── overlay 挂载（ctx.ui.mountOverlay 宿主实现——usePluginRuntimeHost 接线）──

  /** 挂载浮层（decoration/hud），返回 disposable handle。 */
  mountOverlay(pluginId: string, options: OverlayMountOptions, component: ComponentType, props?: Record<string, unknown>): OverlayHandle {
    const id = `ovr-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`
    mutate(() => {
      state.overlays = [
        ...state.overlays,
        { id, layer: options.layer, component, props: props ?? {}, visible: true, options, pluginId },
      ]
    })
    return {
      dispose: () => {
        mutate(() => {
          state.overlays = state.overlays.filter((o) => o.id !== id)
        })
      },
      setProps: (next: Record<string, unknown>) => {
        // 性能守卫（2026-08-29 桌宠事件风暴）：相同内容 props 不 mutate 不
        // emit——高频事件（progress.iteration 每条都推相同情绪）下无条件
        // mutate 会引爆全订阅者重渲染链（玻璃化 effect 重跑 = 全站 CSS
        // 变量失效风暴）。props 浅比较足够（overlay props 均为原始值）。
        const cur = state.overlays.find((o) => o.id === id)
        if (cur && shallowEqualProps(cur.props, next)) return
        mutate(() => {
          state.overlays = state.overlays.map((o) => (o.id === id ? { ...o, props: next } : o))
        })
      },
      show: (visible: boolean) => {
        const cur = state.overlays.find((o) => o.id === id)
        if (cur && cur.visible === visible) return
        mutate(() => {
          state.overlays = state.overlays.map((o) => (o.id === id ? { ...o, visible } : o))
        })
      },
    }
  },

  /** 清空某插件的全部浮层。 */
  unmountPluginOverlays(pluginId: string): void {
    if (!state.overlays.some((o) => o.pluginId === pluginId)) return
    mutate(() => {
      state.overlays = state.overlays.filter((o) => o.pluginId !== pluginId)
    })
  },
}

// ── server → localStorage 同步（换浏览器/设备恢复；SETTINGS_SYNCED_EVENT）──

if (typeof window !== 'undefined') {
  window.addEventListener(SETTINGS_SYNCED_EVENT, () => {
    mutate(() => {
      const loaded = loadPersisted()
      state.global = loaded.global
      state.sessionOverrides = loaded.sessionOverrides
    })
  })
}

// ── React hook ────────────────────────────────────────────────────────────────

/** 订阅 ambience 快照（AmbienceRoot / 设置面板用）。 */
export function useAmbience(): AmbienceSnapshot {
  return useSyncExternalStore(ambienceStore.subscribe, ambienceStore.get, ambienceStore.get)
}

/** overlay 层的拖拽位置记忆（hud 层 localStorage）。 */
export function loadOverlayPos(key: string): { top?: number; left?: number; right?: number; bottom?: number } | null {
  try {
    const raw = localStorage.getItem(`xbot:ambience:pos:${key}`)
    return raw ? (JSON.parse(raw) as { top?: number; left?: number; right?: number; bottom?: number }) : null
  } catch {
    return null
  }
}

export function saveOverlayPos(key: string, pos: { top?: number; left?: number; right?: number; bottom?: number }): void {
  try {
    localStorage.setItem(`xbot:ambience:pos:${key}`, JSON.stringify(pos))
  } catch { /* ignore */ }
}
