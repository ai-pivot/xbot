/**
 * Tolerate strong/strikethrough emphasis that CommonMark refuses in Chinese
 * prose, where the delimiter ends up glued to CJK text on the outside and to
 * punctuation (or a space) on the inside:
 *
 *   `reserve_sidecar_trace` 是** “每个 tick 最多把 N 个 …” 的原子配额领取器**——…
 *   见**（1）**、**“引用”**的说明
 *
 * Why those stay literal: a `**` run is only a valid opener when it is
 * *left-flanking* — not followed by whitespace, and (when followed by
 * punctuation) preceded by whitespace or punctuation. `是**“…”` is followed by
 * `“` and preceded by the letter `是`, so it opens nothing; the mirror case
 * (`**“…”**的`) fails the closing side for the same reason. GitHub renders both
 * literally (verified with `POST /markdown`, `mode: gfm`), so this is the
 * documented GFM behaviour, not a parser bug — the chat renderer has to decide
 * to be forgiving, the same way `protectBareURLs` handles greedy autolinks.
 *
 * The repair runs on the parsed tree: an unpaired `**…**` inside Chinese prose
 * is a single text node (the parser rejected the emphasis, so no `strong` node
 * exists), which lets us rebuild it as `text + strong + text` **without
 * changing a single character** — unlike rewriting the source, which would have
 * to insert spaces around the delimiters to satisfy the flanking rules.
 */

/** Minimal mdast shape: only what this plugin reads/writes. */
interface MdNode {
  type: string
  value?: string
  children?: MdNode[]
}

/** Han, kana, hangul — the "words" that hug a delimiter in CJK prose. */
const CJK_TEXT = /[\u3040-\u30ff\u3400-\u9fff\uac00-\ud7af]/

/** Delimiter runs we repair; single `*`/`_` emphasis stays with CommonMark. */
const DELIMITERS: ReadonlyArray<{ marker: string; node: "strong" | "delete" }> = [
  { marker: "**", node: "strong" },
  { marker: "__", node: "strong" },
  { marker: "~~", node: "delete" },
]

/** Inline syntax inside the candidate span that would need a real re-parse
 * (code, links, nested emphasis, escapes). Those spans are left untouched. */
const UNSUPPORTED_INNER = /[`[\]\\*_~]/

/** Cheap pre-filter: only text nodes carrying a delimiter run are worth scanning. */
const DELIMITER_RUN = /\*\*|__|~~/

interface Span {
  /** Offset of the opening marker. */
  start: number
  /** Offset of the closing marker. */
  end: number
  marker: string
  node: "strong" | "delete"
}

/**
 * Earliest span in `text` that CJK prose clearly meant as emphasis: a balanced
 * delimiter pair whose outer neighbour is CJK text, with no inline syntax inside.
 */
function findSpan(text: string): Span | null {
  let best: Span | null = null
  for (const { marker, node } of DELIMITERS) {
    let start = text.indexOf(marker)
    while (start >= 0) {
      const end = text.indexOf(marker, start + marker.length)
      if (end < 0) break
      const inner = text.slice(start + marker.length, end)
      const previous = text[start - 1] ?? ""
      const next = text[end + marker.length] ?? ""
      const candidate: Span = { start, end, marker, node }
      const usable =
        inner.trim() !== "" &&
        !UNSUPPORTED_INNER.test(inner) &&
        (CJK_TEXT.test(previous) || CJK_TEXT.test(next)) &&
        (best === null || start < best.start)
      if (usable) {
        best = candidate
        break
      }
      start = text.indexOf(marker, start + 1)
    }
  }
  return best
}

/** Split a text node into text / emphasis / text runs. */
function repairText(value: string): MdNode[] {
  const out: MdNode[] = []
  let rest = value
  for (let span = findSpan(rest); span; span = findSpan(rest)) {
    const inner = rest.slice(span.start + span.marker.length, span.end)
    // The whitespace the author typed inside the run moves outside it, so the
    // rendered characters stay identical while the emphasis starts on content.
    const leading = /^\s*/.exec(inner)?.[0] ?? ""
    const core = inner.trim()
    const trailing = inner.slice(leading.length + core.length)
    const before = rest.slice(0, span.start) + leading
    if (before) out.push({ type: "text", value: before })
    out.push({ type: span.node, children: [{ type: "text", value: core }] })
    rest = trailing + rest.slice(span.end + span.marker.length)
  }
  if (rest) out.push({ type: "text", value: rest })
  return out
}

/** Walk the tree, rewriting text nodes that carry a repairable emphasis span. */
function repairNode(node: MdNode): void {
  if (!node.children) return
  const children: MdNode[] = []
  for (const child of node.children) {
    if (child.type === "text" && child.value && DELIMITER_RUN.test(child.value)) {
      children.push(...repairText(child.value))
      continue
    }
    repairNode(child)
    children.push(child)
  }
  node.children = children
}

/**
 * remark plugin: rebuild `**…**` / `__…__` / `~~…~~` runs that CommonMark
 * rejected only because they touch CJK text (see the file header).
 */
export function remarkCjkEmphasis() {
  return (tree: unknown): void => {
    repairNode(tree as MdNode)
  }
}
