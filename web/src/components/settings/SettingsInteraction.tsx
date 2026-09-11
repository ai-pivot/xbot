/**
 * SettingsInteraction — interaction preferences.
 *
 * Send-key mode and code word-wrap. (The former collapse-level / merge-tools
 * preferences were removed: iterations are now always rendered individually.)
 */
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
  const { mode: sendKeyMode, setMode: setSendKeyMode } = useSendKeyMode()
  const { wordWrap, setWordWrap } = useCodeWordWrap()

  return (
    <div className="flex flex-col gap-2.5 p-4">
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
              ? 'border-accent/40 bg-accent/14'
              : 'border-border bg-bg-secondary hover:bg-bg-tertiary',
          )}
        >
          <span
            className={cn(
              'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors',
              wordWrap ? 'bg-accent' : 'bg-bg-hover',
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
                    ? 'border-accent/40 bg-accent/14'
                    : 'border-border bg-bg-secondary hover:bg-bg-tertiary',
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
