/**
 * Build-time helper (also imported by vite.config.ts): derive the single-label
 * wildcard that covers every Vercel deployment of this project, so the preview
 * gate can tell the operator exactly what to put in `web.preview_origins`.
 *
 * Vercel exposes the deployment host (`VERCEL_URL`, `<project>-<hash>-<scope>`)
 * and the branch alias (`VERCEL_BRANCH_URL`, `<project>-git-<branch>-<scope>`).
 * Their common prefix/suffix, cut back to dash boundaries, are the project and
 * scope; the differing middle becomes `*`. The result follows the backend
 * pattern rules (HTTPS, one `*` in the leftmost label, literal text on both
 * sides). Anything unexpected yields '' and the gate falls back to the exact
 * page origin.
 */
const LABEL = /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/
const VERCEL_SUFFIX = '.vercel.app'

function vercelLabel(host: string | undefined): string {
  if (!host || !host.endsWith(VERCEL_SUFFIX)) return ''
  const label = host.slice(0, -VERCEL_SUFFIX.length)
  return LABEL.test(label) ? label : ''
}

export function deriveVercelOriginPattern(deploymentHost?: string, branchHost?: string): string {
  const a = vercelLabel(deploymentHost)
  const b = vercelLabel(branchHost)
  if (!a || !b || a === b) return ''
  let p = 0
  while (p < a.length && p < b.length && a[p] === b[p]) p++
  let s = 0
  while (s < a.length - p && s < b.length - p && a[a.length - 1 - s] === b[b.length - 1 - s]) s++
  const prefix = a.slice(0, a.lastIndexOf('-', p - 1) + 1)
  const commonSuffix = a.slice(a.length - s)
  const dash = commonSuffix.indexOf('-')
  const suffix = dash < 0 ? '' : commonSuffix.slice(dash)
  if (prefix.length < 2 || suffix.length < 2) return ''
  if (a.length <= prefix.length + suffix.length || b.length <= prefix.length + suffix.length) return ''
  return `https://${prefix}*${suffix}${VERCEL_SUFFIX}`
}

/** The `config.json` fragment shown (and copied) by the preview gate. */
export function previewOriginsSnippet(origin: string): string {
  return `"web": {\n  "preview_origins": [\n    ${JSON.stringify(origin)}\n  ]\n}`
}
