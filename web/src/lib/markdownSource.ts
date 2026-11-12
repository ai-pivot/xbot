/**
 * Source-level Markdown preprocessing shared by the chat renderer.
 *
 * `protectBareURLs` fixes a GFM autolink-literal failure mode that shows up in
 * real model output: the literal autolink keeps consuming characters until the
 * next whitespace, so Markdown syntax written directly against a URL is eaten by
 * the link. Reported shape (2026-11-09):
 *
 *   PR 已提: **https://github.com/org/repo/pull/92**（`branch` → `main`）
 *
 * rendered as a literal `**` plus a link whose href/label was
 * `https://…/92**（`branch`` — the closing `**`, the full-width parenthesis and
 * the inline code span were all swallowed (GitHub's own renderer does the same;
 * this is the documented GFM ambiguity, not a parser bug we can wait out).
 *
 * The fix is to make the URL end explicit with a CommonMark *hard* autolink
 * (`<https://…>`), whose end is the `>` instead of "next whitespace". Only URLs
 * that are immediately followed by a greedy delimiter are rewritten — ordinary
 * prose URLs keep remark's own (well-tested) trailing-punctuation handling.
 */

/** Fenced-code state after `line`; empty when no fence is open. */
export function codeFenceAfterLine(line: string, fence: string): string {
  const match = line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/)
  if (!match) return fence
  const [, marker, suffix] = match
  if (!fence) return marker[0] === "`" && suffix.includes("`") ? "" : marker
  return marker[0] === fence[0] && marker.length >= fence.length && /^[ \t]*$/.test(suffix) ? "" : fence
}

/** A bare http(s) URL: the same start GFM's autolink literal recognizes. */
const BARE_URL = /https?:\/\/[^\s<>]+/g

/**
 * Characters GFM keeps consuming inside a URL but that end a human-written URL:
 * emphasis/code delimiters (`*`, backtick, `__`, `~~`) and full-width (CJK)
 * punctuation, which is always prose punctuation and never part of a link.
 */
const FULL_WIDTH_PUNCTUATION = new Set([
  "（", "）", "「", "」", "『", "』", "【", "】", "《", "》", "〈", "〉",
  "“", "”", "‘", "’", "，", "。", "、", "；", "：", "！", "？", "…", "—",
])

/** Characters that never end a doubled `_`/`~` run inside a real path. */
const URL_PATH_CHAR = /[A-Za-z0-9/._~%+=@-]/

/** Offset of the first character that ends a human-written URL (`-1`: none). */
function urlTerminator(url: string): number {
  for (let index = 0; index < url.length; index++) {
    const ch = url[index]
    // Emphasis (asterisk) and inline code (backtick) delimiters are never part
    // of a URL in prose, and GFM's closing-marker trim already gives up on them.
    if (ch === "*" || ch === "`") return index
    if (FULL_WIDTH_PUNCTUATION.has(ch)) return index
    if (ch === "_" || ch === "~") {
      let run = 1
      while (url[index + run] === ch) run++
      // `foo__bar` / `~user` are ordinary URL characters; only a doubled run
      // that is itself followed by non-URL text reads as emphasis/strikethrough.
      if (run >= 2 && !URL_PATH_CHAR.test(url[index + run] ?? "")) return index
      index += run - 1
    }
  }
  return -1
}

/** ASCII punctuation GFM strips off the end of a URL; must stay outside ours too. */
const TRAILING_PUNCTUATION = new Set([".", ",", ";", ":", "!", "?", "'", '"', "]"])

/** True when `text` closes a parenthesis that was opened before the URL. */
function closesOuterParenthesis(text: string): boolean {
  let depth = 0
  for (const ch of text) {
    if (ch === "(") depth++
    else if (ch === ")" && depth > 0) depth--
    else if (ch === ")") return true
  }
  return false
}

/** Trim the punctuation GFM would have left outside the link. */
function trimTrailingPunctuation(url: string): string {
  let end = url.length
  while (end > 0) {
    const ch = url[end - 1]
    if (TRAILING_PUNCTUATION.has(ch) || (ch === ")" && closesOuterParenthesis(url.slice(0, end)))) {
      end--
      continue
    }
    break
  }
  return url.slice(0, end)
}

function protectPart(part: string, codeRanges: ReadonlyArray<readonly [number, number]>): string {
  let output = ""
  let cursor = 0
  for (const match of part.matchAll(BARE_URL)) {
    const start = match.index ?? 0
    // Inside an inline code span: the URL is literal text, not a link.
    if (codeRanges.some(([from, to]) => start >= from && start < to)) continue
    const previous = part[start - 1]
    // GFM only starts an autolink when the previous character is not an ASCII
    // letter — mirror it so we never create links GFM would not have created.
    if (previous !== undefined && /[A-Za-z]/.test(previous)) continue
    // Already a hard autolink (`<…>`), a link label (`[…`) or a link
    // destination (`](…`): rewriting those would change their meaning.
    if (previous === "<" || previous === "[") continue
    if (part.slice(Math.max(0, start - 2), start) === "](") continue

    const url = match[0]
    const stop = urlTerminator(url)
    if (stop <= 0) continue
    const head = trimTrailingPunctuation(url.slice(0, stop))
    if (!head) continue
    output += part.slice(cursor, start) + "<" + head + ">"
    cursor = start + head.length
  }
  return output + part.slice(cursor)
}

/** Ranges (UTF-16 offsets) occupied by inline code spans on a single line. */
function inlineCodeRanges(line: string): Array<readonly [number, number]> {
  const ranges: Array<readonly [number, number]> = []
  for (const match of line.matchAll(/(`+[^`]*`+)/g)) {
    const start = match.index ?? 0
    ranges.push([start, start + match[0].length])
  }
  return ranges
}

/**
 * Rewrite bare URLs that are immediately followed by Markdown-significant
 * delimiters into hard autolinks so the delimiters keep their meaning.
 * Code (fenced blocks and inline spans) is never touched.
 */
export function protectBareURLs(markdown: string): string {
  let fence = ""
  return markdown
    .split("\n")
    .map((line) => {
      const nextFence = codeFenceAfterLine(line, fence)
      if (fence || nextFence) {
        fence = nextFence
        return line
      }
      return protectPart(line, inlineCodeRanges(line))
    })
    .join("\n")
}
