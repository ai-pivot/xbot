/**
 * LeftActivityBar —— 桌面左侧栏的「插件 view 隔离区」。
 *
 * 左侧栏本体（SessionSidebar）是 channel/会话列表的**整体区域**（由
 * AppShell 硬编码独占），这里只渲染「用户主动移到桌面左侧 slot
 * （desktop.activity_bar）的插件 view」，与 channel 列表物理隔离（独立
 * section + 分隔线），避免插件 view 混进会话列表。
 *
 * 多个插件 view 时渲染 tab 栏切换；仅一个时直接渲染其内容。
 */
import { useEffect, useState, type ReactNode } from 'react'

import { useOptionalPluginRuntime } from '@/plugin-runtime'
import { PluginView } from '@/plugin-runtime/PluginView'
import { useLayoutItems } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS, LAYOUT_GROUPS } from '@/plugin-runtime/layoutTypes'
import { CollapsibleGroup } from './CollapsibleGroup'

interface Panel {
  id: string
  pluginId: string
  title: string
  view: import('@/plugin-api').ViewContribution
}

export function LeftActivityBar(): ReactNode {
  const runtime = useOptionalPluginRuntime()
  const layoutItems = useLayoutItems('desktop.activity_bar')
  const [panels, setPanels] = useState<Panel[]>([])
  const [activeId, setActiveId] = useState<string>('')

  // 组合：desktop.activity_bar slot 里「非会话列表」的布局项（= 用户移入的
  // 插件 view），匹配 runtime 的 view 贡献渲染。
  useEffect(() => {
    if (!runtime) {
      setPanels([])
      return
    }
    const recompute = () => {
      const viewIds = new Set(
        layoutItems
          .filter((it) => it.id !== BUILTIN_LAYOUT_ITEMS.desktopSessions)
          .map((it) => it.id),
      )
      const all = runtime.listAllViews()
      const next: Panel[] = all
        .filter(({ view }) => viewIds.has(view.id))
        .map(({ pluginId, view }) => ({ id: view.id, pluginId, title: view.title, view }))
      setPanels(next)
      setActiveId((cur) => (next.some((p) => p.id === cur) ? cur : next[0]?.id ?? ''))
    }
    recompute()
    return runtime.subscribeViews(recompute)
  }, [runtime, layoutItems])

  if (panels.length === 0) return null
  const active = panels.find((p) => p.id === activeId) ?? panels[0]

  return (
    <CollapsibleGroup groupId={LAYOUT_GROUPS.plugins} title="插件">
      <div className="flex flex-col">
        {panels.length > 1 && (
          <div className="flex items-center gap-1 border-b border-[var(--border)] px-1 py-1">
            {panels.map((p) => (
              <button
                key={p.id}
                type="button"
                onClick={() => setActiveId(p.id)}
                className={`truncate rounded px-2 py-1 text-xs transition-colors ${
                  p.id === active.id
                    ? 'bg-[var(--bg-tertiary)] font-medium text-text-primary'
                    : 'text-text-muted hover:text-text-secondary'
                }`}
              >
                {p.title}
              </button>
            ))}
          </div>
        )}
        <div className="min-h-0 overflow-y-auto">
          <PluginView pluginId={active.pluginId} view={active.view} />
        </div>
      </div>
    </CollapsibleGroup>
  )
}