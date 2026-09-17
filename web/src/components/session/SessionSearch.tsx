/**
 * SessionSearch — 会话过滤框 + 展开它的开关按钮（Spec 3 §3.7）。
 *
 * 搜索默认收起（会话搜索是低频操作）：点击与「新建会话」同排的
 * `SessionSearchToggle` 展开——展开时【横向挤压】同排的新建会话按钮
 * （收缩为图标大小），搜索框在同一行内展开，**不纵向撑开列表**。
 *
 * 组件始终挂载（收起时由父容器的 0 宽裁切 + `aria-hidden` 隐藏），这样
 * 展开/收起都能走 CSS 动画；收起态不可聚焦、不进入可达性树。
 *
 * 纯前端过滤：匹配 label 或 preview，大小写不敏感。有查询时列表忽略分类分组，
 * 显示扁平排序结果；清除查询恢复分组视图。
 */
import { Search, X } from 'lucide-react'
import { useEffect, useRef, type RefObject } from 'react'

import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'

interface SessionSearchProps {
  value: string
  onChange: (v: string) => void
  /** 展开态：展开时自动聚焦；收起时不可聚焦（仍挂载以播放动画）。 */
  open: boolean
  /** Esc 关闭（父组件收起并清空查询）。 */
  onClose?: () => void
  /**
   * 外部持有的输入框 ref。父组件在**开关按钮的点击手势内**同步
   * `focus()`（`toggleSearch`）——手机浏览器只在用户手势的同一次任务里
   * 打开软键盘，`useEffect` 里的异步 focus 拿不到键盘。
   */
  inputRef?: RefObject<HTMLInputElement | null>
  className?: string
}

export function SessionSearch({ value, onChange, open, onClose, inputRef, className }: SessionSearchProps) {
  const { t } = useI18n()
  const innerRef = useRef<HTMLInputElement | null>(null)
  const ref = inputRef ?? innerRef

  // 展开即聚焦：点击按钮后可直接输入（桌面即时；手机另由父组件在手势内同步 focus）。
  useEffect(() => {
    if (open) ref.current?.focus()
  }, [open, ref])

  return (
    <div
      className={cn('flex h-full min-w-0 flex-1 items-center gap-2 rounded-lg border px-3', className)}
      style={{ borderColor: 'var(--border)', background: 'var(--bg-primary)' }}
    >
      <Search className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
      {/* ⚠️ 绝不使用 readOnly-until-focus 反自动填充：readOnly 的输入框在手机上
          **永远不会弹出软键盘**（用户 2026-09-15：「session 面板那个搜索，不要
          disable 弹出键盘，这导致手机端都不会弹出键盘」）。反自动填充改用不阻塞
          键盘的手段：唯一的非凭据 name + autoComplete="off" + data-form-type。 */}
      <input
        ref={ref}
        type="text"
        inputMode="search"
        name="xbot-session-search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.preventDefault()
            onClose?.()
          }
        }}
        placeholder={t('session.searchPlaceholder')}
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="off"
        spellCheck={false}
        enterKeyHint="search"
        data-form-type="other"
        tabIndex={open ? undefined : -1}
        className="h-6 min-w-0 flex-1 bg-transparent text-xs outline-none placeholder:text-text-muted"
        style={{ color: 'var(--text-primary)' }}
        aria-label={t('common.search')}
      />
      {value && (
        <button
          type="button"
          onClick={() => onChange('')}
          aria-label={t('common.close')}
          className="shrink-0 rounded p-0.5 hover:bg-bg-tertiary"
          style={{ color: 'var(--text-muted)' }}
        >
          <X className="size-3.5" />
        </button>
      )}
    </div>
  )
}

interface SessionSearchToggleProps {
  open: boolean
  onToggle: () => void
  className?: string
}

/**
 * 搜索开关按钮 —— 与「新建会话」同一行（`items-stretch` 让两者等高）。
 * `aria-expanded` 表达展开态；`session.searchToggle` 与输入框的 `common.search`
 * 区分开，避免同页出现两个同名可访问元素。
 */
export function SessionSearchToggle({ open, onToggle, className }: SessionSearchToggleProps) {
  const { t } = useI18n()
  return (
    <button
      type="button"
      data-testid="session-search-toggle"
      onClick={onToggle}
      aria-expanded={open}
      aria-label={t('session.searchToggle')}
      title={t('session.searchToggle')}
      className={cn(
        'flex w-8 shrink-0 items-center justify-center rounded-lg border transition-colors',
        open
          ? 'border-accent/40 bg-bg-tertiary text-accent'
          : 'border-border bg-bg-secondary text-text-muted hover:bg-bg-tertiary hover:text-text-primary',
        className,
      )}
    >
      <Search className="size-3.5" />
    </button>
  )
}
