/**
 * SettingsLayout —— 布局定制面板（VSCode 式）。
 *
 * 列出所有布局项（内置按钮 + 插件 view），每项可选目标 slot（默认/其他）。
 * 移动项立即生效并持久化到 localStorage；「恢复默认」重置全部。
 */
import { useMemo, useState } from 'react'

import { Button } from '@/components/ui/button'
import { SettingsSection } from '@/components/settings/SettingsSection'
import { postAPI } from '@/lib/api'
import { useI18n } from '@/providers/i18n'
import { useLayoutConfig } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS, type LayoutSlotId } from '@/plugin-runtime/layoutTypes'

/** slot id → i18n key（显示名走 t()）。 */
const SLOT_LABELS: Record<LayoutSlotId, string> = {
  'mobile.bottom_nav': 'settings.layout.slotMobileNav',
  'mobile.top_bar': 'settings.layout.slotMobileTop',
  'desktop.activity_bar': 'settings.layout.slotActivityBar',
  'desktop.sidebar': 'settings.layout.slotSidebar',
  'desktop.info_bar': 'settings.layout.slotInfoBar',
  'desktop.main': 'settings.layout.slotMain',
}

/** 内置项 id → i18n key（插件项用 view 自带 title）。 */
const BUILTIN_NAMES: Record<string, string> = {
  [BUILTIN_LAYOUT_ITEMS.mobileTools]: 'settings.layout.itemMobileTools',
  [BUILTIN_LAYOUT_ITEMS.mobileNewChat]: 'settings.layout.itemNewChat',
  [BUILTIN_LAYOUT_ITEMS.mobileSettings]: 'settings.layout.itemSettings',
  [BUILTIN_LAYOUT_ITEMS.desktopSessions]: 'settings.layout.itemSessions',
  [BUILTIN_LAYOUT_ITEMS.desktopFiles]: 'settings.layout.itemFiles',
  [BUILTIN_LAYOUT_ITEMS.desktopSearch]: 'settings.layout.itemSearch',
  [BUILTIN_LAYOUT_ITEMS.desktopInfo]: 'settings.layout.itemInfo',
  [BUILTIN_LAYOUT_ITEMS.desktopTasks]: 'settings.layout.itemTasks',
  [BUILTIN_LAYOUT_ITEMS.desktopTerminal]: 'settings.layout.itemTerminal',
}

export function SettingsLayout() {
  const { t } = useI18n()
  const { allItems, overrides, moveItem, moveItemTo, resetItem, resetAll } = useLayoutConfig()
  const [changed, setChanged] = useState(0) // force re-render after moves
  const [dragOverId, setDragOverId] = useState<string | null>(null)
  // 重置面板布局：请求进行中 / 失败提示。
  const [resetting, setResetting] = useState(false)
  const [resetErr, setResetErr] = useState<string | null>(null)

  const slots = useMemo(() => Object.keys(SLOT_LABELS) as LayoutSlotId[], [])

  // 重置面板布局：清 localStorage（v2 + 旧 v1 key）+ 服务端 user_settings，再
  // reload 回到默认停靠状态。userSettings.ts 只有 debounced 同步写（500ms 后才
  // 发请求，点击后立即 reload 会丢写）且无删除 API —— 直接 postAPI 写 '{}'
  // 覆盖。v1 key 对应 SETTING_MAP 的 web:ui:panel-layout；v2 key 预写（引擎路
  // 迁移到 v2 后服务端已有默认值，多余 key 对后端无害——任意 KV 均接受）。
  const resetPanelLayout = async () => {
    setResetting(true)
    setResetErr(null)
    try {
      localStorage.removeItem('xbot:panel-layout-v2')
      localStorage.removeItem('xbot:panel-layout')
    } catch { /* ignore */ }
    try {
      await postAPI('/api/settings', { settings: {
        'web:ui:panel-layout': '{}',
        'web:ui:panel-layout-v2': '{}',
      } })
    } catch (err) {
      setResetting(false)
      setResetErr(t('settings.layout.resetSyncFailed'))
      console.warn('[SettingsLayout] reset panel layout server sync failed:', err)
      return
    }
    window.location.reload()
  }

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

  const itemName = (id: string, title: string) => (BUILTIN_NAMES[id] ? t(BUILTIN_NAMES[id]) : title)

  return (
    <div className="flex flex-col gap-2.5 p-4">
      <SettingsSection
        title={t('settings.nav.layout')}
        description={t('settings.layout.desc')}
      >
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => { resetAll(); setChanged((v) => v + 1) }}>
            {t('settings.layout.resetAll')}
          </Button>
          <Button
            type="button"
            size="sm"
            disabled={resetting}
            title={t('settings.layout.resetPanelTitle')}
            onClick={() => { void resetPanelLayout() }}
          >
            {t('settings.layout.resetPanel')}
          </Button>
          {resetErr && <span className="text-xs text-red-400">{resetErr}</span>}
        </div>
      </SettingsSection>

      {grouped.map(([slot, items]) => (
        <SettingsSection key={slot} title={t(SLOT_LABELS[slot] ?? slot)} description={t('settings.layout.itemCount', { count: items.length })}>
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
                  className={`flex items-center justify-between gap-2.5 rounded-lg border border-border bg-bg-secondary px-3 py-2 transition-colors hover:bg-bg-tertiary ${
                    dragOverId === item.id ? 'ring-2 ring-accent' : ''
                  }`}
                >
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium text-text-primary">{itemName(item.id, item.title)}</div>
                    <div className="truncate font-mono text-[10px] text-text-muted">{item.id}</div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5">
                    <select
                      value={eff}
                      onChange={(e) => { moveItem(item.id, e.target.value as LayoutSlotId); setChanged((v) => v + 1) }}
                      className="rounded-lg border border-border bg-bg-secondary px-2 py-1 text-xs text-text-primary focus:border-accent/40 focus:outline-none"
                    >
                      {slots.map((s) => (
                        <option key={s} value={s}>{t(SLOT_LABELS[s])}</option>
                      ))}
                    </select>
                    {overrides[item.id] !== undefined && (
                      <Button type="button" variant="ghost" size="sm" className="hover:bg-bg-hover" onClick={() => { resetItem(item.id); setChanged((v) => v + 1) }}>
                        {t('settings.layout.resetItem')}
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
