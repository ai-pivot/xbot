/**
 * Unit tests for source-level Markdown preprocessing (src/lib/markdownSource.ts).
 *
 * The reported incident: a model reply containing
 *
 *   PR 已提: **https://github.com/org/repo/pull/92**（`branch` → `main`）
 *
 * rendered as a literal `**https://…**（`branch`` because GFM's autolink
 * literal consumes everything up to the next whitespace — including the closing
 * `**`, the full-width parenthesis and the following inline code. The link href
 * (and label) became the whole run instead of the URL.
 */
import { describe, expect, it } from 'vitest'

import { codeFenceAfterLine, protectBareURLs } from '@/lib/markdownSource'

describe('protectBareURLs', () => {
  it('ends a bare URL before the emphasis delimiter that follows it', () => {
    expect(protectBareURLs('**https://github.com/org/repo/pull/92**（正文）')).toBe(
      '**<https://github.com/org/repo/pull/92>**（正文）',
    )
  })

  it('keeps the reported line intact apart from the hard autolink', () => {
    const src =
      'PR 已提: **https://github.com/MindLab-Research/mint-ops/pull/92**（`ops/prd-console-20261107` → `main`, 1 文件 +9/-5）。'
    expect(protectBareURLs(src)).toBe(
      'PR 已提: **<https://github.com/MindLab-Research/mint-ops/pull/92>**（`ops/prd-console-20261107` → `main`, 1 文件 +9/-5）。',
    )
  })

  it('ends a bare URL before full-width punctuation', () => {
    expect(protectBareURLs('地址 https://example.com/a。')).toBe('地址 <https://example.com/a>。')
    expect(protectBareURLs('（见 https://example.com/a）')).toBe('（见 <https://example.com/a>）')
  })

  it('ends a bare URL before inline code and strikethrough delimiters', () => {
    expect(protectBareURLs('https://example.com/a`code`')).toBe('<https://example.com/a>`code`')
    expect(protectBareURLs('**https://example.com/a**`code`')).toBe('**<https://example.com/a>**`code`')
    expect(protectBareURLs('~~https://example.com/a~~ 删掉')).toBe('~~<https://example.com/a>~~ 删掉')
    expect(protectBareURLs('__https://example.com/a__ 强调')).toBe('__<https://example.com/a>__ 强调')
  })

  it('leaves prose URLs to GFM (whitespace-terminated)', () => {
    const src = 'see https://example.com/a?b=1&c=2 here'
    expect(protectBareURLs(src)).toBe(src)
  })

  it('leaves balanced parentheses inside the URL', () => {
    const src = 'https://en.wikipedia.org/wiki/Foo_(bar)'
    expect(protectBareURLs(src)).toBe(src)
  })

  it('leaves doubled underscores and single tildes that are part of the path', () => {
    expect(protectBareURLs('https://example.com/foo__bar/file.js')).toBe(
      'https://example.com/foo__bar/file.js',
    )
    expect(protectBareURLs('https://example.com/~user/notes')).toBe('https://example.com/~user/notes')
  })

  it('moves ASCII trailing punctuation out of the link', () => {
    expect(protectBareURLs('**https://example.com/a.**（下一句）')).toBe(
      '**<https://example.com/a>.**（下一句）',
    )
    expect(protectBareURLs('**https://example.com/a).**（下一句）')).toBe(
      '**<https://example.com/a>).**（下一句）',
    )
  })

  it('does not touch inline code spans', () => {
    const src = '运行 `curl **https://example.com/a**（x）` 看看'
    expect(protectBareURLs(src)).toBe(src)
  })

  it('does not touch fenced code blocks', () => {
    const src = '```md\n**https://example.com/a**（x）\n```\n\n**https://example.com/b**（y）'
    expect(protectBareURLs(src)).toBe(
      '```md\n**https://example.com/a**（x）\n```\n\n**<https://example.com/b>**（y）',
    )
  })

  it('does not wrap existing hard autolinks', () => {
    const src = '**<https://example.com/a>**（下一句）'
    expect(protectBareURLs(src)).toBe(src)
  })

  it('does not touch markdown link destinations or labels', () => {
    const dest = '[x](https://example.com/a*b)'
    expect(protectBareURLs(dest)).toBe(dest)
    const label = '[https://example.com/a](https://example.com/b)'
    expect(protectBareURLs(label)).toBe(label)
  })

  it('does not linkify URLs glued to a preceding ASCII letter', () => {
    const src = 'abchttps://example.com/a（x）'
    expect(protectBareURLs(src)).toBe(src)
  })

  it('leaves www autolinks to GFM (angle brackets would change the label)', () => {
    const src = '**www.example.com**（x）'
    expect(protectBareURLs(src)).toBe(src)
  })
})

describe('codeFenceAfterLine', () => {
  it('tracks fence openers and closers', () => {
    expect(codeFenceAfterLine('```ts', '')).toBe('```')
    expect(codeFenceAfterLine('```', '```')).toBe('')
    // A closing fence may be longer than the opener (CommonMark).
    expect(codeFenceAfterLine('````', '```')).toBe('')
    // A shorter fence or one with a suffix does not close a longer lock.
    expect(codeFenceAfterLine('```', '````')).toBe('````')
    expect(codeFenceAfterLine('```js', '```')).toBe('```')
  })
})
