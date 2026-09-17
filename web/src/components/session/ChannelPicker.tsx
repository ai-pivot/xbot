/**
 * ChannelPicker — 渠道（channel）筛选下拉：**桌面会话面板左栏** 与 **手机抽屉**
 * 共用的唯一实现。
 *
 * ⚠️ 为什么必须是单一实现：`9d7e99fe`（#326）把 `SessionSidebar` 从 `AppShell`
 * 移出、桌面左栏改为 `PanelDock` → `core.sessions`（`CoreSessionsPanel`）后，
 * 渠道选择器只留在 `SessionSidebar` 里（而它随后只被 `MobileAppShell` 渲染）
 * ⇒ **桌面会话面板再也选不了渠道**：面板只剩「按 `store.activeChannel` 过滤」
 * 的逻辑，却没有任何入口去设置它（用户 2026-09-15 报告：「sessions 侧边栏哪有
 * 渠道选择功能？」）。把下拉抽到这里，两端都渲染同一个组件，结构上不可能再漂移。
 *
 * 渠道集合由**会话列表**推导（`store.sessions` 的 channel + parentChannel，
 * 排除内部 `agent`），按 `ALL_CHANNEL_ORDER` 排序、未知渠道排末尾。
 */
import { useMemo, useState } from 'react'
import {
  Bot,
  ChevronDown,
  Globe,
  LayoutGrid,
  MessageCircle,
  MessageSquare,
  Send,
  Server,
  Terminal,
} from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'

import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { useSessionStore } from '@/hooks/useSessionStore'
import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import type { SessionInfo } from '@/types/shared'

type IconComponent = ComponentType<SVGProps<SVGSVGElement>>

const CHANNEL_ICONS: Record<string, IconComponent> = {
  web: Globe,
  cli: Terminal,
  feishu: MessageCircle,
  qq: MessageSquare,
  napcat: Send,
  server: Server,
  agent: Bot,
}

const ALL_CHANNEL_ORDER = ['web', 'cli', 'feishu', 'qq', 'napcat']

/**
 * 由会话列表推导可筛选的渠道（含 `parentChannel`；`agent` 是内部渠道，永不展示）。
 * 传 `sessions` 而不是让组件自己取，方便单测与复用。
 */
export function availableChannels(sessions: SessionInfo[]): string[] {
  const set = new Set<string>()
  for (const s of sessions) {
    if (s.channel && s.channel !== 'agent') set.add(s.channel)
    if (s.parentChannel && s.parentChannel !== 'agent') set.add(s.parentChannel)
  }
  return Array.from(set).sort((a, b) => {
    const ia = ALL_CHANNEL_ORDER.indexOf(a)
    const ib = ALL_CHANNEL_ORDER.indexOf(b)
    return (ia === -1 ? 999 : ia) - (ib === -1 ? 999 : ib)
  })
}

export interface ChannelPickerProps {
  /** 额外 class（面板工具条里用来控制外边距/最大宽度）。 */
  className?: string
  /** 触发器文案是否大写（手机抽屉的表头风格）。 */
  uppercase?: boolean
}

export function ChannelPicker({ className, uppercase = false }: ChannelPickerProps) {
  const { t } = useI18n()
  const store = useSessionStore()
  const [open, setOpen] = useState(false)

  const channels = useMemo(() => availableChannels(store.sessions), [store.sessions])
  const current = store.activeChannel
  const label = current ? t(`channel.${current}`) || current : t('channel.all')

  const pick = (ch: string | null) => {
    store.setActiveChannel(ch)
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          data-testid="channel-picker"
          title={label}
          aria-label={label}
          aria-expanded={open}
          className={cn(
            // 幽灵按钮：与面板标题行/工具条的极简风格一致（无边框、无底色，hover 才浮起）。
            // ⚠️ 之前做成带 border+bg 的方形按钮，用户 2026-09-15：「你觉得这好看？」——
            // 标题行里只有图标+文字，塞一个描边方块会喧宾夺主。
            'flex min-w-0 shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] font-medium text-text-secondary transition-colors',
            'hover:bg-bg-tertiary/60 hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-app-accent/50',
            uppercase && 'uppercase tracking-wide',
            className,
          )}
        >
          <span className="min-w-0 truncate">{label}</span>
          <ChevronDown className="size-3 shrink-0" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" sideOffset={4} className="w-48 p-1">
        <button
          type="button"
          data-testid="channel-option"
          data-channel="__all__"
          className={cn(
            'flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm transition-colors hover:bg-accent/10',
            !current ? 'font-medium text-accent' : 'text-text-secondary',
          )}
          onClick={() => pick(null)}
        >
          <LayoutGrid className="size-3.5 shrink-0" />
          {t('channel.all')}
        </button>
        {channels.map((ch) => {
          const Icon = CHANNEL_ICONS[ch] || Globe
          return (
            <button
              key={ch}
              type="button"
              data-testid="channel-option"
              data-channel={ch}
              className={cn(
                'flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm transition-colors hover:bg-accent/10',
                current === ch ? 'font-medium text-accent' : 'text-text-secondary',
              )}
              onClick={() => pick(ch)}
            >
              <Icon className="size-3.5 shrink-0" />
              {t(`channel.${ch}`) || ch}
            </button>
          )
        })}
      </PopoverContent>
    </Popover>
  )
}
