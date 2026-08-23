/**
 * Markdown YAML frontmatter utilities.
 *
 * SKILL.md (and other md files) often start with a `---`-delimited YAML block
 * (name / description / ...). CommonMark parses a `---` line that follows text
 * as a setext heading underline — so the raw frontmatter would render as an
 * <h2> swallowing the YAML lines. These helpers parse the block out and return
 * the body, plus a typed view of the key/value pairs for UI (e.g. a
 * FrontmatterCard in MarkdownPreview).
 *
 * Only a block that begins the document is treated as frontmatter; a `---`
 * mid-document (horizontal rule) or an unterminated leading `---` is left
 * untouched.
 */

export interface FrontmatterInfo {
  /** True when the source began with a `---`-delimited YAML block. */
  hasFrontmatter: boolean
  /** Parsed key/value pairs (string values, quotes stripped). */
  values: Record<string, string>
  /** The markdown body with the frontmatter block removed. */
  body: string
}

const FRONTMATTER_RE = /^---\r?\n([\s\S]*?)(?:\r?\n)?---(?:\r?\n|$)/

/** Strip matching surrounding quotes from a YAML scalar. */
function unquote(value: string): string {
  const t = value.trim()
  if (t.length >= 2) {
    if ((t.startsWith('"') && t.endsWith('"')) || (t.startsWith("'") && t.endsWith("'"))) {
      return t.slice(1, -1)
    }
  }
  return t
}

/** Strip a trailing `# comment` only when the value is unquoted. */
function stripComment(value: string): string {
  const t = value.trim()
  if (t.startsWith('"') || t.startsWith("'")) return t
  const hash = t.indexOf(' #')
  return hash >= 0 ? t.slice(0, hash).trim() : t
}

/**
 * Parse a leading YAML frontmatter block out of markdown source.
 * Returns the parsed values plus the body without the block.
 */
export function parseFrontmatter(source: string): FrontmatterInfo {
  const m = FRONTMATTER_RE.exec(source)
  if (!m) {
    return { hasFrontmatter: false, values: {}, body: source }
  }
  const values: Record<string, string> = {}
  for (const rawLine of m[1].split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    const colon = line.indexOf(':')
    if (colon <= 0) continue
    const key = line.slice(0, colon).trim()
    if (!key) continue
    values[key] = unquote(stripComment(line.slice(colon + 1)))
  }
  return {
    hasFrontmatter: true,
    values,
    body: source.slice(m[0].length).replace(/^\r?\n/, ''),
  }
}

/** Remove a leading YAML frontmatter block, returning the markdown body. */
export function stripFrontmatter(source: string): string {
  return parseFrontmatter(source).body
}
