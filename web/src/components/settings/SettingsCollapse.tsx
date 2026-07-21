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
import { useCollapseLevel, useMergeTools } from '@/hooks/useCollapseLevel'
import { useSendKeyMode } from '@/hooks/useSendKeyMode'
import { useI18n } from '@/providers/i18n'
import type { CollapseLevel } from '@/types/shared'
import type { SendKeyMode } from '@/types/agent'
import { cn } from '@/lib/utils'

import { SettingsSection } from './SettingsSection'

const LEVELS: { value: CollapseLevel; labelKey: string; descKey: string }[] = [
  { value: 'all', labelKey: 'collapseAll', descKey: 'collapseAllDesc' },
  { value: 'minimal', labelKey: 'collapseMinimal', descKey: 'collapseMinimalDesc' },
  { value: 'none', labelKey: 'collapseNone', descKey: 'collapseNoneDesc' },
]

const SEND_KEY_OPTIONS: { value: SendKeyMode; labelKey: string; descKey: string }[] = [
  { value: 'ctrl-enter', labelKey: 'sendKeyCtrlEnter', descKey: 'sendKeyCtrlEnterDesc' },
  { value: 'enter', labelKey: 'sendKeyEnter', descKey: 'sendKeyEnterDesc' },
]

export function SettingsCollapse() {
  const { t } = useI18n()
  const { level: collapseLevel, setLevel: setCollapseLevel } = useCollapseLevel()
  const { mergeTools, setMergeTools } = useMergeTools()
  const { mode: sendKeyMode, setMode: setSendKeyMode } = useSendKeyMode()

  return (
    <div className="flex flex-col">
      <SettingsSection
        title={t('settings.collapseLevel')}
        description={t('settings.collapseLevelDesc')}
      >
        <div className="flex flex-col gap-1.5">
          {LEVELS.map(({ value, labelKey, descKey }) => {
            const active = collapseLevel === value
            return (
              <button
                key={value}
                type="button"
                aria-pressed={active}
                onClick={() => setCollapseLevel(value)}
                className={cn(
                  'flex items-start gap-3 rounded-md border px-3 py-2.5 text-left transition-colors',
                  active
                    ? 'border-accent bg-accent/10'
                    : 'border-border bg-transparent hover:bg-muted',
                )}
              >
                <span
                  className={cn(
                    'mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border',
                    active ? 'border-accent' : 'border-border',
                  )}
                >
                  {active ? <span className="size-2 rounded-full bg-accent" /> : null}
                </span>
                <span className="flex flex-col gap-0.5">
                  <span className="text-sm font-medium text-foreground">
                    {t(`settings.${labelKey}`)}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {t(`settings.${descKey}`)}
                  </span>
                </span>
              </button>
            )
          })}
        </div>
      </SettingsSection>

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
            'flex items-center gap-3 rounded-md border px-3 py-2.5 text-left transition-colors',
            mergeTools
              ? 'border-accent bg-accent/10'
              : 'border-border bg-transparent hover:bg-muted',
          )}
        >
          <span
            className={cn(
              'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors',
              mergeTools ? 'bg-accent' : 'bg-border',
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
            <span className="text-sm font-medium text-foreground">
              {mergeTools ? t('settings.mergeToolsOn') : t('settings.mergeToolsOff')}
            </span>
          </span>
        </button>
      </SettingsSection>

      {/* Send Key Mode */}
      <SettingsSection
        title={t('settings.sendKeyMode')}
        description={t('settings.sendKeyModeDesc')}
      >
        <div className="flex flex-col gap-1.5">
          {SEND_KEY_OPTIONS.map(({ value, labelKey, descKey }) => {
            const active = sendKeyMode === value
            return (
              <button
                key={value}
                type="button"
                aria-pressed={active}
                onClick={() => setSendKeyMode(value)}
                className={cn(
                  'flex items-start gap-3 rounded-md border px-3 py-2.5 text-left transition-colors',
                  active
                    ? 'border-accent bg-accent/10'
                    : 'border-border bg-transparent hover:bg-muted',
                )}
              >
                <span
                  className={cn(
                    'mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border',
                    active ? 'border-accent' : 'border-border',
                  )}
                >
                  {active ? <span className="size-2 rounded-full bg-accent" /> : null}
                </span>
                <span className="flex flex-col gap-0.5">
                  <span className="text-sm font-medium text-foreground">
                    {t(`settings.${labelKey}`)}
                  </span>
                  <span className="text-xs text-muted-foreground">
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
