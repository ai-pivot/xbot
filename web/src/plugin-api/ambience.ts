/**
 * ambience —— 氛围装饰层类型（壁纸 / 玻璃拟态 / 自由浮层 overlay）。
 *
 * 设计（Ambience Layer 方案，四层装饰模型）：
 *   z:0  wallpaper   壁纸层（图片/CSS 渐变，pointer-events:none）
 *   z:10 app         应用内容层（玻璃拟态：--glass-* CSS 变量驱动）
 *   z:50 decoration  装饰层（粒子/氛围光，pointer-events:none）
 *   z:60 hud         悬浮交互层（桌宠/挂件，可交互可拖拽）
 *
 * 宿主渲染分层，插件只声明组件与参数 —— 无 DOM 暴露，安全边界不破
 * （与 v2「类型即契约」一致：壁纸/玻璃走贡献点声明，浮层走 mountOverlay
 * 返回 disposable handle，卸载自动清理）。
 */

/** 壁纸预设（插件贡献点声明或用户上传）。 */
export interface WallpaperPreset {
  /** 插件内唯一；宿主合并时以 `pluginId:presetId` 去重。 */
  id: string
  name: string
  /**
   * 插件资产路径（相对插件 web/ 根，如 `assets/aurora.webp`）。
   * 与 `css` 二选一；两者都给时 `src` 优先（图优先于渐变）。
   */
  src?: string
  /**
   * 纯 CSS background 值（渐变/纯色）——零资产零下载。
   * 例：`linear-gradient(165deg, #070d24, #150e38)`。
   */
  css?: string
  /** background-position 焦点（缺省 `center`）。 */
  focus?: string
}

/** 玻璃拟态参数（内容层）。 */
export interface GlassParams {
  /** 内容层不透明度 0-1（缺省 0.82）。 */
  opacity?: number
  /** 背景模糊 px（缺省 12）。 */
  blur?: number
  /** 壁纸本身不透明度 0-1（缺省 1——调低壁纸变淡露出底色）。 */
  wallpaperOpacity?: number
  /** 玻璃底色（遮罩色，如 `#0b0e14`；缺省不覆盖——走主题色 alpha 化）。 */
  tint?: string
}

/** 主题 token 集（注入 CSS 变量，亮/暗两套）。 */
export interface ThemeTokens {
  light?: Record<string, string>
  dark?: Record<string, string>
}

/** ambience 贡献点：壁纸预设 + 主题 token + 玻璃默认值（纯声明式）。 */
export interface AmbienceContribution {
  kind: 'ambience'
  id: string
  /**
   * 壁纸预设集合（资产走 /plugins/<id>/web/<src> 静态托管）。
   * readonly 声明支持插件 `as const` satisfies 写法（静态数据）。
   */
  readonly wallpapers?: readonly WallpaperPreset[]
  /** 主题 token（CSS 变量名 → 值，亮/暗两套）。 */
  readonly tokens?: ThemeTokens
  /** 玻璃参数默认值（用户未自定义时的缺省）。 */
  readonly glass?: GlassParams
}

/** overlay 层（z 序语义；宿主分配挂载点，插件不接触 DOM）。 */
export type OverlayLayer = 'decoration' | 'hud'

/** overlay 挂载参数。 */
export interface OverlayMountOptions {
  /**
   * 目标层：`decoration`（z:50，纯视觉，pointer-events:none）/
   * `hud`（z:60，可交互——桌宠/挂件）。
   */
  layer: OverlayLayer
  /** 初始位置（px，相对视口四角；缺省右下）。 */
  position?: { top?: number; left?: number; right?: number; bottom?: number }
  /** 可拖拽（仅 hud 层有效——宿主提供拖拽 + 位置记忆 localStorage）。 */
  draggable?: boolean
  /** 位置记忆 key（缺省用 layer + 挂载序号）。 */
  positionKey?: string
}

/** overlay 控制句柄（disposable：卸载自动清理）。 */
export interface OverlayHandle {
  /** 卸载浮层（幂等）。 */
  dispose: () => void
  /** 更新传给组件的 props（插件重渲染入口）。 */
  setProps(props: Record<string, unknown>): void
  /** 显示/隐藏（不卸载，保留挂载状态）。 */
  show(visible: boolean): void
}

/** 生效的氛围 profile（全局或会话级）。 */
export interface AmbienceProfile {
  /** 总开关：false 时壁纸层隐藏、内容层玻璃还原纯色。 */
  enabled: boolean
  /**
   * 当前壁纸 id（`pluginId:presetId` 插件预设 / `user:<id>` 用户上传）。
   * null = 无壁纸（纯色）。
   */
  wallpaper: string | null
  /** 玻璃参数（缺省字段按插件贡献的默认值或宿主缺省）。 */
  glass: GlassParams
}

/** 运行时氛围 API（ctx.ui.ambience）——壁纸/玻璃/会话 profile。 */
export interface AmbienceAPI {
  /** 列出可用壁纸（激活插件的预设 + 用户上传合并，含 css/src 元数据）。 */
  listWallpapers(): WallpaperPreset[]
  /**
   * 上传壁纸（浏览器端 canvas 压缩到 maxDim 内，存 IndexedDB）。
   * 返回 `user:<id>` 预设；本机存储（配置经 user_settings 跨设备同步，
   * 资产本身不同步——其他设备回落插件预设）。
   */
  uploadWallpaper(file: File, opts?: { maxDim?: number }): Promise<WallpaperPreset>
  /** 删除用户上传的壁纸（插件预设不可删）。 */
  removeWallpaper(id: string): boolean
  /** 应用氛围变更（Partial 合并当前生效 profile，立即生效）。 */
  apply(profile: Partial<AmbienceProfile>): void
  /** 当前生效 profile（全局 + 会话覆盖合并结果）。 */
  getActive(): AmbienceProfile
  /**
   * 会话级 profile 覆盖：每会话独立氛围（壁纸/玻璃），切会话即换肤。
   * chatID 传 null 清除当前会话覆盖（回落全局）；profile 传 null 删除该会话覆盖。
   */
  setSessionProfile(chatID: string | null, profile: Partial<AmbienceProfile> | null): void
}
