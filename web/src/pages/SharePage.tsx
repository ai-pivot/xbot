/**
 * SharePage —— 公开分享页（**无需登录**：token 即凭据）。
 *
 * 这是「把面板分享给朋友」的落地页。宿主对内容零感知，只做三件事：
 *   1. 按 token 拉取 artifact（GET /api/share/{token}，故意不走鉴权）；
 *   2. 动态加载**产出它的那个插件**（artifact.module_url）；
 *   3. 交给插件自己注册的 shareRenderer（按 contentType 匹配）渲染。
 *
 * 所以新增任何可分享的内容形态都不需要改这个文件 —— 是插件在声明能力，
 * 与 messageRenderer 同一模式。未分享的内容不在 shared_artifacts 表里，
 * 公开路径无从触达。
 */
import { useEffect, useMemo, useState } from 'react'

import { I18nProvider } from '@/providers/i18n'
import type { ShareRendererContribution, SharedArtifact } from '@/plugin-api'

interface ShareResponse {
  token: string
  plugin_id: string
  content_type: string
  module_url?: string
  payload: string
  title: string
  created_at: string
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'ready'; artifact: SharedArtifact; render: ShareRendererContribution }
  | { kind: 'error'; message: string }

export function SharePage({ token }: { token: string }) {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    const run = async () => {
      try {
        const res = await fetch(`/api/share/${encodeURIComponent(token)}`, {
          headers: { Accept: 'application/json' },
        })
        const envelope = (await res.json()) as { ok?: boolean; data?: ShareResponse; error?: { message?: string } }
        const data = envelope.data ?? (envelope as unknown as ShareResponse)
        if (!res.ok || !data?.content_type) {
          throw new Error(envelope.error?.message || '分享不存在或已失效')
        }
        const artifact: SharedArtifact = {
          token: data.token ?? token,
          pluginId: data.plugin_id,
          contentType: data.content_type,
          payload: data.payload,
          title: data.title ?? '',
          createdAt: data.created_at ?? '',
          expiresAt: '',
        }
        if (!data.module_url) {
          throw new Error('找不到该分享对应的插件渲染器')
        }

        // 加载产出插件并让它注册渲染器。ctx 只提供它需要的两项：
        // contributes.register（声明式贡献点）与 share.registerRenderer。
        const renderers: ShareRendererContribution[] = []
        const mod = (await import(/* @vite-ignore */ data.module_url)) as {
          activate?: (ctx: unknown) => unknown
        }
        const minimalCtx = {
          meta: { id: data.plugin_id, version: '' },
          contributes: {
            register: (c: ShareRendererContribution) => {
              if (c && c.kind === 'shareRenderer') renderers.push(c)
              return () => {}
            },
            registerAll: (cs: ShareRendererContribution[]) => {
              for (const c of cs) if (c && c.kind === 'shareRenderer') renderers.push(c)
              return () => {}
            },
          },
          share: {
            registerRenderer: (d: ShareRendererContribution) => {
              renderers.push(d)
              return () => {}
            },
          },
        }
        try {
          await mod.activate?.(minimalCtx)
        } catch {
          // activate 可能做超出分享所需的初始化（rpc 等）——渲染器若已登记就不影响。
        }

        const renderer = renderers.find((r) => r.contentType === artifact.contentType)
        if (!renderer) {
          throw new Error('该内容类型没有可用的分享渲染器')
        }
        if (!cancelled) setState({ kind: 'ready', artifact, render: renderer })
      } catch (e) {
        if (!cancelled) {
          setState({ kind: 'error', message: e instanceof Error ? e.message : '加载失败' })
        }
      }
    }
    void run()
    return () => {
      cancelled = true
    }
  }, [token])

  const body = useMemo(() => {
    if (state.kind === 'loading') {
      return <div className="p-8 text-center text-sm text-text-muted">加载中…</div>
    }
    if (state.kind === 'error') {
      return (
        <div className="mx-auto max-w-md p-8 text-center">
          <div className="text-lg font-medium text-text-primary">无法打开这个分享</div>
          <div className="mt-2 text-sm text-text-muted">{state.message}</div>
        </div>
      )
    }
    return <>{state.render.render(state.artifact)}</>
  }, [state])

  return (
    <I18nProvider>
      <div className="min-h-dvh bg-bg-primary">
        <header className="flex items-center gap-2 border-b border-border px-4 py-2.5">
          <span className="text-sm font-medium text-text-primary">
            {state.kind === 'ready' && state.artifact.title ? state.artifact.title : '共享面板'}
          </span>
          <span className="text-xs text-text-muted">Shared from xbot</span>
        </header>
        <main className="p-3 md:p-5">{body}</main>
      </div>
    </I18nProvider>
  )
}
