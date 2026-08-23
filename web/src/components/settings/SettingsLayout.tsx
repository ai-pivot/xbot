/**
 * SettingsLayout —— 布局定制面板（VSCode 式）。
 *
 * 列出所有布局项（内置按钮 + 插件 view），每项可选目标 slot（默认/其他）。
 * 移动项立即生效并持久化到 localStorage；「恢复默认」重置全部。
 */
import { useMemo, useState } from 'react'

import { Button } from '@/components/ui/button'
import { SettingsSection } from '@/components/settings/SettingsSection'
import { useI18n } from '@/providers/i18n'
import { useLayoutConfig } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS, type LayoutSlotId } from '@/plugin-runtime/layoutTypes'

const SLOT_LABELS: Record<LayoutSlotId, string> = {
  'mobile.bottom_nav': '📱 底部导航',
  'mobile.top_bar': '📱 顶栏',
  'desktop.activity_bar': '🖥️ 左侧栏',
  'desktop.sidebar': '🖥️ 右侧面板',
  'desktop.info_bar': '🖥️ 底部信息栏',
  'desktop.main': '🖥️ 主编辑区',
}

/** 内置项 id → 显示名（插件项用 view 自带 title）。 */
const BUILTIN_NAMES: Record<string, string> = {
  [BUILTIN_LAYOUT_ITEMS.mobileTools]: '工具按钮',
  [BUILTIN_LAYOUT_ITEMS.mobileNewChat]: '新建会话',
  [BUILTIN_LAYOUT_ITEMS.mobileSettings]: '设置',
  [BUILTIN_LAYOUT_ITEMS.desktopSessions]: '会话列表',
  [BUILTIN_LAYOUT_ITEMS.desktopFiles]: '文件面板',
  [BUILTIN_LAYOUT_ITEMS.desktopSearch]: '搜索面板',
  [BUILTIN_LAYOUT_ITEMS.desktopInfo]: '信息面板',
  [BUILTIN_LAYOUT_ITEMS.desktopTasks]: '任务面板',
  [BUILTIN_LAYOUT_ITEMS.desktopTerminal]: '终端面板',
}

export function SettingsLayout() {
  const { t } = useI18n()
  const { allItems, overrides, moveItem, moveItemTo, resetItem, resetAll } = useLayoutConfig()
  const [changed, setChanged] = useState(0) // force re-render after moves
  const [dragOverId, setDragOverId] = useState<string | null>(null)

  const slots = useMemo(() => Object.keys(SLOT_LABELS) as LayoutSlotId[], [])

  // 按默认 slot 分组展示。
  const grouped = useMemo(() => {
    const map = new Map<LayoutSlotId, typeof allItems>()
    for (const item of allItems) {
      const eff = overrides[item.id] ?? item.slot
      const list = map.get(eff) ?? []
      list.push(item)
      map.set(eff, list)
    }
    return [...map.entries()].sort((a, b) => slots.indexOf(a[0]) - slots.indexOf(b[0]))
  }, [allItems, overrides, slots, changed])

  const itemName = (id: string, title: string) => BUILTIN_NAMES[id] ?? title

  return (
    <div className="flex flex-col">
      <SettingsSection
        title={t('settings.nav.layout')}
        description="把按钮/面板移动到任意位置（手机底部导航、顶栏、桌面侧栏等），立即生效并记住偏好。"
      >
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => { resetAll(); setChanged((v) => v + 1) }}>
            恢复默认布局
          </Button>
        </div>
      </SettingsSection>

      {grouped.map(([slot, items]) => (
        <SettingsSection key={slot} title={SLOT_LABELS[slot] ?? slot} description={`${items.length} 项`}>
          <div className="flex flex-col gap-2">
            {items.map((item) => {
              const eff = overrides[item.id] ?? item.slot
              return (
                <div
                  key={item.id}
                  draggable
                  onDragStart={(e) => {
                    e.dataTransfer.setData('text/plain', item.id)
                    e.dataTransfer.effectAllowed = 'move'
                  }}
                  onDragOver={(e) => {
                    // 拖到另一项上 = 移到该项所在 slot（drop zone）
                    if (e.dataTransfer.types.includes('text/plain')) {
                      e.preventDefault()
                      e.dataTransfer.dropEffect = 'move'
                      setDragOverId(item.id)
                    }
                  }}
                  onDragLeave={() => setDragOverId((id) => (id === item.id ? null : id))}
                  onDrop={(e) => {
                    e.preventDefault()
                    const src = e.dataTransfer.getData('text/plain') || dragOverId
                    if (src && src !== item.id) {
                      // 拖到某项上 = 插入到该项之前（与真实 UI 的插入线语义一致）。
                      moveItemTo(src, slot, { beforeId: item.id })
                      setChanged((v) => v + 1)
                    }
                    setDragOverId(null)
                  }}
                  className={`flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 transition-shadow ${
                    dragOverId === item.id ? 'ring-2 ring-accent' : ''
                  }`}
                >
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{itemName(item.id, item.title)}</div>
                    <div className="truncate font-mono text-[10px] text-text-muted">{item.id}</div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5">
                    <select
                      value={eff}
                      onChange={(e) => { moveItem(item.id, e.target.value as LayoutSlotId); setChanged((v) => v + 1) }}
                      className="rounded-md border border-border bg-bg-secondary px-2 py-1 text-xs"
                    >
                      {slots.map((s) => (
                        <option key={s} value={s}>{SLOT_LABELS[s]}</option>
                      ))}
                    </select>
                    {overrides[item.id] !== undefined && (
                      <Button type="button" variant="ghost" size="sm" onClick={() => { resetItem(item.id); setChanged((v) => v + 1) }}>
                        重置
                      </Button>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        </SettingsSection>
      ))}
    </div>
  )
}
