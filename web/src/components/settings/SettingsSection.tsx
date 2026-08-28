/**
 * SettingsSection — generic settings-item layout (布局 v2 视觉).
 *
 * Renders a group title, an optional description, then the setting control(s).
 * Each section is a rounded-xl card (border white/[.06] + bg white/[.02])
 * with a 10px uppercase tracking group title — the unified "new visual"
 * language shared across the settings center.
 */
import type { ReactNode } from 'react'
import { useId } from 'react'

interface SettingsSectionProps {
  /** Section heading (i18n-translated). */
  title: string
  /** Secondary line explaining the option. */
  description?: string
  /** The setting control(s): switch, select, color picker, ... */
  children: ReactNode
}

export function SettingsSection({ title, description, children }: SettingsSectionProps) {
  // Generate a stable-ish label id so controls can associate aria-labelledby.
  const reactId = useId()
  const titleId = `settings-section-${reactId}`

  return (
    <section
      aria-labelledby={titleId}
      className="flex flex-col gap-2.5 rounded-xl border border-white/[.06] bg-white/[.02] px-4 py-4"
    >
      <div className="flex flex-col gap-1">
        <h3 id={titleId} className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">
          {title}
        </h3>
        {description ? (
          <p className="text-xs text-text-muted">{description}</p>
        ) : null}
      </div>
      <div className="flex flex-col gap-2.5">{children}</div>
    </section>
  )
}
