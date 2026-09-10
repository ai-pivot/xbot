/**
 * ShareAPI 运行时实现（通用能力，零内容语义）。
 *
 * - create / list / revoke → 鉴权 HTTP：发布公开链接是「需登录的显式动作」。
 * - registerRenderer → 只登记一个 contribution；公开分享页由宿主渲染，按
 *   artifact.contentType 派发到对应插件的渲染器。
 *
 * 宿主不知道 payload 里是什么 —— 只有产出它的插件能解释。
 */
import type { CreateShareInput, ShareAPI, ShareLink, ShareRendererContribution } from '@/plugin-api'
import { postAPI } from '@/lib/api'

interface ShareCreateResponse {
  token: string
  plugin_id: string
  content_type: string
  title: string
  created_at: string
  expires_at: string
  path: string
}

interface ShareListResponse {
  shares?: Array<{
    token: string
    plugin_id?: string
    content_type: string
    title: string
    created_at: string
    expires_at: string
  }>
}

export interface ShareServiceDeps {
  /** 产出插件 id —— 由 runtime 注入；插件自己不传，也无法冒充别的插件。 */
  pluginId: string
  /** 登记一个 shareRenderer 贡献点（由 runtime registry 持有，返回退订）。 */
  registerRenderer: (decl: ShareRendererContribution) => () => void
}

export function createShareAPI(deps: ShareServiceDeps): ShareAPI {
  return {
    async create(input: CreateShareInput): Promise<ShareLink> {
      const data = await postAPI<ShareCreateResponse>('/api/share/create', {
        plugin_id: deps.pluginId,
        content_type: input.contentType,
        payload: input.payload,
        title: input.title ?? '',
        expires_in_days: input.expiresInDays ?? 0,
      })
      return {
        token: data.token,
        pluginId: data.plugin_id,
        contentType: data.content_type,
        payload: input.payload,
        title: data.title,
        createdAt: data.created_at,
        expiresAt: data.expires_at,
        path: data.path,
      }
    },

    async list(): Promise<ShareLink[]> {
      const data = await postAPI<ShareListResponse>('/api/share/list', {})
      // list 不回传 payload —— 列表只需要元数据。
      return (data.shares ?? []).map((s) => ({
        token: s.token,
        pluginId: s.plugin_id ?? deps.pluginId,
        contentType: s.content_type,
        payload: '',
        title: s.title,
        createdAt: s.created_at,
        expiresAt: s.expires_at,
        path: `/s/${s.token}`,
      }))
    },

    async revoke(token: string): Promise<void> {
      await postAPI('/api/share/revoke', { token })
    },

    registerRenderer: deps.registerRenderer,
  }
}
