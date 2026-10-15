import type { SessionInfo } from '@/types/shared'
import { groupSessions, sessionKey, sortSessions } from './session-grouping'

/** Persisted user-defined group; its default directory only presets creation. */
export interface SessionCustomGroup {
  id: string
  name: string
  defaultWorkDir: string
}

/** Authoritative server snapshot. Membership keys are canonical sessionKey values. */
export interface SessionGroupState {
  revision: number
  groups: SessionCustomGroup[]
  memberships: Record<string, string>
}

/** Mutation response carries the exact created group identity, independent of normalization. */
export interface SessionGroupResponse extends SessionGroupState { createdGroupId?: string }

export interface SessionSelector { channel: string; chatID: string }

export type SessionGroupAction =
  | { action: 'create' | 'convert'; name: string; defaultWorkDir: string; sessions?: SessionSelector[] }
  | { action: 'update'; id: string; name: string; defaultWorkDir: string }
  | { action: 'delete'; id: string }
  | { action: 'move'; sessions: SessionSelector[]; groupId: string | null }
  | { action: 'reorder'; orderedIds: string[] }

export interface ComposedSessionGroup {
  key: string
  sessions: SessionInfo[]
  customGroup?: SessionCustomGroup
}

/** Custom membership wins over cwd; unassigned sessions retain automatic path grouping. */
export function composeSessionGroups(
  sessions: SessionInfo[], stars: string[], state: SessionGroupState,
): ComposedSessionGroup[] {
  const custom = state.groups.map((customGroup) => ({
    key: `custom:${customGroup.id}`, customGroup, sessions: [] as SessionInfo[],
  }))
  const byID = new Map(custom.map((group) => [group.customGroup.id, group]))
  const automatic: SessionInfo[] = []
  for (const session of sortSessions(sessions, stars)) {
    const group = byID.get(state.memberships[sessionKey(session)])
    if (group) group.sessions.push(session)
    else automatic.push(session)
  }
  return [...custom, ...groupSessions(automatic, 'path', stars)]
}
