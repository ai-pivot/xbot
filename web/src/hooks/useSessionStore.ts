/**
 * useSessionStore — session list state + data layer (Spec 3).
 *
 * Responsibilities:
 *   - render the cached tree, then refresh it with POST /api/session-tree
 *   - derive session status from SSE events:
 *       session.action 'busy'   → running
 *       session.action 'idle'   → idle
 *       ask_user message        → waiting_input
 *       (any error msg)         → error  (best-effort; not in scope UI)
 *   - star persistence (localStorage, Spec 3 §3.3)
 *   - create / switch / rename / delete via REST, with EventSource switching
 *   - CWD error handling with toast (Child 5)
 *
 * Backend contracts (channel/web/web_api.go):
 *   POST   /api/session-tree                 → { ok, data: { sessions } }
 *   POST   /api/chats/create {label}         → { ok, data: { chat_id } }
 *   POST   /api/chats/{id}/switch {channel}  → { ok, data: { chat_id, channel } }
 *   POST   /api/chats/{id}/rename {channel,label} → { ok }
 *   POST   /api/chats/{id}/delete {channel}       → { ok, data: {} }
 *   POST   /api/rpc set_cwd                  → set working directory
 *   GET    /api/sse?chat_id=...&channel=...  → server events
 */
import { createElement, createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { getContextUsage, setCwd } from '@/components/agent/api'
import { useWSConnection } from '@/hooks/useWSConnection'
import { postAPI } from '@/lib/api'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'
import { groupSessions, parseAgentChatID, sameSession, sessionKey, sortSessions } from '@/lib/session-grouping'
import { clearSessionCaches, loadSessionTreeCache, saveSessionTreeCache, sessionCacheKey } from '@/lib/webCache'
import { rememberRecentWorkDir } from '@/lib/recent-workdirs'
import type { SessionCategory, SessionEvent, SessionInfo, SessionSelector, SessionStatus, TodoItem } from '@/types/shared'
import type { AskUserPrompt, AskUserQuestion } from '@/types/agent'

const STARRED_KEY = 'xbot-starred'
const CATEGORY_KEY = 'xbot:session-category'
const UNREAD_KEY = 'xbot:session-unread'
const ACTIVE_CHANNEL_KEY = 'xbot:active-channel'
const DEFAULT_CHANNEL = 'web'
const TRANSIENT_SUBAGENT_TTL_MS = 10 * 60 * 1000
/** How long an SSE session(busy)/subagent_started event is trusted over a
 * contradicting HTTP session-tree response. The session-tree RPC reads
 * chatCancelCh, which lags the SSE idle event by up to one round-trip — the
 * window absorbs that lag. Past the window, HTTP is authoritative (idle wins),
 * so a LOST idle event (SSE ring eviction, disconnect window, cross-route
 * delivery gap) is corrected on the next refresh instead of forcing running
 * forever. */
const EXECUTING_TRUST_WINDOW_MS = 15_000
/** Sidebar session-tree page size (backend pagination). */
const SESSION_TREE_PAGE_SIZE = 60

/** WSMessage shape we care about here (avoids importing the full envelope). */
interface AskUserEnvelope {
  type: string
  chat_id?: string
}

export interface SessionGroup {
  key: string
  sessions: SessionInfo[]
}

export interface SessionStore {
  sessions: SessionInfo[]
  groups: SessionGroup[]
  /** Flat list, sorted (starred-first, lastActive-desc) — used by search. */
  sortedSessions: SessionInfo[]
  activeSessionId: string | null
  activeSession: SessionSelector | null
  starredIds: string[]
  category: SessionCategory
  /** Set of session keys with unread replies. */
  unreadIds: string[]
  /** Currently selected channel filter (null = show all). */
  activeChannel: string | null
  loading: boolean
  error: string | null
  /** SubAgent sessions for visible parent chats. */
  subAgents: SessionInfo[]
  /** Pending AskUser prompts keyed by "channel:chatID". Survives session switch. */
  askUserPrompts: Map<string, AskUserPrompt>
  setCategory: (c: SessionCategory) => void
  setActiveChannel: (channel: string | null) => void
  markRead: (key: string) => void
  /** Optimistically set a session's status (e.g. running after send). */
  setStatus: (selector: SessionSelector, status: SessionStatus) => void
  refresh: () => Promise<void>
  /** Whether more paginated web sessions exist beyond the loaded window. */
  hasMore: boolean
  /** Fetch the next page of web sessions and append them (backend pagination). */
  loadMore: () => Promise<void>
  toggleStar: (id: string) => void
  createSession: (label?: string, workPath?: string, model?: string, subscriptionId?: string) => Promise<string | null>
  /** Fork: copy a session's conversation context into a new session. Returns new chatID or null. */
  forkSession: (sourceChatID: string, sourceChannel?: string, label?: string) => Promise<string | null>
  switchSession: (id: string, channel: string) => Promise<void>
  /** Lightweight session activation (no cache clearing, no async wait).
   * Used when switching active tabs — each tab keeps its own state, so we
   * only need to update the sidebar highlight + backend tracking. */
  activateSession: (id: string, channel: string) => void
  renameSession: (id: string, channel: string, label: string) => Promise<boolean>
  deleteSession: (id: string, channel: string) => Promise<boolean>
  /** Batch-update sort_order for sessions (drag-and-drop reordering). */
  reorderSessions: (channel: string, orderedIDs: string[]) => Promise<boolean>
  /** Clear the AskUser prompt for a session (after answer/cancel). */
  clearAskUserPrompt: (channel: string, chatID: string) => void
}

/* ── localStorage starred ids ── */

function loadStarred(): string[] {
  try {
    const raw = localStorage.getItem(STARRED_KEY)
    const parsed = raw ? (JSON.parse(raw) as unknown) : null
    if (Array.isArray(parsed)) return parsed.filter((x): x is string => typeof x === 'string')
  } catch {
    /* ignore */
  }
  return []
}

function persistStarred(ids: string[]): void {
  try {
    const value = JSON.stringify(ids)
    localStorage.setItem(STARRED_KEY, value)
    syncSettingToServer(STARRED_KEY, value)
  } catch {
    /* ignore */
  }
}

/* ── localStorage category persistence ── */

function loadCategory(): SessionCategory {
  try {
    const raw = localStorage.getItem(CATEGORY_KEY)
    if (raw === 'time' || raw === 'status' || raw === 'path') return raw
  } catch {
    /* ignore */
  }
  return 'time'
}

function persistCategory(c: SessionCategory): void {
  try {
    localStorage.setItem(CATEGORY_KEY, c)
    syncSettingToServer(CATEGORY_KEY, c)
  } catch {
    /* ignore */
  }
}

/* ── localStorage unread set persistence ── */

function loadUnread(): string[] {
  try {
    const raw = localStorage.getItem(UNREAD_KEY)
    const parsed = raw ? (JSON.parse(raw) as unknown) : null
    if (Array.isArray(parsed)) return parsed.filter((x): x is string => typeof x === 'string')
  } catch {
    /* ignore */
  }
  return []
}

function persistUnread(ids: string[]): void {
  try {
    localStorage.setItem(UNREAD_KEY, JSON.stringify(ids))
  } catch {
    /* ignore */
  }
}

/* ── localStorage active channel persistence ── */

function loadActiveChannel(): string | null {
  try {
    const raw = localStorage.getItem(ACTIVE_CHANNEL_KEY)
    if (raw === null || typeof raw === 'string') return raw
  } catch {
    /* ignore */
  }
  return null
}

function persistActiveChannel(channel: string | null): void {
  try {
    if (channel === null) localStorage.removeItem(ACTIVE_CHANNEL_KEY)
    else localStorage.setItem(ACTIVE_CHANNEL_KEY, channel)
  } catch {
    /* ignore */
  }
}

/* ── API responses ── */

interface ListSessionTreeResponse {
  sessions?: RawTreeNode[]
  orphan_subagents?: RawChat[]
  has_more?: boolean
  next_offset?: number
}
interface RawChat {
  chat_id: string
  channel?: string
  label: string
  work_dir?: string
  last_active: string
  created_at?: string
  sort_order?: number
  preview?: string
  is_current?: boolean
  type?: string
  full_key?: string
  role?: string
  instance?: string
  parent_chat_id?: string
  parent_channel?: string
  historical?: boolean
  running?: boolean
  status?: SessionStatus
  synthetic?: boolean
  children?: RawChat[]
}
type RawTreeNode = RawChat
interface CreateChatResponse {
  chat_id?: string
}
interface SwitchChatResponse {
  chat_id?: string
  channel?: string
  todos?: TodoItem[]
}
interface TransientSubAgent {
  session: SessionInfo
  updatedAt: number
}
/** Normalize a raw backend chat into a SessionInfo (default status 'idle'). */
function toSessionInfo(c: RawChat, channel: string, children?: SessionInfo[]): SessionInfo {
  const fullKey = c.full_key || c.chat_id
  const parsedAgent = parseAgentChatID(fullKey)
  const isAgent = isRawAgentRow(c, c.channel || channel, parsedAgent)
  const rawChannel = isAgent ? 'agent' : (c.channel || channel)
  const role = parsedAgent?.role || c.role
  const instance = parsedAgent?.instance || c.instance
  const parentChatID = c.parent_chat_id || parsedAgent?.parentChatID
  const parentChannel = c.parent_channel || parsedAgent?.parentChannel
  const isHistoricalAgent = isAgent && c.historical === true
  const label = isAgent
    ? subAgentLabel(c.label, role, instance, c.chat_id)
    : sessionDisplayLabel(c.label, c.chat_id, rawChannel)
  return {
    chatID: isAgent ? fullKey : c.chat_id,
    channel: rawChannel,
    label,
    workDir: c.work_dir || undefined,
    lastActive: c.last_active,
    createdAt: c.created_at,
    sortOrder: c.sort_order,
    preview: c.preview || '',
    status: c.status || (c.running ? 'running' : 'idle'),
    isCurrent: !!c.is_current,
    type: isAgent ? 'agent' : 'main',
    fullKey: isAgent ? fullKey : undefined,
    role,
    instance,
    parentChatID,
    parentChannel,
    historical: isHistoricalAgent,
    agentChatID: isAgent ? fullKey : undefined,
    running: !!c.running,
    synthetic: !!c.synthetic,
    children,
  }
}

function subAgentLabel(label: string, role?: string, instance?: string, chatID?: string): string {
  const raw = (label || '').trim()
  if (role) {
    return instance ? `${role}/${instance}` : role
  }
  if (!raw || raw === 'default' || raw === '默认会话') return instance || chatID || 'SubAgent'
  return label
}

function sessionDisplayLabel(label: string, chatID: string, channel: string): string {
  if (channel !== 'cli') return label
  const raw = (label || '').trim()
  if (raw && raw !== 'default' && raw !== '默认会话') return label
  const { workDir, name } = parseCLIChatID(chatID)
  if (name && name !== 'default') return name
  const base = basename(workDir)
  return base || name || label || chatID
}

function parseCLIChatID(chatID: string): { workDir: string; name: string } {
  const idx = chatID.lastIndexOf(':')
  if (idx <= 0 || idx === chatID.length - 1) {
    return { workDir: '', name: chatID }
  }
  return { workDir: chatID.slice(0, idx), name: chatID.slice(idx + 1) }
}

function basename(path: string): string {
  const clean = path.replace(/[\\/]+$/, '')
  const slash = Math.max(clean.lastIndexOf('/'), clean.lastIndexOf('\\'))
  return slash >= 0 ? clean.slice(slash + 1) : clean
}

export function normalizeSessionTree(rows: RawTreeNode[], orphanRows: RawChat[] = []): { mainSessions: SessionInfo[]; agents: SessionInfo[] } {
  const mainByKey = new Map<string, SessionInfo>()
  const mainFallback = new Map<string, SessionInfo | null>()
  const agentByKey = new Map<string, SessionInfo>()
  const looseAgentRows: RawChat[] = []
  const normalizeAgentChildren = (children: RawChat[], parentChannel: string, parentChatID: string): SessionInfo[] => {
    const result: SessionInfo[] = []
    for (const child of children) {
      const childChannel = child.channel || 'agent'
      const childAgents = normalizeAgentChildren(child.children || [], childChannel, child.chat_id)
      const agent = toSessionInfo({
        ...child,
        type: 'agent',
        channel: childChannel,
        parent_chat_id: child.parent_chat_id || parentChatID,
        parent_channel: child.parent_channel || parentChannel,
      }, 'agent', childAgents)
      indexAgent(agentByKey, agent)
      result.push(agent)
    }
    return result
  }
  for (const node of rows) {
    if (isRawAgentRow(node)) {
      looseAgentRows.push(node)
      continue
    }
    const parentChannel = node.channel || DEFAULT_CHANNEL
    const childAgents = normalizeAgentChildren(node.children || [], parentChannel, node.chat_id)
    const main = toSessionInfo({
      ...node,
      type: 'main',
      channel: parentChannel,
      parent_chat_id: undefined,
      parent_channel: undefined,
    }, parentChannel, childAgents)
    const existing = mainByKey.get(sessionKey(main))
    if (existing?.children?.length) {
      for (const child of existing.children) main.children = appendUniqueChild(main.children, child)
    }
    indexMainSession(mainByKey, mainFallback, main)
  }
  for (const row of [...looseAgentRows, ...orphanRows]) {
    const agent = toSessionInfo({ ...row, type: 'agent', channel: row.channel || 'agent' }, 'agent')
    attachOrphanAgent(agent, mainByKey, mainFallback, agentByKey)
  }
  const agents = flattenTreeAgents([...mainByKey.values()])
  return {
    mainSessions: [...mainByKey.values()],
    agents,
  }
}

export function normalizeCanonicalSessionTree(rows: RawTreeNode[], orphanRows: RawChat[] = []): { mainSessions: SessionInfo[]; agents: SessionInfo[] } {
  const looseAgentRows: RawChat[] = []
  const normalizeAgentChildren = (children: RawChat[], parentChannel: string, parentChatID: string): SessionInfo[] => {
    const result: SessionInfo[] = []
    for (const child of children) {
      const childAgents = normalizeAgentChildren(child.children || [], child.channel || 'agent', child.chat_id)
      const agent = toSessionInfo({
        ...child,
        type: 'agent',
        channel: 'agent',
        parent_chat_id: child.parent_chat_id || parentChatID,
        parent_channel: child.parent_channel || parentChannel,
      }, 'agent', childAgents)
      result.push(agent)
    }
    return result
  }
  const mainSessions: SessionInfo[] = []
  for (const node of rows) {
    if (isRawAgentRow(node)) {
      looseAgentRows.push(node)
      continue
    }
    const parentChannel = node.channel || DEFAULT_CHANNEL
    const main = toSessionInfo({
      ...node,
      type: 'main',
      channel: parentChannel,
      parent_chat_id: undefined,
      parent_channel: undefined,
    }, parentChannel, normalizeAgentChildren(node.children || [], parentChannel, node.chat_id))
    mainSessions.push(main)
  }
  const supplementRows = mergeRawSubAgentRows(looseAgentRows, orphanRows)
  if (supplementRows.length === 0) return { mainSessions, agents: flattenTreeAgents(mainSessions) }

  const mainByKey = new Map<string, SessionInfo>()
  const mainFallback = new Map<string, SessionInfo | null>()
  const agentByKey = new Map<string, SessionInfo>()
  for (const session of mainSessions) {
    indexMainSession(mainByKey, mainFallback, session)
    for (const agent of flattenTreeAgents([session])) indexAgent(agentByKey, agent)
  }
  for (const row of supplementRows) {
    const agent = toSessionInfo({ ...row, type: 'agent', channel: row.channel || 'agent' }, 'agent')
    attachOrphanAgent(agent, mainByKey, mainFallback, agentByKey)
  }
  const merged = [...mainByKey.values()]
  return { mainSessions: merged, agents: flattenTreeAgents(merged) }
}

function isRawAgentRow(row: RawChat, channel = row.channel, parsed = parseAgentChatID(row.full_key || row.chat_id)): boolean {
  return row.type === 'agent' ||
    row.type === 'subagent' ||
    channel === 'agent' ||
    !!row.parent_chat_id ||
    !!parsed ||
    !!row.role ||
    !!row.instance
}

function attachOrphanAgent(
  agent: SessionInfo,
  mainByKey: Map<string, SessionInfo>,
  mainFallback: Map<string, SessionInfo | null>,
  agentByKey: Map<string, SessionInfo>,
): void {
  if (!agent.parentChannel || !agent.parentChatID) return
  if (findAgent(agentByKey, agent)) return

  const parentSelector = { channel: agent.parentChannel, chatID: agent.parentChatID }
  const parentKey = sessionKey(parentSelector)
  const parentAgent = agentByKey.get(parentKey)
  if (parentAgent) {
    parentAgent.children = appendUniqueChild(parentAgent.children, agent)
    indexAgent(agentByKey, agent)
    return
  }

  let parent = lookupMainSession(mainByKey, mainFallback, agent.parentChannel, agent.parentChatID)
  if (!parent && agent.parentChannel === 'agent') {
    parent = synthesizeMissingAgentParent(agent.parentChatID, agent.lastActive)
    if (parent) {
      attachOrphanAgent(parent, mainByKey, mainFallback, agentByKey)
      parent = findAgent(agentByKey, parent)
    }
  }
  if (!parent && canSynthesizeParent(agent.parentChannel, agent.parentChatID)) {
    parent = syntheticParentSession(agent.parentChannel, agent.parentChatID, agent.lastActive)
    indexMainSession(mainByKey, mainFallback, parent)
  }
  if (!parent) return
  parent.children = appendUniqueChild(parent.children, agent)
  indexAgent(agentByKey, agent)
}

function indexMainSession(
  exact: Map<string, SessionInfo>,
  fallback: Map<string, SessionInfo | null>,
  session: SessionInfo,
): void {
  exact.set(sessionKey(session), session)
  for (const key of mainFallbackKeys(session.channel, session.chatID)) {
    const existing = fallback.get(key)
    fallback.set(key, existing && existing !== session ? null : session)
  }
}

function lookupMainSession(
  exact: Map<string, SessionInfo>,
  fallback: Map<string, SessionInfo | null>,
  channel: string,
  chatID: string,
): SessionInfo | undefined {
  const direct = exact.get(sessionKey({ channel, chatID }))
  if (direct) return direct
  const qualified = splitQualifiedSessionKey(chatID)
  if (qualified) {
    const found = exact.get(sessionKey(qualified))
    if (found) return found
    channel = qualified.channel
    chatID = qualified.chatID
  }
  for (const key of mainFallbackKeys(channel, chatID)) {
    const found = fallback.get(key)
    if (found) return found
  }
  return undefined
}

function mainFallbackKeys(channel: string, chatID: string): string[] {
  if ((channel || DEFAULT_CHANNEL) !== 'cli') return []
  const name = cliSessionNameFromChatID(chatID)
  if (!name || name === 'default') return []
  return [sessionKey({ channel: 'cli', chatID: name })]
}

function cliSessionNameFromChatID(chatID: string): string {
  const idx = chatID.lastIndexOf(':')
  if (idx <= 0 || idx === chatID.length - 1) return chatID
  return chatID.slice(idx + 1)
}

function splitQualifiedSessionKey(value: string): SessionSelector | null {
  const idx = value.indexOf(':')
  if (idx <= 0 || idx === value.length - 1) return null
  const channel = value.slice(0, idx)
  if (!/^[A-Za-z0-9_-]+$/.test(channel)) return null
  return { channel, chatID: value.slice(idx + 1) }
}

function indexAgent(index: Map<string, SessionInfo>, agent: SessionInfo): void {
  for (const key of agentIndexKeys(agent)) index.set(key, agent)
  for (const child of agent.children || []) indexAgent(index, child)
}

function findAgent(index: Map<string, SessionInfo>, agent: SessionInfo): SessionInfo | undefined {
  for (const key of agentIndexKeys(agent)) {
    const existing = index.get(key)
    if (existing) return existing
  }
  return undefined
}

function agentIndexKeys(agent: SessionInfo): string[] {
  const keys = new Set<string>()
  keys.add(sessionKey(agent))
  for (const id of [agent.fullKey, agent.agentChatID]) {
    if (id) keys.add(sessionKey({ channel: 'agent', chatID: id }))
  }
  return [...keys]
}

function appendUniqueChild(children: SessionInfo[] | undefined, child: SessionInfo): SessionInfo[] {
  const next = children ? [...children] : []
  if (!next.some((existing) => sessionKey(existing) === sessionKey(child))) next.push(child)
  return next
}

function syntheticParentSession(channel: string, chatID: string, lastActive: string): SessionInfo {
  return {
    chatID,
    channel,
    label: sessionDisplayLabel('default', chatID, channel),
    lastActive,
    preview: '',
    status: 'idle',
    isCurrent: false,
    type: 'main',
    synthetic: true,
    children: [],
  }
}

function canSynthesizeParent(channel: string, chatID: string): boolean {
  if (!channel || !chatID) return false
  if (channel === 'web') return true
  return channel === 'cli' && looksLikeCLIChatID(chatID)
}

function looksLikeCLIChatID(chatID: string): boolean {
  const { workDir, name } = parseCLIChatID(chatID)
  return looksLikeWorkDir(workDir) || (!!name && name !== 'default')
}

function looksLikeWorkDir(path: string): boolean {
  return path.startsWith('/') || /^[A-Za-z]:[\\/]/.test(path) || path.startsWith('~')
}

function synthesizeMissingAgentParent(fullKey: string, lastActive: string): SessionInfo | undefined {
  const parsed = parseAgentChatID(fullKey)
  if (!parsed) return undefined
  return {
    chatID: fullKey,
    channel: 'agent',
    label: subAgentLabel('default', parsed.role, parsed.instance, fullKey),
    lastActive,
    preview: '',
    status: 'idle',
    isCurrent: false,
    type: 'agent',
    fullKey,
    role: parsed.role,
    instance: parsed.instance,
    parentChannel: parsed.parentChannel,
    parentChatID: parsed.parentChatID,
    historical: true,
    agentChatID: fullKey,
    synthetic: true,
    children: [],
  }
}

function flattenTreeAgents(sessions: SessionInfo[]): SessionInfo[] {
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

function cloneSessionTree(session: SessionInfo): SessionInfo {
  return {
    ...session,
    children: session.children?.map(cloneSessionTree),
  }
}

function mergeTransientSubAgents(
  sessions: SessionInfo[],
  transients: Map<string, TransientSubAgent>,
  now = Date.now(),
  pruneWhenPresent = true,
): { mainSessions: SessionInfo[]; agents: SessionInfo[] } {
  const mainByKey = new Map<string, SessionInfo>()
  const mainFallback = new Map<string, SessionInfo | null>()
  const agentByKey = new Map<string, SessionInfo>()
  for (const session of sessions.map(cloneSessionTree)) {
    indexMainSession(mainByKey, mainFallback, session)
    for (const agent of flattenTreeAgents([session])) indexAgent(agentByKey, agent)
  }

  for (const [key, entry] of transients) {
    if (now - entry.updatedAt > TRANSIENT_SUBAGENT_TTL_MS) {
      transients.delete(key)
      continue
    }
    if (findAgent(agentByKey, entry.session)) {
      if (!pruneWhenPresent) continue
      transients.delete(key)
      continue
    }
    attachOrphanAgent(cloneSessionTree(entry.session), mainByKey, mainFallback, agentByKey)
  }

  const mainSessions = [...mainByKey.values()]
  return { mainSessions, agents: flattenTreeAgents(mainSessions) }
}

function mergeRawSubAgentRows(base: RawChat[], extra: RawChat[]): RawChat[] {
  if (extra.length === 0) return base
  const result = [...base]
  const seen = new Set<string>()
  const keyFor = (row: RawChat) => `${row.channel || 'agent'}:${row.full_key || row.chat_id}`
  for (const row of result) seen.add(keyFor(row))
  for (const row of extra) {
    const key = keyFor(row)
    if (seen.has(key)) continue
    seen.add(key)
    result.push(row)
  }
  return result
}

function subAgentFromEvent(ev: SessionEvent, running: boolean, now = new Date().toISOString()): SessionInfo | null {
  const fullSessionKey = ev.session_key || ev.chat_id || ''
  const parsed = parseAgentChatID(fullSessionKey)
  const role = parsed?.role || ev.role
  if (!role) return null
  const instance = parsed?.instance ?? ev.instance ?? ''
  const parentChannel = parsed?.parentChannel || ev.channel || DEFAULT_CHANNEL
  const parentChatID = parsed?.parentChatID || ev.parent_id || ev.chat_id
  if (!parentChatID) return null
  const fullKey = parsed && fullSessionKey
    ? fullSessionKey
    : `${parentChannel}:${parentChatID}/${role}${instance ? `:${instance}` : ''}`
  return {
    chatID: fullKey,
    channel: 'agent',
    label: subAgentLabel('default', role, instance, fullKey),
    lastActive: now,
    preview: '',
    status: running ? 'running' : 'idle',
    isCurrent: false,
    type: 'agent',
    fullKey,
    role,
    instance,
    parentChannel,
    parentChatID,
    historical: false,
    agentChatID: fullKey,
    running,
    synthetic: false,
    children: [],
  }
}

function updateSessionTree(
  nodes: SessionInfo[],
  selector: SessionSelector,
  update: (session: SessionInfo) => SessionInfo,
  matches: (session: SessionInfo) => boolean = (session) => sameSession(session, selector),
  matchedUpdate: (session: SessionInfo) => SessionInfo = update,
): SessionInfo[] {
  let changed = false
  const next = nodes.map((node) => {
    let current = matches(node) ? matchedUpdate(node) : node
    if (current !== node) changed = true
    const children = current.children
    if (children?.length) {
      const nextChildren = updateSessionTree(children, selector, update, matches, matchedUpdate)
      if (nextChildren !== children) {
        current = { ...current, children: nextChildren }
        changed = true
      }
    }
    return current
  })
  return changed ? next : nodes
}

function subAgentLifecycleMatcher(
  role: string | undefined,
  instance: string | undefined,
  parentID: string | undefined,
  fullKey?: string,
) {
  return (s: SessionInfo) => {
    if (s.channel !== 'agent') return false
    if (fullKey) return s.chatID === fullKey || s.fullKey === fullKey || s.agentChatID === fullKey
    if (role && s.role !== role) return false
    if ((instance ?? '') && (s.instance ?? '') !== instance) return false
    if (parentID && s.parentChatID !== parentID && s.chatID !== parentID && s.fullKey !== parentID && s.agentChatID !== parentID) return false
    return true
  }
}

function markSubAgentLifecycle(nodes: SessionInfo[], role: string | undefined, instance: string | undefined, parentID: string | undefined, running: boolean, fullKey?: string): SessionInfo[] {
  const matches = subAgentLifecycleMatcher(role, instance, parentID, fullKey)
  return updateSessionTree(
    nodes,
    { channel: 'agent', chatID: '' },
    (s) => s,
    matches,
    (s) => ({
      ...s,
      running,
      status: running ? 'running' : 'idle',
      lastActive: new Date().toISOString(),
    }),
  )
}

/** Remove SubAgent nodes matching the lifecycle matcher from the tree.
 * subagent_stopped(removed=true) means the backend cascade-deleted the tenant
 * (TTL eviction / unload / spawn-failure cleanup) — the row must not linger as
 * a stale "idle" entry until the next tree refresh. */
function removeSubAgentNodes(nodes: SessionInfo[], matches: (s: SessionInfo) => boolean): SessionInfo[] {
  let changed = false
  const next: SessionInfo[] = []
  for (const node of nodes) {
    if (matches(node)) {
      changed = true
      continue
    }
    const children = node.children
    if (children?.length) {
      const pruned = removeSubAgentNodes(children, matches)
      if (pruned !== children) {
        changed = true
        next.push({ ...node, children: pruned.length ? pruned : undefined })
        continue
      }
    }
    next.push(node)
  }
  return changed ? next : nodes
}

// ⚠️ SESSION-PANEL GLOBAL-STATE BAN (eslint.config.js): per-session code
// (useProgressStream / useChatMessages / components/agent) MUST NOT use
// window.dispatchEvent / addEventListener / removeEventListener — route
// cross-session signals through src/lib/sessionEvents.ts (enforces the
// session identity at the type level). Root cause: useProgressStream
// dispatched IDENTITY-LESS agent-idle events (PhaseDone's inner payload
// carries no chat_id) and useSessionStore's listener fell back to "clear
// the ACTIVE session" — cancelling a background session idled the busy
// session the user was viewing (user report: "cancel 一个 session 导致
// 所有 busy 的 session 状态异常").
function applyPersistedUnreadStatuses(
  nodes: SessionInfo[],
  unread: Set<string>,
  active: SessionSelector | null,
): SessionInfo[] {
  let changed = false
  const next = nodes.map((node) => {
    const children = node.children ? applyPersistedUnreadStatuses(node.children, unread, active) : node.children
    const shouldMark = unread.has(sessionKey(node)) && !sameSession(node, active) && !node.running && node.status === 'idle'
    const updated = shouldMark ? { ...node, status: 'unread' as const } : node
    if (updated !== node || children !== node.children) changed = true
    return children === node.children && updated === node ? node : { ...updated, children }
  })
  return changed ? next : nodes
}

export function useSessionStoreImpl(): SessionStore {
  const ws = useWSConnection()
  // Hold ws in a ref — its methods delegate to a stable MultiSSEManager,
  // so we don't need ws in the effect deps. Including ws would cause
  // handler re-registration on every connection state change.
  const wsRef = useRef(ws)
  wsRef.current = ws
  const [cachedTree] = useState(loadSessionTreeCache)
  const [sessions, setSessions] = useState<SessionInfo[]>(() => cachedTree?.sessions ?? [])
  const [subAgents, setSubAgents] = useState<SessionInfo[]>(() => cachedTree?.subAgents ?? [])
  const [activeSession, setActiveSession] = useState<SessionSelector | null>(() => {
    const active = cachedTree?.sessions.find((session) => session.isCurrent && !session.synthetic)
      ?? cachedTree?.sessions.find((session) => !session.synthetic)
    return active ? { channel: active.channel, chatID: active.chatID } : null
  })
  const [starredIds, setStarredIds] = useState<string[]>(loadStarred)
  const [category, setCategoryState] = useState<SessionCategory>(loadCategory)
  const [unreadIds, setUnreadIds] = useState<string[]>(loadUnread)
  const unreadIdsRef = useRef(unreadIds)
  unreadIdsRef.current = unreadIds
  const [activeChannel, setActiveChannelState] = useState<string | null>(loadActiveChannel)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Backend pagination: whether more web user_chats exist beyond the loaded
  // window, and the offset to request for the next page.
  const [hasMore, setHasMore] = useState(false)
  const nextOffsetRef = useRef(0)
  // AskUser prompts keyed by "channel:chatID" — survives session switch.
  const [askUserPrompts, setAskUserPrompts] = useState<Map<string, AskUserPrompt>>(new Map())

  // Re-read starred/category from localStorage when server sync updates values.
  useEffect(() => {
    const handler = () => {
      setStarredIds(loadStarred())
      setCategoryState(loadCategory())
    }
    window.addEventListener(SETTINGS_SYNCED_EVENT, handler)
    return () => window.removeEventListener(SETTINGS_SYNCED_EVENT, handler)
  }, [])

  // Keep the latest session list available to SSE handlers without re-binding.
  const sessionsRef = useRef(sessions)
  sessionsRef.current = sessions
  const activeSessionRef = useRef(activeSession)
  activeSessionRef.current = activeSession
  const refreshSeqRef = useRef(0)
  const loadMoreSeqRef = useRef(0)
  const switchSeqRef = useRef(0)
  const subAgentRefreshTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const transientSubAgentsRef = useRef(new Map<string, TransientSubAgent>())
  // SSE intents per session key ("channel:chatID"): the LAST session event
  // (busy/idle) with its timestamp. Intents are the ONLY frontend-held overlay
  // and are a bounded 15s race-window hint — NEVER a source of truth: HTTP
  // (session-tree) is the persistent authority, and the unconditional 30s poll
  // guarantees convergence even when every SSE event is missed. A FRESH intent
  // overrides a contradicting HTTP response (the tree RPC lags SSE by up to one
  // round-trip); an expired (or absent) intent means HTTP ALWAYS wins.
  //
  // This replaces the previous busy-only map which had two deadlock modes:
  // (1) a lost idle left busy forever (no HTTP correction — the original
  //     "明明 idle 却显示 busy");
  // (2) — worse — "no busy key + carried idle" suppressed HTTP running FOREVER
  //     (mergeStatus couldn't distinguish "SSE idle just arrived" from "never
  //     saw busy / backend-restart race"), so a session that auto-resumed
  //     after a backend restart showed idle while actually running (user
  //     report: "明明 busy 侧边栏却显示 idle"). The timestamped IDLE intent
  //     closes both: idle is only trusted within the same window, and the
  //     unconditional poll gives HTTP the last word.
  const sseIntentsRef = useRef(new Map<string, { ts: number; busy: boolean }>())

  const refresh = useCallback(async () => {
    const seq = ++refreshSeqRef.current
    const initialLoad = sessionsRef.current.length === 0
    if (initialLoad) setLoading(true)
    setError(null)
    try {
      // Refresh PRESERVES the already-loaded window: fetch from offset 0 up to
      // the currently-loaded offset in one request (limit = loaded count, min
      // one page). SSE events (subagent_stopped / created) and CRUD callbacks
      // invoke refresh frequently; if refresh reset to page 1, a single SSE
      // event would snap a user who had scrolled 5 pages back to the top and
      // drop the loaded pages. With limit = loaded count, the loaded pages
      // survive and new sessions appear at their sorted position.
      const limit = Math.max(SESSION_TREE_PAGE_SIZE, nextOffsetRef.current)
      const data = await postAPI<ListSessionTreeResponse>('/api/session-tree', {
        offset: 0,
        limit,
      })
      if (seq !== refreshSeqRef.current) return
      setHasMore(data.has_more ?? false)
      if (typeof data.next_offset === 'number') nextOffsetRef.current = data.next_offset
      const normalized = normalizeCanonicalSessionTree(data.sessions || [], data.orphan_subagents || [])
      const { mainSessions } = mergeTransientSubAgents(normalized.mainSessions, transientSubAgentsRef.current)
      const { sessions: markedSessions, active } = reconcileActiveSession(mainSessions, activeSessionRef.current)
      const withUnread = applyPersistedUnreadStatuses(markedSessions, new Set(unreadIdsRef.current), active)
      const cachedSessions = mergeStatus(sessionsRef.current, withUnread, sseIntentsRef.current, Date.now())
      sessionsRef.current = cachedSessions
      const cachedAgents = flattenTreeAgents(cachedSessions)
      saveSessionTreeCache(cachedSessions, cachedAgents)
      setSessions((prev) => (sameSessionList(prev, cachedSessions) ? prev : cachedSessions))
      setSubAgents((prev) => (sameSessionList(prev, cachedAgents) ? prev : cachedAgents))
      if (active) setActiveSession(active)
    } catch (e) {
      if (seq !== refreshSeqRef.current) return
      setError(e instanceof Error ? e.message : 'network error')
    } finally {
      if (seq === refreshSeqRef.current && initialLoad) setLoading(false)
    }
  }, [])

  const loadMore = useCallback(async () => {
    // loadMore uses its own sequence counter so it is NOT cancelled by a
    // concurrent refresh (SSE events trigger refresh frequently). Previously
    // they shared refreshSeqRef, so an SSE event mid-loadMore would cancel
    // the load (seq mismatch → early return → loaded page discarded).
    const seq = ++loadMoreSeqRef.current
    setError(null)
    try {
      const offset = nextOffsetRef.current
      const data = await postAPI<ListSessionTreeResponse>('/api/session-tree', {
        offset,
        limit: SESSION_TREE_PAGE_SIZE,
      })
      if (seq !== loadMoreSeqRef.current) return
      setHasMore(data.has_more ?? false)
      if (typeof data.next_offset === 'number') nextOffsetRef.current = data.next_offset
      const normalized = normalizeCanonicalSessionTree(data.sessions || [], data.orphan_subagents || [])
      const { mainSessions } = mergeTransientSubAgents(normalized.mainSessions, transientSubAgentsRef.current)
      const withUnread = applyPersistedUnreadStatuses(mainSessions, new Set(unreadIdsRef.current), activeSessionRef.current)
      // Append the new page to the existing list (dedup by session key), then
      // carry over live status from the existing list.
      // CRITICAL: loadMore must NOT call reconcileActiveSession or setActiveSession —
      // the active session is on page 1 and is NOT in the incoming page. Calling
      // reconcileActiveSession on the incoming page would pick the first session of
      // the new page as the new active, switching the user's session.
      //
      // mergeStatus(prev, next) returns next.map(apply) — it only keeps sessions
      // in `next`. So `next` MUST be the full merged list (old + new), not just
      // the incoming page. Passing `withUnread` (new page only) as `next` would
      // discard all existing sessions.
      const appended = appendUniqueSessions(sessionsRef.current, withUnread)
      const merged = mergeStatus(sessionsRef.current, appended, sseIntentsRef.current, Date.now())
      sessionsRef.current = merged
      const cachedAgents = flattenTreeAgents(merged)
      saveSessionTreeCache(merged, cachedAgents)
      setSessions((prev) => (sameSessionList(prev, merged) ? prev : merged))
      setSubAgents((prev) => (sameSessionList(prev, cachedAgents) ? prev : cachedAgents))
    } catch (e) {
      if (seq !== loadMoreSeqRef.current) return
      setError(e instanceof Error ? e.message : 'network error')
    }
  }, [])

  /* Preserve live status across refresh: a fresh fetch resets every row to
   * 'idle', so carry over the inferred status keyed by chatID.
   *
   * Running status resolution — HTTP is the persistent authority; SSE intents
   * are a bounded 15s race-window overlay, NEVER a source of truth:
   *   1. Fresh intent (busy) + HTTP idle  → running: the busy event beat the
   *      in-flight tree response (chatCancelCh lags SSE by up to one
   *      round-trip).
   *   2. Fresh intent (idle) + HTTP running → idle: the idle event beat the
   *      in-flight response (same lag in reverse). TIME-BOUNDED — the old
   *      unbounded rule ("no busy key + carried idle → idle") suppressed HTTP
   *      running FOREVER after a backend-restart race: the busy event was
   *      consumed before the race, so nothing ever contradicted the stale
   *      carried idle while the session kept running (user report: "明明 busy
   *      侧边栏却显示 idle" for an auto-resumed session).
   *   3. Expired / absent intent → HTTP ALWAYS wins. This is the core fix for
   *      "must be fully synced with the backend": a lost idle (busy intent
   *      goes stale) self-corrects on the next refresh; a backend-restart
   *      resume (no intent at all — the busy event was missed) converges to
   *      HTTP running; a restart race (carried idle + HTTP running) converges
   *      to running instead of deadlocking.
   * The 30s unconditional poll guarantees HTTP gets the last word even when
   * every SSE event is missed.
   */
  function mergeStatus(prev: SessionInfo[], next: SessionInfo[], intents: Map<string, { ts: number; busy: boolean }>, now: number): SessionInfo[] {
    if (prev.length === 0) return next
    const statusBy = new Map<string, Pick<SessionInfo, 'status' | 'running'>>()
    const collect = (nodes: SessionInfo[]) => {
      for (const node of nodes) {
        statusBy.set(sessionKey(node), { status: node.status, running: node.running })
        collect(node.children || [])
      }
    }
    const apply = (node: SessionInfo): SessionInfo => {
      const carried = statusBy.get(sessionKey(node))
      const children = node.children?.map(apply)
      if (!carried) return { ...node, children }
      // waiting_input / error / unread are set by SSE events (ask_user, etc.)
      // and must survive refresh — HTTP doesn't know about these states.
      if (carried.status === 'waiting_input' || carried.status === 'error' || carried.status === 'unread') {
        return { ...node, status: carried.status, running: false, children }
      }
      const key = sessionKey(node)
      const intent = intents.get(key)
      const fresh = intent !== undefined && now - intent.ts <= EXECUTING_TRUST_WINDOW_MS
      if (fresh && intent.busy && !node.running) {
        // Case 1: the busy event beat the in-flight tree response (chatCancelCh
        // registers before the busy emit, but the RPC may have been answered
        // before the turn's chatCancelCh registration reached it).
        return { ...node, status: 'running', running: true, children }
      }
      if (fresh && !intent.busy && node.running) {
        // Case 2: the idle event beat the in-flight tree response. Bounded by
        // the window — past it, HTTP wins (the RPC has caught up).
        return { ...node, status: 'idle', running: false, children }
      }
      // Case 3 (no intent / expired / agreeing): HTTP authority. This is the
      // restart-race fix: a session auto-resumed by the backend (busy event
      // missed during the reconnect window) has NO intent — HTTP running shows
      // running regardless of a stale carried idle. The old
      // "busySince===undefined && carried.status==='idle' → idle" rule
      // deadlocked exactly here forever.
      return { ...node, children }
    }
    collect(prev)
    const merged = next.map(apply)
    return sameSessionList(prev, merged) ? prev : merged
  }

  function reconcileActiveSession(
    rows: SessionInfo[],
    current: SessionSelector | null,
  ): { sessions: SessionInfo[]; active: SessionSelector | null } {
    const selectableRows = rows.filter((s) => !s.synthetic)
    const chosen = current && selectableRows.some((s) => sameSession(s, current))
      ? current
      : selectableRows.find((s) => s.isCurrent) ?? selectableRows[0] ?? null
    const active = chosen ? { channel: chosen.channel || DEFAULT_CHANNEL, chatID: chosen.chatID } : null
    return {
      sessions: active
        ? rows.map((s) => ({ ...s, isCurrent: sameSession(s, active) }))
        : rows,
      active,
    }
  }

  const toggleStar = useCallback((id: string) => {
    setStarredIds((prev) => {
      const next = prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]
      persistStarred(next)
      return next
    })
  }, [])

  const setCategory = useCallback((c: SessionCategory) => {
    persistCategory(c)
    setCategoryState(c)
  }, [])

  const setActiveChannel = useCallback((channel: string | null) => {
    persistActiveChannel(channel)
    setActiveChannelState(channel)
  }, [])

  const markRead = useCallback((key: string) => {
    setUnreadIds((prev) => {
      if (!prev.includes(key)) return prev
      const next = prev.filter((x) => x !== key)
      persistUnread(next)
      return next
    })
  }, [])

  const addUnread = useCallback((key: string) => {
    setUnreadIds((prev) => {
      if (prev.includes(key)) return prev
      const next = [...prev, key]
      persistUnread(next)
      return next
    })
  }, [])

  const setStatus = useCallback((selector: SessionSelector, status: SessionStatus) => {
    const running = status === 'running' || status === 'pending'
    setSessions((prev) => {
      const next = updateSessionTree(prev, selector, (s) => ({ ...s, status, running }))
      // Keep sessionsRef in sync so mergeStatus in refresh() sees the latest
      // SSE-driven status (e.g. 'idle') instead of a stale 'running'.
      sessionsRef.current = next
      return next
    })
  }, [])

  const applySubAgentLifecycle = useCallback((ev: SessionEvent, running: boolean) => {
    if (!ev.role && !parseAgentChatID(ev.chat_id || '')) return
    const created = subAgentFromEvent(ev, running)
    const createdKey = created ? sessionKey(created) : ''
    if (created) {
      if (running) {
        transientSubAgentsRef.current.set(createdKey, { session: created, updatedAt: Date.now() })
        // Record the busy INTENT so mergeStatus trusts the SSE-driven running
        // only for EXECUTING_TRUST_WINDOW_MS — a missed subagent_stopped (route
        // gap / ring eviction) previously left the row running forever with no
        // HTTP correction path ("subagent 被卸载了却还显示"). HTTP
        // (IsProcessingByChannel reads interactiveSubAgents running state for
        // agent sessions — accurate) wins after the window.
        sseIntentsRef.current.set(createdKey, { ts: Date.now(), busy: true })
      } else if (ev.removed) {
        // Destroyed (TTL eviction / unload / spawn-failure cleanup): the DB
        // tenant is cascade-deleted — drop the transient row immediately
        // instead of parking it idle for the transient TTL (10min), which
        // resurrected destroyed rows on every mergeTransientSubAgents pass.
        transientSubAgentsRef.current.delete(createdKey)
        sseIntentsRef.current.delete(createdKey)
      } else {
        // Don't delete from transient map immediately — the backend may not
        // have persisted the agent tenant yet. Let mergeTransientSubAgents'
        // TTL (10min) handle cleanup. The refresh() after 500ms will pick up
        // the persisted row from the DB, and markSubAgentLifecycle will set
        // it to idle. This prevents the subagent from disappearing between
        // subagent_stopped and DB persistence.
        transientSubAgentsRef.current.set(createdKey, { session: { ...created, running: false, status: 'idle' }, updatedAt: Date.now() })
        sseIntentsRef.current.set(createdKey, { ts: Date.now(), busy: false })
      }
    }
    const merged = mergeTransientSubAgents(sessionsRef.current, transientSubAgentsRef.current, Date.now(), false)
    let mainSessions: SessionInfo[]
    if (!running && ev.removed) {
      // Destroyed: remove the node from the tree outright — the backend
      // cascade-deleted the tenant, so the row must not linger as idle until
      // the next tree refresh.
      mainSessions = removeSubAgentNodes(
        merged.mainSessions,
        subAgentLifecycleMatcher(ev.role, ev.instance, ev.parent_id || ev.chat_id, ev.session_key),
      )
    } else {
      mainSessions = running
        ? markSubAgentLifecycle(merged.mainSessions, ev.role, ev.instance, ev.parent_id || ev.chat_id, true, ev.session_key)
        : markSubAgentLifecycle(merged.mainSessions, ev.role, ev.instance, ev.parent_id || ev.chat_id, false, ev.session_key)
    }
    const agents = flattenTreeAgents(mainSessions)
    sessionsRef.current = mainSessions
    setSessions((prev) => (sameSessionList(prev, mainSessions) ? prev : mainSessions))
    setSubAgents((prev) => (sameSessionList(prev, agents) ? prev : agents))
  }, [])

  const createSession = useCallback(
    async (label?: string, workPath?: string, model?: string, subscriptionId?: string): Promise<string | null> => {
      let chatID: string
      let appliedWorkDir: string | undefined
      try {
        // Default the new session's model to the current active session's model —
        // new sessions inherit the (subscription, model) pair the user is currently
        // using. Model-subscription integration: the pair travels together; an
        // explicit model param (with its subscriptionId) wins; when neither is
        // available the backend falls back to the Balance tier model.
        let effectiveModel = model ?? ''
        let effectiveSubID = subscriptionId ?? ''
        if (!effectiveModel && activeSessionRef.current) {
          const cur = activeSessionRef.current
          try {
            const ctx = await getContextUsage(wsRef.current, cur.channel, cur.chatID)
            if (ctx.model) {
              effectiveModel = ctx.model
              effectiveSubID = ctx.subscription_id ?? ''
            }
          } catch {
            // Non-fatal — the backend falls back to the Balance tier model.
          }
        }
        const data = await postAPI<CreateChatResponse>('/api/chats/create', { label: label ?? '', model: effectiveModel, subscription_id: effectiveSubID })
        if (!data.chat_id) return null
        chatID = data.chat_id
      } catch {
        return null
      }
      if (workPath) {
        try {
          await setCwd({ channel: DEFAULT_CHANNEL, chatID }, workPath)
          rememberRecentWorkDir(workPath)
          appliedWorkDir = workPath
        } catch (e) {
          // Non-fatal: session was created, but CWD is the default.
          // Toast so the user knows their workPath didn't take effect.
          const msg = e instanceof Error ? e.message : 'unknown error'
          toast.error(`工作目录设置失败: ${msg}`)
        }
      }
      const selector = { channel: DEFAULT_CHANNEL, chatID }
      activeSessionRef.current = selector
      setActiveSession(selector)
      // Optimistic insert so the new session appears immediately; refresh reconciles.
      setSessions((prev) => [
        {
          chatID,
          channel: DEFAULT_CHANNEL,
          label: label || chatID,
          lastActive: new Date().toISOString(),
          preview: '',
          status: 'idle',
          isCurrent: true,
          workDir: appliedWorkDir,
        },
        ...prev.map((s) => ({ ...s, isCurrent: false })),
      ])
      void refresh()
      return chatID
    },
    [refresh],
  )

  const forkSession = useCallback(
    async (sourceChatID: string, sourceChannel: string = DEFAULT_CHANNEL, label?: string): Promise<string | null> => {
      try {
        const data = await postAPI<{ chat_id: string }>('/api/chats/fork', {
          source_channel: sourceChannel,
          source_chat_id: sourceChatID,
          label: label ?? '',
        })
        if (!data.chat_id) return null
        const chatID = data.chat_id
        const selector = { channel: DEFAULT_CHANNEL, chatID }
        activeSessionRef.current = selector
        setActiveSession(selector)
        // Optimistic insert; refresh reconciles with the real label from DB.
        setSessions((prev) => [
          {
            chatID,
            channel: DEFAULT_CHANNEL,
            label: label || `${chatID.slice(0, 12)}…`,
            lastActive: new Date().toISOString(),
            preview: '',
            status: 'idle',
            isCurrent: true,
          },
          ...prev.map((s) => ({ ...s, isCurrent: false })),
        ])
        void refresh()
        return chatID
      } catch {
        return null
      }
    },
    [refresh],
  )

  const switchSession = useCallback(
    async (id: string, ch: string): Promise<void> => {
      const switchSeq = ++switchSeqRef.current
      const useChannel = ch || DEFAULT_CHANNEL
      // Clear the OLD session's caches so the new session loads fresh from the
      // server (like a page refresh). Without this, stale progress snapshots
      // and message caches from the previous session cause "iteration disappears"
      // on 50% of busy-session switches.
      if (activeSessionRef.current) {
        const oldCacheKey = sessionCacheKey(activeSessionRef.current.channel, activeSessionRef.current.chatID)
        clearSessionCaches(oldCacheKey)
      }
      try {
        await postAPI<SwitchChatResponse>(
          `/api/chats/${encodeURIComponent(id)}/switch`,
          { channel: useChannel },
        )
      } catch {
        return
      }
      if (switchSeq !== switchSeqRef.current) return
      const selector = { channel: useChannel, chatID: id }
      activeSessionRef.current = selector
      setActiveSession(selector)
      markRead(sessionKey(selector))
      const nextSessions = markCurrentSession(sessionsRef.current, selector)
      sessionsRef.current = nextSessions
      saveSessionTreeCache(nextSessions, flattenTreeAgents(nextSessions))
      setSessions(nextSessions)
      // Intents are per-session keyed and 15s-bounded (self-expiring) — no clear
      // needed on switch. The old unbounded-Set clear guarded against stale busy
      // keys; with the intent window, HTTP wins past 15s and refresh() below
      // re-syncs from the server anyway. Clearing here would re-introduce the
      // switch race (clear + HTTP refresh caught before a re-registered
      // chatCancelCh → idle for a busy session).
      // No snapshot cache — todos are restored authoritatively by the session's
      // active_progress via reload. A cached snapshot can be stale (user report:
      // "全是缓存的错误").
      // Immediately query the server for the latest session status — the
      // local sessions list may be stale (e.g. a previous busy/idle event
      // failed to arrive). This ensures the sidebar and AgentPanel show the
      // correct running state right after switching.
      void refresh()
    },
    [markRead, refresh],
  )

  /**
   * Lightweight session activation for the tab-based desktop flow.
   *
   * Unlike `switchSession`, this does NOT clear session caches — each agent
   * tab keeps its own SSE/webCache state, so switching tabs should not wipe
   * another tab's progress cursor. Used when a dockview tab becomes active
   * (onDidActivePanelChange) to update the sidebar highlight + backend tracking.
   */
  const activateSession = useCallback(
    (id: string, ch: string): void => {
      const useChannel = ch || DEFAULT_CHANNEL
      const selector = { channel: useChannel, chatID: id }
      activeSessionRef.current = selector
      setActiveSession(selector)
      markRead(sessionKey(selector))
      const nextSessions = markCurrentSession(sessionsRef.current, selector)
      sessionsRef.current = nextSessions
      saveSessionTreeCache(nextSessions, flattenTreeAgents(nextSessions))
      setSessions(nextSessions)
      // Fire-and-forget backend switch (updates last_active_at).
      void postAPI(`/api/chats/${encodeURIComponent(id)}/switch`, { channel: useChannel }).catch(() => {})
      // Refresh session list to get fresh running status — without this, the
      // sessions array may carry stale `running` from localStorage, causing idle
      // sessions to show as busy after page refresh (activateSession is called
      // by onDidActivePanelChange during layout restoration, before the initial
      // refresh() has completed).
      void refresh()
    },
    [markRead, refresh],
  )

  const renameSession = useCallback(async (id: string, channel: string, label: string): Promise<boolean> => {
    try {
      await postAPI(`/api/chats/${encodeURIComponent(id)}/rename`, { channel, label })
    } catch {
      return false
    }
    setSessions((prev) => prev.map((s) => (sameSession(s, { channel, chatID: id }) ? { ...s, label } : s)))
    void refresh()
    return true
  }, [refresh])

  const reorderSessions = useCallback(async (channel: string, orderedIDs: string[]): Promise<boolean> => {
    const orders: Record<string, number> = {}
    orderedIDs.forEach((id, i) => { orders[id] = i + 1 })
    try {
      await postAPI('/api/chats/reorder', { channel, orders })
    } catch {
      return false
    }
    // Optimistically update sortOrder in local state.
    setSessions((prev) => prev.map((s) => {
      const idx = orderedIDs.indexOf(s.chatID)
      return idx >= 0 ? { ...s, sortOrder: idx + 1 } : s
    }))
    return true
  }, [])

  const deleteSession = useCallback(
    async (id: string, channel: string): Promise<boolean> => {
      try {
        await postAPI(`/api/chats/${encodeURIComponent(id)}/delete`, { channel })
      } catch {
        return false
      }
      const selector = { channel, chatID: id }
      clearSessionCaches(sessionCacheKey(channel, id))
      const deleted = sessionsRef.current.find((s) => sameSession(s, selector))
      setSessions((prev) => prev.filter((s) => !sameSession(s, selector)))
      markRead(sessionKey(selector))
      setStarredIds((prev) => {
        const key = deleted ? sessionKey(deleted) : id
        if (!prev.includes(key)) return prev
        const next = prev.filter((x) => x !== key)
        persistStarred(next)
        return next
      })
      if (sameSession(activeSession, selector)) {
        setActiveSession(null)
      }
      void refresh()
      return true
    },
    [activeSession, refresh, markRead],
  )

  /* ── SSE-driven status inference ── */

  // session events: busy → running, idle → idle, deleted → remove, renamed → label
  useEffect(() => {
    return wsRef.current.onSession((ev) => {
      const chatID = ev.chat_id
      if (!chatID) return
      const selector = { channel: ev.channel || DEFAULT_CHANNEL, chatID }
      // SubAgent session events only trigger a refresh of the Web-only
      // canonical tree. Web creates a transient child row first so short-lived
      // one-shot agents do not disappear before the backend tree refresh lands.
      if (ev.action === 'subagent_started' || ev.action === 'subagent_stopped') {
        applySubAgentLifecycle(ev, ev.action === 'subagent_started')
        if (subAgentRefreshTimerRef.current) clearTimeout(subAgentRefreshTimerRef.current)
        subAgentRefreshTimerRef.current = setTimeout(() => {
          subAgentRefreshTimerRef.current = null
          void refresh()
        }, 500)
        return
      }
      switch (ev.action) {
        case 'busy':
          sseIntentsRef.current.set(sessionKey(selector), { ts: Date.now(), busy: true })
          setStatus(selector, 'running')
          break
        case 'idle': {
          const key = sessionKey(selector)
          const wasExecuting = sseIntentsRef.current.get(key)?.busy === true
          // Record the idle INTENT (not a delete): the 15s window lets it beat a
          // lagging HTTP running response (chatCancelCh deregistration races the
          // tree RPC), and — critically — an idle timestamp (vs the old "no key
          // + carried idle" rule) can never suppress HTTP running FOREVER: a
          // backend-restart resume race leaves NO intent at all, and HTTP wins.
          sseIntentsRef.current.set(key, { ts: Date.now(), busy: false })
          if (wasExecuting && !sameSession(activeSessionRef.current, selector)) {
            setStatus(selector, 'unread')
            addUnread(key)
          } else {
            setStatus(selector, 'idle')
          }
          // Don't call refresh() here — it causes a race where the HTTP
          // response (which may be stale) overwrites the SSE-driven status.
          // TUI doesn't refresh on idle either; the sidebar state is
          // driven entirely by SSE events. refresh() runs on initial load
          // and session switch, which is sufficient for sync.
          break
        }
        case 'deleted':
          sseIntentsRef.current.delete(sessionKey(selector))
          markRead(sessionKey(selector))
          setSessions((prev) => prev.filter((s) => !sameSession(s, selector)))
          break
        case 'renamed':
          if (ev.label)
            setSessions((prev) =>
              prev.map((s) => (sameSession(s, selector) ? { ...s, label: ev.label! } : s)),
            )
          break
        case 'created':
          void refresh()
          break
        default:
          break
      }
    })
  }, [setStatus, applySubAgentLifecycle, addUnread, markRead])

  // PhaseDone arrives via SSE faster than session(idle) (which fires after
  // Run() fully exits). Listen for it to clear running state immediately.
  // The event carries chatID+channel so it can clear the correct session
  // even when the user has already switched to a different one.
  // IDENTITY IS MANDATORY: an agent-idle WITHOUT chatID is dropped — the old
  // "clear the active session" fallback was the cross-session pollution that
  // made cancelling session A idle busy session B (a background session's
  // PhaseDone payload carries no chat_id, the identity-less dispatch hit the
  // fallback, and the active session got a fresh idle intent that beat HTTP
  // running for 15s — "cancel one session breaks ALL busy sessions").
  // Per-session panels dispatch via sessionEvents.ts, which enforces the
  // identity; dispatchAgentIdle logs+drops empty chatIDs.
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent).detail as { chatID?: string; channel?: string } | undefined
      if (!detail?.chatID) return
      const selector = { channel: detail.channel || DEFAULT_CHANNEL, chatID: detail.chatID }
      sseIntentsRef.current.set(sessionKey(selector), { ts: Date.now(), busy: false })
      setStatus(selector, 'idle')
    }
    window.addEventListener('agent-idle', handler)
    return () => window.removeEventListener('agent-idle', handler)
  }, [setStatus])

  // ── Sidebar state reconciliation ──────────────────────────────────────────
  // sessions-resync is dispatched by the SSE layer when events were PROVABLY
  // lost (reconnect, resync_required ring eviction, active-progress recovery
  // gap). Do NOT clear the intents here — a busy event replayed by catch-up
  // just re-armed the 15s window, and clearing it would re-introduce the
  // backend-restart race (clear + HTTP refresh answered before the resumed
  // turn registered chatCancelCh → idle; the one-shot busy event was already
  // consumed → idle forever). The 15s window self-heals stale intents and the
  // unconditional 30s poll converges to HTTP regardless.
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null
    const handler = () => {
      // Debounce: several SSE connections (dockview tabs) can dispatch
      // sessions-resync within the same reconnect window — one refresh covers all.
      if (timer) return
      timer = setTimeout(() => {
        timer = null
        void refresh()
      }, 300)
    }
    window.addEventListener('sessions-resync', handler)
    return () => {
      window.removeEventListener('sessions-resync', handler)
      if (timer) clearTimeout(timer)
    }
  }, [refresh])

  // Tab foregrounded: SSE was likely throttled/disconnected while hidden —
  // missed session events are the #1 sidebar-staleness source. Reconcile via
  // refresh (HTTP authority); the 15s intent window self-heals stale intents
  // (no clear — same restart-race reasoning as sessions-resync).
  useEffect(() => {
    const handler = () => {
      if (document.visibilityState !== 'visible') return
      void refresh()
    }
    document.addEventListener('visibilitychange', handler)
    return () => document.removeEventListener('visibilitychange', handler)
  }, [refresh])

  // NOTE: no periodic poll — the sidebar is 100% event-driven (user's
  // requirement "必须100%实时"). busy/idle session events reach every web
  // client via the user-level broadcast (real-time), in-connection drops are
  // detected by the seq-gap → sessions-resync path in sseConnection (real-time,
  // event-driven correction, not polling), reconnect / resync_required /
  // visibilitychange trigger HTTP reconciliation, and mergeStatus converges to
  // HTTP authority once the 15s intent window expires. The old 30s poll was a
  // crutch for a Case-3 deadlock that no longer exists — removed.


  useEffect(() => {
    return () => {
      if (subAgentRefreshTimerRef.current) clearTimeout(subAgentRefreshTimerRef.current)
    }
  }, [])

  // ask_user → waiting_input + store prompt for the session.
  useEffect(() => {
    return wsRef.current.onMessage((msg) => {
      if (msg.type !== 'ask_user') return
      const explicitChatID = (msg as AskUserEnvelope).chat_id
      const fallback = activeSessionRef.current
      const chatID = explicitChatID ?? wsRef.current.chatID ?? fallback?.chatID
      const channel = msg.channel
        ?? (chatID === wsRef.current.chatID ? wsRef.current.channel : null)
        ?? (fallback && chatID === fallback.chatID ? fallback.channel : DEFAULT_CHANNEL)
      // [ASKDEBUG] 诊断：双面板 split view 下 ask_user 面板不渲染的定位日志
      //（复现后看 console：无此日志 = 连接层没送达 handler；有此日志但面板
      // miss = key 匹配问题 —— 与 useAskUser 的 [ASKDEBUG] miss 配对看）。
      // DEV-only（CR#9: PR 描述承诺"正常流程静默"——生产不打）。
      if (import.meta.env.DEV) {
        console.warn('[ASKDEBUG] ask_user received', {
          explicitChatID: explicitChatID ?? null,
          msgChannel: msg.channel ?? null,
          resolvedChatID: chatID,
          resolvedChannel: channel,
          resolvedKey: channel && chatID ? `${channel}:${chatID}` : null,
          primaryChatID: wsRef.current.chatID,
          questions: Array.isArray(msg.progress?.questions) ? msg.progress.questions.length : 0,
        })
      }
      if (chatID) {
        setStatus({ channel, chatID }, 'waiting_input')
        // Store the prompt so it survives session switch.
        const p = msg.progress
        const questions: AskUserQuestion[] = []
        if (p?.questions && Array.isArray(p.questions)) {
          for (const q of p.questions) {
            if (!q || typeof q !== 'object') continue
            const o = q as Record<string, unknown>
            const question = typeof o.question === 'string' ? o.question : ''
            const options = Array.isArray(o.options)
              ? o.options.filter((x): x is string => typeof x === 'string')
              : undefined
            // Only drop a question that has NEITHER text NOR options. A question
            // with no prompt text but real options (the LLM sometimes emits
            // `{allow_other:true, options:[...]}` without `question`) must still
            // survive so the panel renders the options — the title is skipped.
            if (!question && !(options && options.length > 0)) continue
            // Backend serializes AskUserQuestion as snake_case (multi_select /
            // allow_other, see protocol/events.go). Map to the frontend
            // camelCase fields so AskUserPanel renders multi-select checkboxes
            // and the "Other" toggle — without this they are always undefined.
            questions.push({
              question,
              options,
              multiSelect: o.multi_select === true,
              allowOther: o.allow_other === true,
            })
          }
        }
        const requestId = (p?.request_id as string | undefined) ?? msg.id ?? String(Date.now())
        const key = `${channel}:${chatID}`
        setAskUserPrompts((prev) => {
          const next = new Map(prev)
          next.set(key, { requestId, questions })
          return next
        })
      }
    })
  }, [setStatus])

  // Initial load.
  useEffect(() => {
    void refresh()
  }, [refresh])

  const sortedSessions = useMemo(() => sortSessions(sessions, starredIds), [sessions, starredIds])
  const clearAskUserPrompt = useCallback((channel: string, chatID: string) => {
    const key = `${channel}:${chatID}`
    setAskUserPrompts((prev) => {
      const next = new Map(prev)
      next.delete(key)
      return next
    })
  }, [])

  const groups = useMemo(() => groupSessions(sessions, category, starredIds), [sessions, category, starredIds])
  const activeSessionId = activeSession?.chatID ?? null

  return useMemo(() => ({
    sessions,
    groups,
    sortedSessions,
    activeSessionId,
    activeSession,
    starredIds,
    category,
    unreadIds,
    activeChannel,
    loading,
    error,
    subAgents,
    askUserPrompts,
    setCategory,
    setActiveChannel,
    markRead,
    setStatus,
    refresh,
    hasMore,
    loadMore,
    toggleStar,
    createSession,
    forkSession,
    switchSession,
    activateSession,
    renameSession,
    deleteSession,
    reorderSessions,
    clearAskUserPrompt,
  }), [sessions, groups, sortedSessions, activeSessionId, activeSession, starredIds, category, unreadIds, activeChannel, loading, error, subAgents,
    askUserPrompts, setCategory, setActiveChannel, markRead, setStatus, refresh, hasMore, loadMore, toggleStar, createSession, forkSession, switchSession, activateSession, renameSession, deleteSession, reorderSessions, clearAskUserPrompt])
}

function markCurrentSession(nodes: SessionInfo[], selector: SessionSelector): SessionInfo[] {
  return nodes.map((session) => {
    const current = sameSession(session, selector)
    return {
      ...session,
      isCurrent: current,
      status: current && session.status === 'unread' ? 'idle' : session.status,
      children: session.children ? markCurrentSession(session.children, selector) : session.children,
    }
  })
}

function sameSessionList(a: SessionInfo[], b: SessionInfo[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (!sameSessionNode(a[i], b[i])) return false
  }
  return true
}

/**
 * Append incoming sessions onto the existing list, deduplicating by sessionKey
 * (recursively into children). Existing nodes keep their object identity; only
 * genuinely-new incoming nodes are appended. Used by loadMore so the default
 * chat (returned on every page) and already-loaded sub-agents don't duplicate.
 */
function appendUniqueSessions(existing: SessionInfo[], incoming: SessionInfo[]): SessionInfo[] {
  const seen = new Set<string>()
  const mark = (node: SessionInfo) => {
    seen.add(sessionKey(node))
    for (const c of node.children || []) mark(c)
  }
  for (const n of existing) mark(n)

  const result = [...existing]
  const appendIfNew = (node: SessionInfo): SessionInfo | null => {
    const key = sessionKey(node)
    if (seen.has(key)) return null
    seen.add(key)
    const children = (node.children || [])
      .map(appendIfNew)
      .filter((c): c is SessionInfo => c !== null)
    return { ...node, children: children.length > 0 ? children : undefined }
  }
  for (const n of incoming) {
    const r = appendIfNew(n)
    if (r) result.push(r)
  }
  return result
}

function sameSessionNode(a: SessionInfo, b: SessionInfo): boolean {
  if (
    a.chatID !== b.chatID ||
    a.channel !== b.channel ||
    a.label !== b.label ||
    a.workDir !== b.workDir ||
    a.lastActive !== b.lastActive ||
    a.preview !== b.preview ||
    a.status !== b.status ||
    a.isCurrent !== b.isCurrent ||
    a.type !== b.type ||
    a.fullKey !== b.fullKey ||
    a.role !== b.role ||
    a.instance !== b.instance ||
    a.parentChatID !== b.parentChatID ||
    a.parentChannel !== b.parentChannel ||
    a.running !== b.running ||
    a.historical !== b.historical ||
    a.agentChatID !== b.agentChatID ||
    a.synthetic !== b.synthetic
  ) {
    return false
  }
  return sameSessionList(a.children || [], b.children || [])
}

/* ── Context singleton ── */

export const SessionStoreContext = createContext<SessionStore | undefined>(undefined)

export function SessionStoreProvider({ children }: { children: ReactNode }) {
  const store = useSessionStoreImpl()
  return createElement(SessionStoreContext.Provider, { value: store }, children)
}

export function useSessionStore(): SessionStore {
  const ctx = useContext(SessionStoreContext)
  if (!ctx) throw new Error('useSessionStore must be used within a <SessionStoreProvider>')
  return ctx
}
