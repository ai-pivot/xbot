/**
 * Unit tests for the CJK emphasis tolerance plugin.
 *
 * Incident (2026-11-10, user screenshot): a model reply contained
 *
 *   `reserve_sidecar_trace` 是** “每个 maintenance tick 最多把 N 个 sidecar delete RPC
 *   挂回本 tick 的 trace” 的原子配额领取器**——它不控制请求是否执行。
 *
 * and the `**` stayed literal (no bold). Cause: CommonMark requires a left-flanking
 * opening run — `**` glued to CJK text on the outside and to punctuation/space on
 * the inside (`是**“…”**`) is NOT left-flanking, and the mirror image is not
 * right-flanking (`**“…”**的`). GitHub's own renderer keeps them literal too
 * (verified with `POST /markdown`, mode=gfm), so this is the documented GFM
 * behaviour, not a parser bug — the chat renderer has to decide to be forgiving.
 */
import { describe, expect, it } from 'vitest'
import { unified } from 'unified'
import remarkGfm from 'remark-gfm'
import remarkParse from 'remark-parse'

import { remarkCjkEmphasis } from '@/lib/remarkCjkEmphasis'

interface MdNode {
  type: string
  value?: string
  children?: MdNode[]
}

const processor = unified().use(remarkParse).use(remarkGfm).use(remarkCjkEmphasis)

function tree(markdown: string): MdNode {
  // `runSync` is what react-markdown does (`processor.run`): parse, then run the
  // plugins' transformers — where this repair lives.
  return processor.runSync(processor.parse(markdown)) as unknown as MdNode
}

/** Flatten the paragraph into [kind, text] pairs for readable assertions. */
function segments(markdown: string): Array<[string, string]> {
  const paragraph = tree(markdown).children?.[0]
  const out: Array<[string, string]> = []
  const walk = (node: MdNode): void => {
    if (node.type === 'text') {
      out.push(['text', node.value ?? ''])
      return
    }
    if (node.type === 'strong' || node.type === 'delete') {
      out.push([node.type, (node.children ?? []).map((child) => child.value ?? '').join('')])
      return
    }
    for (const child of node.children ?? []) walk(child)
  }
  walk(paragraph!)
  return out
}

describe('remarkCjkEmphasis', () => {
  it('bolds a term glued to CJK text and opened by a space + quote', () => {
    // The space the author typed inside the run moves outside it, so the
    // rendered characters are unchanged.
    expect(segments('是** “每个 tick” 的领取器**——y')).toEqual([
      ['text', '是 '],
      ['strong', '“每个 tick” 的领取器'],
      ['text', '——y'],
    ])
  })

  it('bolds a quoted term without the space', () => {
    expect(segments('是**“每个 tick” 的领取器**——y')).toEqual([
      ['text', '是'],
      ['strong', '“每个 tick” 的领取器'],
      ['text', '——y'],
    ])
  })

  it('bolds a quoted term followed by CJK text', () => {
    expect(segments('**“x”**的领取器')).toEqual([
      ['strong', '“x”'],
      ['text', '的领取器'],
    ])
  })

  it('bolds parenthesized and colon-led Chinese emphasis', () => {
    expect(segments('中文**（括号）**后')).toEqual([
      ['text', '中文'],
      ['strong', '（括号）'],
      ['text', '后'],
    ])
    expect(segments('说**：注意**——y')).toEqual([
      ['text', '说'],
      ['strong', '：注意'],
      ['text', '——y'],
    ])
  })

  it('handles __strong__ and ~~strike~~ with the same rule', () => {
    expect(segments('是__“x”__的')).toEqual([['text', '是'], ['strong', '“x”'], ['text', '的']])
    expect(segments('是~~“x”~~的')).toEqual([['text', '是'], ['delete', '“x”'], ['text', '的']])
  })

  it('leaves ordinary emphasis alone', () => {
    expect(segments('是**加粗**文字')).toEqual([
      ['text', '是'],
      ['strong', '加粗'],
      ['text', '文字'],
    ])
  })

  it('leaves literal asterisks without CJK adjacency alone', () => {
    // `2**3**4` is parsed as emphasis by CommonMark itself — not our business.
    for (const source of ['a ** b ** c', 'a**"b"**c']) {
      expect(segments(source).filter(([kind]) => kind !== 'text')).toEqual([])
    }
  })

  it('does not touch code spans or fenced code', () => {
    expect(tree('`**“x”**的`').children?.[0].children?.[0].type).toBe('inlineCode')
    const fenced = tree('```\n是**“x”**的\n```').children?.[0]
    expect(fenced?.type).toBe('code')
    expect(fenced?.value).toBe('是**“x”**的')
  })

  it('keeps the text content identical when the emphasis is repaired', () => {
    const source = '是** “每个 tick” 的领取器**——y'
    const text = segments(source).map(([, value]) => value).join('')
    expect(text).toBe(source.replace(/\*\*/g, ''))
  })
})
