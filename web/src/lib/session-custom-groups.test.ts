import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '@/types/shared'
import { composeSessionGroups, type SessionGroupState } from './session-custom-groups'
import { sessionKey } from './session-grouping'

const session = (chatID: string, channel = 'web', workDir = '/repo'): SessionInfo => ({
  chatID, channel, workDir, label: chatID, lastActive: '', preview: '', status: 'idle', isCurrent: false,
})

describe('composeSessionGroups', () => {
  it('preserves custom order and empty groups; membership wins over path without changing cwd', () => {
    const a = session('same'), b = session('same', 'cli'), c = session('unset', 'web', '')
    const state: SessionGroupState = {
      revision: 1, groups: [{ id: 'empty', name: 'Empty', defaultWorkDir: '/new' }, { id: 'g', name: 'Team', defaultWorkDir: '/other' }],
      memberships: { [sessionKey(a)]: 'g' },
    }
    const groups = composeSessionGroups([a, b, c], [], state)
    expect(groups.map((g) => g.key)).toEqual(['custom:empty', 'custom:g', '/repo', '__unset__'])
    expect(groups[0].sessions).toEqual([])
    expect(groups[1].sessions).toEqual([a])
    expect(groups[2].sessions).toEqual([b])
    expect(a.workDir).toBe('/repo')
    expect(state.memberships).toEqual({ [sessionKey(a)]: 'g' })
  })

  it('uses existing starred sorting and automatic grouping after membership reset', () => {
    const a = session('a'), b = session('b')
    const state: SessionGroupState = { revision: 2, groups: [{ id: 'g', name: 'G', defaultWorkDir: '' }], memberships: { [sessionKey(a)]: 'g', [sessionKey(b)]: 'g' } }
    expect(composeSessionGroups([a, b], [sessionKey(b)], state)[0].sessions).toEqual([b, a])
    expect(composeSessionGroups([a], [], { ...state, memberships: {} })[1].key).toBe('/repo')
  })
})
