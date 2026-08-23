/**
 * FrontmatterCard — renders the YAML frontmatter of a markdown file (SKILL.md
 * etc.) as a compact metadata card instead of letting CommonMark parse it as a
 * heading. Styled with the same --md-* variables as .md-body so it follows
 * [data-md-theme] overrides.
 */
import { memo } from 'react'

export interface FrontmatterCardProps {
  /** Parsed frontmatter key/value pairs. */
  values: Record<string, string>
}

/** Keys shown as labeled rows (name/description get a dedicated layout). */
const LABELED_KEYS = ['name', 'description']

export const FrontmatterCard = memo(function FrontmatterCard({ values }: FrontmatterCardProps) {
  const name = values.name
  const description = values.description
  const rest = Object.entries(values).filter(([k]) => !LABELED_KEYS.includes(k))

  return (
    <div data-testid="frontmatter-card" className="md-frontmatter-card">
      {name && <div className="md-frontmatter-name">{name}</div>}
      {description && <div className="md-frontmatter-desc">{description}</div>}
      {rest.length > 0 && (
        <dl className="md-frontmatter-fields">
          {rest.map(([k, v]) => (
            <div className="md-frontmatter-field" key={k}>
              <dt>{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  )
})
