import type { WebToolProgress } from '@/types/shared'

/**
 * isGenUITool — metadata-driven GenUI detection.
 *
 * A tool renders via the GenUI runtime when it declares ui.mode === 'genui'
 * (populated from the tool's UIDecl on the backend). The legacy `display_html`
 * name check is kept ONLY for committed/history messages persisted before the
 * metadata existed — it must never be used as the primary criterion.
 *
 * See docs/agent/genui-plugin-design.md §9.
 */
export function isGenUITool(tool: WebToolProgress | { name?: string; uiMode?: string } | null | undefined): boolean {
  if (!tool) return false
  if (tool.uiMode === 'genui') return true
  // Legacy fallback for pre-metadata history rows (transition period only).
  if (tool.name === 'display_html') return true
  return false
}

/** Extract the GenUI source code from a tool (args.code preferred, Detail fallback). */
export function genUICode(tool: WebToolProgress | { detail?: string; args?: string } | null | undefined): string {
  if (!tool) return ''
  if (tool.detail) return tool.detail
  return ''
}
