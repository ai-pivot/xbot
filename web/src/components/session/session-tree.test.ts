import { describe, expect, it } from 'vitest'

import { childrenForParent, descendantsForParent, flattenSubAgentTree, isChildOfSession } from './session-tree'
import { sessionKey } from '@/lib/session-grouping'
import type { SessionInfo } from '@/types/shared'

function session(p: Partial<SessionInfo> & { chatID: string; channel: string }): SessionInfo {
  return {
    chatID: p.chatID,
    channel: p.channel,
    label: p.label ?? p.chatID,
    lastActive: p.lastActive ?? '2026-07-08T00:00:00Z',
    preview: p.preview ?? '',
    status: p.status ?? 'idle',
    isCurrent: p.isCurrent ?? false,
    type: p.type,
    role: p.role,
    instance: p.instance,
    parentChatID: p.parentChatID,
    parentChannel: p.parentChannel,
    children: p.children,
  }
}

describe('session tree helpers', () => {
  it('reads canonical backend-attached children from the parent node', () => {
    const child = session({
      channel: 'agent',
      chatID: 'cli:/repo:Agent-main/review:1',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
    })
    const parent = {
      ...session({ channel: 'cli', chatID: '/repo:Agent-main' }),
      children: [child, child],
    }

    expect(childrenForParent(parent).map(sessionKey)).toEqual([sessionKey(child)])
    expect(isChildOfSession(child, parent)).toBe(true)
  })

  it('does not attach rows by guessing parent fields in the renderer', () => {
    const parent = session({
      channel: 'cli',
      chatID: '/vePFS-Mindverse/user/intern/yihang:Agent-warm-stone',
    })
    const child = session({
      channel: 'agent',
      chatID: 'cli:Agent-warm-stone/review',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: 'Agent-warm-stone',
      role: 'review',
    })

    expect(childrenForParent(parent).map(sessionKey)).toEqual([])
    expect(isChildOfSession(child, parent)).toBe(false)
  })

  it('returns recursive SubAgent descendants for task panels', () => {
    const fix = session({
      channel: 'agent',
      chatID: 'agent:cli:/repo:Agent-main/review:1/fix:2',
      type: 'agent',
      parentChannel: 'agent',
      parentChatID: 'cli:/repo:Agent-main/review:1',
      role: 'fix',
    })
    const review = session({
      channel: 'agent',
      chatID: 'cli:/repo:Agent-main/review:1',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
      role: 'review',
      children: [fix],
    })
    const parent = session({
      channel: 'cli',
      chatID: '/repo:Agent-main',
      children: [review],
    })

    expect(descendantsForParent(parent).map(sessionKey)).toEqual([
      sessionKey(review),
      sessionKey(fix),
    ])
    expect(flattenSubAgentTree([parent]).map(sessionKey)).toEqual([
      sessionKey(review),
      sessionKey(fix),
    ])
  })
})
