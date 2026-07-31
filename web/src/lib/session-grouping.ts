/**
 * session-grouping — pure helpers for grouping & sorting the session list.
 *
 * Spec 3 §3.2. Kept separate from the hook so the logic is trivially
 * unit-testable and reusable (the search view flattens with the same sort).
 *
 * Grouping key types are opaque strings; the UI maps them to translated
 * labels. Sorting is stable on top of the starred-first / lastActive-desc
 * rule.
 */
import type { SessionCategory, SessionInfo, SessionSelector, SessionStatus } from '@/types/shared'

export function sessionKey(s: SessionSelector): string {
  return `${s.channel || 'web'}:${s.chatID}`
}

export function sameSession(a: SessionSelector | null | undefined, b: SessionSelector | null | undefined): boolean {
  return !!a && !!b && (a.channel || 'web') === (b.channel || 'web') && a.chatID === b.chatID
}

export function isSubAgentSession(s: Pick<SessionInfo, 'chatID' | 'channel' | 'type' | 'parentChatID' | 'fullKey' | 'agentChatID'>): boolean {
  return s.type === 'agent' || !!s.parentChatID || isInteractiveSubAgentTenant(s.channel, s.fullKey || s.agentChatID || s.chatID)
}

export function isInteractiveSubAgentTenant(channel: string | undefined, chatID: string): boolean {
  const ch = channel || 'web'
  if (ch === 'agent' || chatID.startsWith('agent:')) return true
  if (parseAgentChatID(chatID)) return true
  const prefix = `${ch}:`
  return chatID.startsWith(prefix) && chatID.slice(prefix.length).includes('/')
}

export interface ParsedAgentChatID {
  parentChannel: string
  parentChatID: string
  role: string
  instance: string
}

/**
 * TUI interactive SubAgent tenant key:
 *   <parent-channel>:<parent-chat-id>/<role>[:instance]
 *
 * Parent chat IDs may contain ':' and '/', so parse from the last slash and
 * only split the parent channel at the first colon.
 */
export function parseAgentChatID(chatID: string): ParsedAgentChatID | null {
  const slash = chatID.lastIndexOf('/')
  if (slash <= 0 || slash === chatID.length - 1) return null
  const parent = chatID.slice(0, slash)
  const roleInstance = chatID.slice(slash + 1)
  const channelSep = parent.indexOf(':')
  if (channelSep <= 0 || channelSep === parent.length - 1) return null
  const roleSep = roleInstance.lastIndexOf(':')
  const hasInstance = roleSep > 0 && roleSep < roleInstance.length - 1
  return {
    parentChannel: parent.slice(0, channelSep),
    parentChatID: parent.slice(channelSep + 1),
    role: hasInstance ? roleInstance.slice(0, roleSep) : roleInstance,
    instance: hasInstance ? roleInstance.slice(roleSep + 1) : '',
  }
}

/** Bucket a single session into one group key for the active category. */
export function sessionGroupKey(s: SessionInfo, category: SessionCategory): string {
  switch (category) {
    case 'time':
      return timeBucket(s.lastActive)
    case 'status':
      return s.status
    case 'path':
      return pathBucket(s)
  }
}

/**
 * Return the full work directory used as the stable `path` grouping key.
 *
 * CLI sessions have chatID like `/path/to/workdir:session-name`.
 * Older servers are supported by parsing CLI-style chat IDs.
 */
export function pathBucket(s: SessionInfo): string {
  const explicit = normalizeWorkDir(s.workDir || '')
  if (explicit) return explicit
  // For SubAgent sessions, inherit the parent's path group
  if (s.parentChatID && s.parentChannel) {
    return pathBucket({ ...s, chatID: s.parentChatID, channel: s.parentChannel, parentChatID: undefined, parentChannel: undefined })
  }
  if (s.channel === 'cli') {
    const workDir = extractWorkDir(s.chatID)
    if (workDir) return normalizeWorkDir(workDir)
  }
  return '__unset__'
}

/** Extract the workDir from a CLI-style chatID: `/path/to/workdir:session-name`. */
function extractWorkDir(chatID: string): string {
  const idx = chatID.lastIndexOf(':')
  const candidate = idx > 1 && idx < chatID.length - 1 ? chatID.slice(0, idx) : chatID
  // Must look like a path (starts with / or drive letter or ~)
  if (candidate.startsWith('/') || /^[A-Za-z]:[\\/]/.test(candidate) || candidate.startsWith('~')) {
    return candidate
  }
  return ''
}

function normalizeWorkDir(path: string): string {
  const trimmed = path.trim()
  if (trimmed === '/' || /^[A-Za-z]:[\\/]$/.test(trimmed)) return trimmed
  return trimmed.replace(/[\\/]+$/, '')
}

/** Ordered status groups (UI iterates this for stable ordering). */
export const STATUS_ORDER: SessionStatus[] = ['running', 'waiting_input', 'pending', 'unread', 'idle', 'error']

/** Ordered time buckets. */
export const TIME_BUCKETS = ['today', 'yesterday', 'earlier'] as const
export type TimeBucket = (typeof TIME_BUCKETS)[number]

function timeBucket(lastActive: string): TimeBucket {
  // lastActive is RFC3339 from the backend (UserChatWithPreview.last_active).
  const ts = Date.parse(lastActive)
  if (Number.isNaN(ts)) return 'earlier'
  const now = new Date(ts)
  const startOfToday = new Date()
  startOfToday.setHours(0, 0, 0, 0)
  if (now >= startOfToday) return 'today'
  const startOfYesterday = new Date(startOfToday)
  startOfYesterday.setDate(startOfYesterday.getDate() - 1)
  if (now >= startOfYesterday) return 'yesterday'
  return 'earlier'
}

/**
 * Sort a list of sessions: starred first (stable), then sortOrder (custom,
 * ascending — lower number = higher in list), then createdAt (ascending —
 * older first). This ensures switching sessions never reorders the list.
 *
 * Sessions with sortOrder > 0 (manually reordered via drag-and-drop) come
 * before sessions with sortOrder = 0 (never reordered). Within each group,
 * sort by sortOrder ascending, or createdAt ascending as fallback.
 *
 * `starredIds` is the set of starred chat ids (looked up by chatID).
 */
export function sortSessions(sessions: SessionInfo[], starredIds: string[]): SessionInfo[] {
  const starred = new Set(starredIds)
  return sessions.filter((s) => !isSubAgentSession(s)).sort((a, b) => {
    const sa = starred.has(sessionKey(a)) ? 1 : 0
    const sb = starred.has(sessionKey(b)) ? 1 : 0
    if (sa !== sb) return sb - sa
    const oa = a.sortOrder ?? 0
    const ob = b.sortOrder ?? 0
    if (oa > 0 && ob > 0) {
      // Both have custom order — sort ascending (lower = higher in list)
      if (oa !== ob) return oa - ob
    } else if (oa > 0) {
      return -1 // a has custom order, b doesn't → a first
    } else if (ob > 0) {
      return 1 // b has custom order, a doesn't → b first
    }
    // Neither has custom order → sort by createdAt ascending (oldest first)
    return (a.createdAt || '').localeCompare(b.createdAt || '')
  })
}

export interface SessionGroup {
  key: string
  sessions: SessionInfo[]
}

/**
 * Group + sort sessions for a category. Group order:
 *   - time:    today → yesterday → earlier
 *   - status:  STATUS_ORDER
 *   - path:    sorted by full directory, sessions without a directory last
 *
 * Within each group the full sort (starred-first, lastActive-desc) applies,
 * so starred items float to the top of their group too.
 */
export function groupSessions(
  sessions: SessionInfo[],
  category: SessionCategory,
  starredIds: string[],
): SessionGroup[] {
  const sorted = sortSessions(sessions, starredIds)
  const map = new Map<string, SessionInfo[]>()
  for (const s of sorted) {
    const key = sessionGroupKey(s, category)
    const arr = map.get(key)
    if (arr) arr.push(s)
    else map.set(key, [s])
  }
  let keys: string[]
  if (category === 'status') {
    keys = STATUS_ORDER.filter((k) => map.has(k))
  } else if (category === 'time') {
    keys = TIME_BUCKETS.filter((k) => map.has(k))
  } else {
    // path: sort alphabetically, with the unset bucket last
    keys = [...map.keys()].sort((a, b) => {
      if (a === '__unset__') return 1
      if (b === '__unset__') return -1
      return a.localeCompare(b)
    })
  }
  return keys.map((key) => ({ key, sessions: map.get(key)! }))
}
