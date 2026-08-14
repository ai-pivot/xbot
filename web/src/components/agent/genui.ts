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
  if (tool.detail) return stripGenUIPrefix(tool.detail)
  return ''
}

/**
 * Strip the Summary prefix that legacy (pre-plugin-fix) display_html tools
 * stored in Detail: "🎨 UI rendered (5929 chars)\nexport default function App() {...}".
 * The prefix makes sucrase compilation fail → blank iframe. New backend writes
 * pure TSX to Detail; this normalizes BOTH old history rows and any future
 * prefix leakage so the committed render always compiles.
 */
export function stripGenUIPrefix(code: string): string {
  if (!code) return ''
  // Match a leading line like "🎨 UI rendered (5929 chars)" (any summary text
  // before the first code marker). We only strip when the remainder looks like
  // a TSX module (contains "export default" / "function App" / "=>" / JSX).
  const lines = code.split('\n')
  if (lines.length > 1) {
    const first = lines[0].trim()
    // Legacy Summary prefix patterns: "🎨 UI rendered (N chars)" or any
    // non-code header line followed by a TSX module.
    if (!/^(import|export|const|function|class|return|\/\/|\/\*|#|\{|\()/.test(first) && /export default|function App|<[A-Z]/.test(code)) {
      return lines.slice(1).join('\n').trimStart()
    }
  }
  return code
}
