/**
 * SettingsSection — generic settings-item layout (Spec 7 §3.7).
 *
 * Renders a title, an optional description, then the setting control(s).
 * A top border separates consecutive sections inside a settings category so
 * the panel reads as a clean vertical list of options (VSCode-style).
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
      className="flex flex-col gap-3 border-t border-border px-5 py-4 first:border-t-0"
    >
      <div className="flex flex-col gap-1">
        <h3 id={titleId} className="text-sm font-medium text-foreground">
          {title}
        </h3>
        {description ? (
          <p className="text-xs text-muted-foreground">{description}</p>
        ) : null}
      </div>
      <div className="flex flex-col gap-2">{children}</div>
    </section>
  )
}
