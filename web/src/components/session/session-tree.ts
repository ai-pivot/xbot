import type { SessionInfo } from '@/types/shared'
import { sessionKey } from '@/lib/session-grouping'

export function childrenForParent(parent: SessionInfo): SessionInfo[] {
  const seen = new Set<string>()
  const result: SessionInfo[] = []
  for (const child of parent.children || []) {
    const childKey = sessionKey(child)
    if (!seen.has(childKey)) {
      seen.add(childKey)
      result.push(child)
    }
  }
  return result
}

export function isChildOfSession(child: SessionInfo, parent: SessionInfo): boolean {
  return childrenForParent(parent).some((candidate) => sessionKey(candidate) === sessionKey(child))
}

export function descendantsForParent(parent: SessionInfo): SessionInfo[] {
  const result: SessionInfo[] = []
  const seen = new Set<string>()
  const visit = (node: SessionInfo) => {
    for (const child of childrenForParent(node)) {
      const key = sessionKey(child)
      if (seen.has(key)) continue
      seen.add(key)
      result.push(child)
      visit(child)
    }
  }
  visit(parent)
  return result
}

export function flattenSubAgentTree(sessions: SessionInfo[]): SessionInfo[] {
  const result: SessionInfo[] = []
  const seen = new Set<string>()
  const visit = (nodes: SessionInfo[] | undefined) => {
    for (const node of nodes || []) {
      const key = sessionKey(node)
      if (!seen.has(key)) {
        seen.add(key)
        result.push(node)
      }
      visit(node.children)
    }
  }
  for (const session of sessions) visit(session.children)
  return result
}

export function matchesSelfOrDescendant(
  session: SessionInfo,
  matches: (s: SessionInfo) => boolean,
  seen = new Set<string>(),
): boolean {
  const key = sessionKey(session)
  if (seen.has(key)) return false
  seen.add(key)
  if (matches(session)) return true
  return childrenForParent(session).filter(isVisibleSubAgent).some((child) => matchesSelfOrDescendant(child, matches, seen))
}

export function isVisibleSubAgent(session: SessionInfo): boolean {
  // Always show sub-agents (running AND completed) so users can open and review
  // them. Previously this filtered to running/waiting_input/pending only — a
  // completed sub-agent vanished from the sidebar immediately, and with the
  // (now fixed) unreliable running flag even active ones could disappear.
  // Only historical legacy rows are hidden.
  return session.historical !== true
}
