/**
 * ToolGroupCard — legacy re-export of FoldedToolGroup (Spec 4 refactor).
 *
 * The canonical component is now FoldedToolGroup.tsx. This file keeps the old
 * import path working during the migration.
 */
export { FoldedToolGroup as ToolGroupCard } from './FoldedToolGroup'

/** Duration formatting helper — kept for backward compat. */
export function formatDuration(ms: number): string {
  if (!ms || ms <= 0) return '0s'
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  const rem = Math.round(s % 60)
  return `${m}m ${rem}s`
}
