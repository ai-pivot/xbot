/**
 * SettingsCollapse — Agent intermediate-step collapse preference (Spec 7 §3.4).
 *
 * Three levels: 'all' (final output only), 'minimal' (tool name + summary,
 * details collapsed), 'none' (expand everything). Persisted by useCollapseLevel
 * to localStorage 'xbot-collapse-level' and broadcast app-wide so the Agent
 * workspace (Spec 4) can apply it live.
 */
import { useCollapseLevel } from '@/hooks/useCollapseLevel'
import { useI18n } from '@/providers/i18n'
import type { CollapseLevel } from '@/types/shared'
import { cn } from '@/lib/utils'

import { SettingsSection } from './SettingsSection'

const LEVELS: { value: CollapseLevel; labelKey: string; descKey: string }[] = [
  { value: 'all', labelKey: 'collapseAll', descKey: 'collapseAllDesc' },
  { value: 'minimal', labelKey: 'collapseMinimal', descKey: 'collapseMinimalDesc' },
  { value: 'none', labelKey: 'collapseNone', descKey: 'collapseNoneDesc' },
]

export function SettingsCollapse() {
  const { t } = useI18n()
  const { level: collapseLevel, setLevel: setCollapseLevel } = useCollapseLevel()

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
    </div>
  )
}
