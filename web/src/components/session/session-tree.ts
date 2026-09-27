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

// pruneSubAgentForest keeps the hierarchy: a node is kept when it is itself
// interesting OR any descendant is. The older "flatten then filter" trick broke
// the tree — an active SubAgent whose parent was idle lost its parent, so the
// panel showed orphaned rows with no depth information.
export function pruneSubAgentForest(  nodes: SessionInfo[] | undefined,
  isInteresting: (node: SessionInfo) => boolean,
): SessionInfo[] {
  const seen = new Set<string>()
  const visit = (list: SessionInfo[] | undefined): SessionInfo[] => {
    const out: SessionInfo[] = []
    for (const node of list || []) {
      const key = sessionKey(node)
      if (seen.has(key)) continue // same dedupe contract as childrenForParent
      const keptChildren = visit(node.children)
      if (!isInteresting(node) && keptChildren.length === 0) continue
      seen.add(key)
      out.push(keptChildren.length > 0 ? { ...node, children: keptChildren } : node)
    }
    return out
  }
  return visit(nodes)
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
