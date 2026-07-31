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
