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
 * Extract the work directory basename from a session for `path` grouping.
 *
 * CLI sessions have chatID like `/path/to/workdir:session-name`.
 * Web sessions have empty workDir → "Web 会话" group key.
 */
export function pathBucket(s: SessionInfo): string {
  // For SubAgent sessions, inherit the parent's path group
  if (s.parentChatID && s.parentChannel) {
    return pathBucket({ ...s, chatID: s.parentChatID, channel: s.parentChannel, parentChatID: undefined, parentChannel: undefined })
  }
  if (s.channel === 'cli' || (s.channel === 'web' && s.chatID.includes(':'))) {
    const workDir = extractWorkDir(s.chatID)
    if (workDir) return basename(workDir)
  }
  // Web sessions (no workDir) → special group key
  return '__web__'
}

/** Extract the workDir from a CLI-style chatID: `/path/to/workdir:session-name`. */
function extractWorkDir(chatID: string): string {
  const idx = chatID.lastIndexOf(':')
  if (idx <= 0 || idx === chatID.length - 1) return ''
  const candidate = chatID.slice(0, idx)
  // Must look like a path (starts with / or drive letter or ~)
  if (candidate.startsWith('/') || /^[A-Za-z]:[\\/]/.test(candidate) || candidate.startsWith('~')) {
    return candidate
  }
  return ''
}

function basename(path: string): string {
  const clean = path.replace(/[\\/]+$/, '')
  const slash = Math.max(clean.lastIndexOf('/'), clean.lastIndexOf('\\'))
  return slash >= 0 ? clean.slice(slash + 1) : clean
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
 * Sort a list of sessions: starred first (stable), then lastActive desc.
 * `starredIds` is the set of starred chat ids (looked up by chatID).
 */
export function sortSessions(sessions: SessionInfo[], starredIds: string[]): SessionInfo[] {
  const starred = new Set(starredIds)
  return sessions.filter((s) => !isSubAgentSession(s)).sort((a, b) => {
    const sa = starred.has(sessionKey(a)) ? 1 : 0
    const sb = starred.has(sessionKey(b)) ? 1 : 0
    if (sa !== sb) return sb - sa
    return (b.lastActive || '').localeCompare(a.lastActive || '')
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
 *   - path:    sorted by group key (directory basename), Web sessions last
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
    // path: sort alphabetically, with '__web__' last
    keys = [...map.keys()].sort((a, b) => {
      if (a === '__web__') return 1
      if (b === '__web__') return -1
      return a.localeCompare(b)
    })
  }
  return keys.map((key) => ({ key, sessions: map.get(key)! }))
}
