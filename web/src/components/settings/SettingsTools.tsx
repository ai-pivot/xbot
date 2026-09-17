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
  /** 后端字段名（Go: json:"server_name,omitempty"）—— 载入时归一化到 serverName。 */
  server_name?: string
  /** MCP 工具专有：所属 MCP server 名（后端来自 MCP bridge 的真实名字）。 */
  serverName?: string
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
      // 字段归一化：后端用 snake_case（server_name），组件内部统一用 serverName。
      // 曾经直接读 serverName ⇒ 永远 undefined ⇒ MCP 分组/服务器开关静默失效。
      setTools((res?.tools ?? []).map((tool) => ({ ...tool, serverName: tool.server_name ?? tool.serverName })))
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
  // MCP 工具按**后端给的真实 server 名**分组（不用名字前缀猜）；服务器级开关
  // = 批量启停该服务器的全部工具（复用 set_tool_enabled，无新增后端语义）。
  const builtin = (tools ?? []).filter((x) => !x.serverName)
  const mcpGroups = new Map<string, ToolSetting[]>()
  for (const tool of tools ?? []) {
    if (!tool.serverName) continue
    const arr = mcpGroups.get(tool.serverName) ?? []
    arr.push(tool)
    mcpGroups.set(tool.serverName, arr)
  }

  const setMany = async (names: string[], next: boolean) => {
    setBusy(names.join(','))
    setTools((prev) => prev?.map((x) => (names.includes(x.name) ? { ...x, enabled: next } : x)) ?? prev)
    try {
      for (const name of names) {
        await rpc('set_tool_enabled', { name, enabled: next })
      }
    } catch (err) {
      setTools((prev) => prev?.map((x) => (names.includes(x.name) ? { ...x, enabled: !next } : x)) ?? prev)
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  const renderTool = (tool: ToolSetting, indented = false) => (
    <li
      key={tool.name}
      className={`flex items-start gap-3 py-2.5 ${indented ? 'pl-6' : ''}`}
      data-testid={`tool-row-${tool.name}`}
    >
      <Switch
        checked={tool.enabled}
        disabled={busy === tool.name}
        onCheckedChange={(v) => void toggle(tool.name, v)}
        aria-label={tool.name}
      />
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="font-mono text-sm text-text-primary">{tool.name}</span>
        {tool.description ? <span className="text-xs text-text-muted">{tool.description}</span> : null}
      </div>
    </li>
  )

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
              {builtin.map((tool) => renderTool(tool))}
            </ul>

            {mcpGroups.size > 0 ? (
              <div className="mt-4 flex flex-col gap-2" data-testid="mcp-section">
                <span className="text-xs font-medium text-text-secondary">
                  {t('settings.tools.mcpServers')}
                </span>
                {[...mcpGroups.entries()].map(([server, group]) => {
                  const names = group.map((g) => g.name)
                  const allOn = group.every((g) => g.enabled)
                  return (
                    <div key={server} className="flex flex-col" data-testid={`mcp-server-${server}`}>
                      <div className="flex items-center gap-3 py-2">
                        <Switch
                          checked={allOn}
                          disabled={busy === names.join(',')}
                          onCheckedChange={(v) => void setMany(names, v)}
                          aria-label={server}
                        />
                        <div className="flex min-w-0 flex-col gap-0.5">
                          <span className="font-mono text-sm text-text-primary">{server}</span>
                          <span className="text-xs text-text-muted">
                            {t('settings.tools.mcpToolCount', { count: group.length })}
                          </span>
                        </div>
                      </div>
                      <ul className="flex flex-col divide-y divide-border">
                        {group.map((tool) => renderTool(tool, true))}
                      </ul>
                    </div>
                  )
                })}
              </div>
            ) : null}
          </>
        )}
      </SettingsSection>
    </div>
  )
}
