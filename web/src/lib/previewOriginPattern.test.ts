import { describe, expect, it } from 'vitest'
import { deriveVercelOriginPattern, previewOriginsSnippet } from './previewOriginPattern'

describe('deriveVercelOriginPattern', () => {
  it('derives the project-wide single-label wildcard from the deployment and branch hosts', () => {
    expect(deriveVercelOriginPattern(
      'xbot-preview-ouws4udwi-mattos-projects-6bf6c006.vercel.app',
      'xbot-preview-git-fix-markdown-a-46d8f7-mattos-projects-6bf6c006.vercel.app',
    )).toBe('https://xbot-preview-*-mattos-projects-6bf6c006.vercel.app')
  })
  it('aligns the wildcard to dash boundaries when the variable parts share characters', () => {
    // hash starts with "g" like "git-…" and both end in "1" before the team slug
    expect(deriveVercelOriginPattern('app-g8x7k2p11-team.vercel.app', 'app-git-main-team.vercel.app'))
      .toBe('https://app-*-team.vercel.app')
    expect(deriveVercelOriginPattern('app-abcd1-team.vercel.app', 'app-git-x1-team.vercel.app'))
      .toBe('https://app-*-team.vercel.app')
  })
  it('returns empty when it cannot produce a safe pattern', () => {
    for (const [a, b] of [
      [undefined, 'app-git-main-team.vercel.app'],
      ['app-abc-team.vercel.app', undefined],
      ['', ''],
      ['app-abc-team.vercel.app', 'app-abc-team.vercel.app'], // identical
      ['app-abc-team.example.com', 'app-git-main-team.example.com'], // not vercel.app
      ['abc-team.vercel.app', 'git-main-team.vercel.app'], // no literal prefix
      ['app-abc.vercel.app', 'app-git-main.vercel.app'], // no literal suffix
      ['a.app-abc-team.vercel.app', 'a.app-git-team.vercel.app'], // extra label
      ['App-abc-team.vercel.app', 'App-git-team.vercel.app'], // not canonical lowercase
      ['https://app-abc-team.vercel.app', 'https://app-git-team.vercel.app'], // scheme, not a host
    ] as const) {
      expect(deriveVercelOriginPattern(a, b), `${a} / ${b}`).toBe('')
    }
  })
})

describe('previewOriginsSnippet', () => {
  it('renders the config.json fragment for the given origin', () => {
    expect(previewOriginsSnippet('https://xbot-preview-*-team.vercel.app')).toBe(
      '"web": {\n  "preview_origins": [\n    "https://xbot-preview-*-team.vercel.app"\n  ]\n}',
    )
  })
})
