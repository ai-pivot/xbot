/**
 * SettingsCollapse — Agent intermediate-step collapse preference (Spec A §4).
 *
 * Three levels: 'all' (final output only), 'minimal' (tool name + summary,
 * details collapsed), 'none' (expand everything). Persisted by useCollapseLevel
 * to localStorage 'xbot-collapse-level' and broadcast app-wide via
 * useSyncExternalStore so every component instance updates immediately.
 *
 * Also includes a `mergeTools` toggle (Spec A §3.1) — orthogonal to the
 * collapse level, controls whether consecutive tool calls are merged into
 * a compact row.
 */
import { useMergeTools } from '@/hooks/useCollapseLevel'
import { useSendKeyMode } from '@/hooks/useSendKeyMode'
import { useCodeWordWrap } from '@/hooks/useCodeWordWrap'
import { useI18n } from '@/providers/i18n'
import type { SendKeyMode } from '@/types/agent'
import { cn } from '@/lib/utils'

import { SettingsSection } from './SettingsSection'

const SEND_KEY_OPTIONS: { value: SendKeyMode; labelKey: string; descKey: string }[] = [
  { value: 'ctrl-enter', labelKey: 'sendKeyCtrlEnter', descKey: 'sendKeyCtrlEnterDesc' },
  { value: 'enter', labelKey: 'sendKeyEnter', descKey: 'sendKeyEnterDesc' },
]

export function SettingsInteraction() {
  const { t } = useI18n()
  const { mergeTools, setMergeTools } = useMergeTools()
  const { mode: sendKeyMode, setMode: setSendKeyMode } = useSendKeyMode()
  const { wordWrap, setWordWrap } = useCodeWordWrap()

  return (
    <div className="flex flex-col gap-2.5 p-4">
      {/* 折叠级别（Collapse Level）选项已按布局 v2 设计整体删除——
          历史渲染固定为默认折叠形态（useCollapseLevel 返回默认值，消费方零改动）。 */}

      {/* Merge Tools Toggle */}
      <SettingsSection
        title={t('settings.mergeTools')}
        description={t('settings.mergeToolsDesc')}
      >
        <button
          type="button"
          aria-pressed={mergeTools}
          onClick={() => setMergeTools(!mergeTools)}
          className={cn(
            'flex items-center gap-3 rounded-lg border px-3 py-2 text-left transition-colors',
            mergeTools
              ? 'border-[#6c8cff]/40 bg-[#6c8cff]/14'
              : 'border-border bg-sidebar-bg hover:bg-surface-bg',
          )}
        >
          <span
            className={cn(
              'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors',
              mergeTools ? 'bg-[#6c8cff]' : 'bg-surface-bg',
            )}
          >
            <span
              className={cn(
                'inline-block size-4 transform rounded-full bg-white transition-transform',
                mergeTools ? 'translate-x-4' : 'translate-x-1',
              )}
            />
          </span>
          <span className="flex flex-col gap-0.5">
            <span className="text-sm font-medium text-text-primary">
              {mergeTools ? t('settings.mergeToolsOn') : t('settings.mergeToolsOff')}
            </span>
          </span>
        </button>
      </SettingsSection>

      {/* Code Word Wrap Toggle */}
      <SettingsSection
        title={t('settings.codeWordWrap')}
        description={t('settings.codeWordWrapDesc')}
      >
        <button
          type="button"
          aria-pressed={wordWrap}
          onClick={() => setWordWrap(!wordWrap)}
          className={cn(
            'flex items-center gap-3 rounded-lg border px-3 py-2 text-left transition-colors',
            wordWrap
              ? 'border-[#6c8cff]/40 bg-[#6c8cff]/14'
              : 'border-border bg-sidebar-bg hover:bg-surface-bg',
          )}
        >
          <span
            className={cn(
              'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors',
              wordWrap ? 'bg-[#6c8cff]' : 'bg-surface-bg',
            )}
          >
            <span
              className={cn(
                'inline-block size-4 transform rounded-full bg-white transition-transform',
                wordWrap ? 'translate-x-4' : 'translate-x-1',
              )}
            />
          </span>
          <span className="flex flex-col gap-0.5">
            <span className="text-sm font-medium text-text-primary">
              {wordWrap ? t('settings.codeWordWrapOn') : t('settings.codeWordWrapOff')}
            </span>
          </span>
        </button>
      </SettingsSection>

      {/* Send Key Mode */}
      <SettingsSection
        title={t('settings.sendKeyMode')}
        description={t('settings.sendKeyModeDesc')}
      >
        <div className="flex flex-col gap-2.5">
          {SEND_KEY_OPTIONS.map(({ value, labelKey, descKey }) => {
            const active = sendKeyMode === value
            return (
              <button
                key={value}
                type="button"
                aria-pressed={active}
                onClick={() => setSendKeyMode(value)}
                className={cn(
                  'flex items-start gap-3 rounded-lg border px-3 py-2 text-left transition-colors',
                  active
                    ? 'border-[#6c8cff]/40 bg-[#6c8cff]/14'
                    : 'border-border bg-sidebar-bg hover:bg-surface-bg',
                )}
              >
                <span
                  className={cn(
                    'mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border',
                    active ? 'border-[#6c8cff]' : 'border-border',
                  )}
                >
                  {active ? <span className="size-2 rounded-full bg-[#6c8cff]" /> : null}
                </span>
                <span className="flex flex-col gap-0.5">
                  <span className="text-sm font-medium text-text-primary">
                    {t(`settings.${labelKey}`)}
                  </span>
                  <span className="text-xs text-text-muted">
                    {t(`settings.${descKey}`)}
                  </span>
                </span>
              </button>
            )
          })}
        </div>
      </SettingsSection>
    </div>
  )
}
