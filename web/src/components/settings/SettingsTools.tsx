/**
 * SettingsTools — 内置工具激活面板（设置 → 工具）。
 *
 * 用户要求（2026-09-17）：「设置里再加上一个 tool 设置面板，允许配置哪些内置 tool
 * 激活，只有激活的 tool 会被上下文里带上」。
 *
 * 契约：开关 = `set_tool_enabled`（admin）→ 运行时改注册表的**激活集**，并把
 * `config.DisabledTools` 持久化（唯一表示，没有第二份副本）。后端
 * `AsDefinitionsForSession` 过滤未激活的工具（**不进 LLM 上下文**），
 * `Registry.Get` 对未激活工具返回 not-found（**不可执行**）。启停可逆 ——
 * 重新打开立即生效，无需重启。
 */
import { useCallback, useEffect, useState } from 'react'

import { postAPI } from '@/lib/api'
import { useI18n } from '@/providers/i18n'
import { Switch } from '@/components/ui/switch'

import { SettingsSection } from './SettingsSection'

interface ToolSetting {
  name: string
  description: string
  enabled: boolean
}

async function rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
  return postAPI<T>('/api/rpc', { method, params })
}

export function SettingsTools() {
  const { t } = useI18n()
  const [tools, setTools] = useState<ToolSetting[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const res = await rpc<{ tools: ToolSetting[] }>('get_tools_settings')
      setTools(res?.tools ?? [])
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setTools([])
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const toggle = async (name: string, next: boolean) => {
    setBusy(name)
    // 乐观更新：RPC 很快，失败再回滚（与 SettingsAgent 同一模式）。
    setTools((prev) => prev?.map((x) => (x.name === name ? { ...x, enabled: next } : x)) ?? prev)
    try {
      await rpc('set_tool_enabled', { name, enabled: next })
    } catch (err) {
      setTools((prev) => prev?.map((x) => (x.name === name ? { ...x, enabled: !next } : x)) ?? prev)
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  const active = tools?.filter((x) => x.enabled).length ?? 0

  return (
    <div className="flex flex-col gap-4 p-5" data-testid="tools-settings">
      <SettingsSection title={t('settings.tools.title')} description={t('settings.tools.description')}>
        {error ? (
          <p className="text-xs text-red-500" role="alert">
            {error}
          </p>
        ) : null}
        {tools === null ? (
          <p className="text-xs text-text-muted" data-testid="tools-loading">
            {t('settings.tools.loading')}
          </p>
        ) : (
          <>
            <p className="text-xs text-text-muted" data-testid="tools-summary">
              {t('settings.tools.summary', { active, total: tools.length })}
            </p>
            <ul className="flex flex-col divide-y divide-border">
              {tools.map((tool) => (
                <li key={tool.name} className="flex items-start gap-3 py-2.5" data-testid={`tool-row-${tool.name}`}>
                  <Switch
                    checked={tool.enabled}
                    disabled={busy === tool.name}
                    onCheckedChange={(v) => void toggle(tool.name, v)}
                    aria-label={tool.name}
                  />
                  <div className="flex min-w-0 flex-col gap-0.5">
                    <span className="font-mono text-sm text-text-primary">{tool.name}</span>
                    {tool.description ? (
                      <span className="text-xs text-text-muted">{tool.description}</span>
                    ) : null}
                  </div>
                </li>
              ))}
            </ul>
          </>
        )}
      </SettingsSection>
    </div>
  )
}
