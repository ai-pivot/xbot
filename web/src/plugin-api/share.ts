/**
 * Shared artifacts（通用插件能力）—— 插件把自己的内容发布成「自带凭据的分享链接」。
 *
 * 宿主对内容零感知：`contentType` 由产出插件命名、`payload` 对宿主不透明，
 * 只有该插件自己注册的 share renderer 能解释它。`token` 即凭据（256 位高熵
 * 随机），任何人拿到链接即可读取这一份快照，**无需登录** —— 这正是「分享给朋友」
 * 的语义。
 *
 * 与 messageRenderer 同一模式：能力由插件**声明**，宿主只做通用派发，核心代码
 * 不出现任何具体内容类型（不感知 genui / 任何其他插件）。
 */
import type { ReactNode } from 'react'

/** 一份不可变的分享快照。 */
export interface SharedArtifact {
  token: string
  /** 产出该 artifact 的插件 id —— 公开分享页据此加载对应插件取得渲染器。 */
  pluginId: string
  /** 产出插件命名，如 'xbot.genui/tsx'。 */
  contentType: string
  /** 对宿主不透明；只有对应 share renderer 能解释。 */
  payload: string
  title: string
  createdAt: string
  /** RFC3339，或 '' = 永不过期。 */
  expiresAt: string
}

/** create() 的返回：artifact + 可直接分享的站内路径。 */
export interface ShareLink extends SharedArtifact {
  /** 站内路径，如 `/s/<token>`（拼上站点域名即可分享）。 */
  path: string
}

/** 公开渲染器：宿主在 /s/:token 页按 contentType 派发到它。 */
export interface ShareRendererContribution {
  kind: 'shareRenderer'
  id: string
  /** 负责的 artifact 类型，与 create() 传入的 contentType 一致。 */
  contentType: string
  /** 渲染一份 artifact。返回 null 表示「不是我负责的」，宿主继续 fallback。 */
  render: (artifact: SharedArtifact) => ReactNode | null
}

export interface CreateShareInput {
  contentType: string
  payload: string
  title?: string
  /** <= 0 或省略 = 永不过期。 */
  expiresInDays?: number
}

export interface ShareAPI {
  /** 发布一份快照，返回可直接分享的链接。 */
  create(input: CreateShareInput): Promise<ShareLink>
  /** 列出本插件（当前会话）创建的分享。 */
  list(): Promise<ShareLink[]>
  /** 撤销一份分享（立即失效）。 */
  revoke(token: string): Promise<void>
  /** 注册公开渲染器；宿主在分享页据 contentType 派发。返回退订函数。 */
  registerRenderer(decl: ShareRendererContribution): () => void
}
