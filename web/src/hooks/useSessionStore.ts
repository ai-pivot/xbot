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
import { setCwd } from '@/components/agent/api'
import { useWSConnection } from '@/hooks/useWSConnection'
import { postAPI } from '@/lib/api'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'
import { groupSessions, parseAgentChatID, sameSession, sessionKey, sortSessions } from '@/lib/session-grouping'
import { clearSessionCaches, loadSessionTreeCache, saveSessionTreeCache, sessionCacheKey } from '@/lib/webCache'
import { rememberRecentWorkDir } from '@/lib/recent-workdirs'
import type { SessionCategory, SessionEvent, SessionInfo, SessionSelector, SessionStatus } from '@/types/shared'
import type { AskUserPrompt, AskUserQuestion } from '@/types/agent'

const STARRED_KEY = 'xbot-starred'
const CATEGORY_KEY = 'xbot:session-category'
const UNREAD_KEY = 'xbot:session-unread'
const ACTIVE_CHANNEL_KEY = 'xbot:active-channel'
const DEFAULT_CHANNEL = 'web'
const TRANSIENT_SUBAGENT_TTL_MS = 10 * 60 * 1000

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
  refresh: () => Promise<void>
  toggleStar: (id: string) => void
  createSession: (label?: string, workPath?: string) => Promise<string | null>
  switchSession: (id: string, channel: string) => Promise<void>
  renameSession: (id: string, channel: string, label: string) => Promise<boolean>
  deleteSession: (id: string, channel: string) => Promise<boolean>
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
}
interface RawChat {
  chat_id: string
  channel?: string
  label: string
  work_dir?: string
  last_active: string
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

function removeSubAgentLifecycle(nodes: SessionInfo[], role: string | undefined, instance: string | undefined, parentID: string | undefined, fullKey?: string): SessionInfo[] {
  const matches = subAgentLifecycleMatcher(role, instance, parentID, fullKey)
  let changed = false
  const visit = (items: SessionInfo[]): SessionInfo[] => {
    const next: SessionInfo[] = []
    for (const item of items) {
      if (matches(item)) {
        changed = true
        continue
      }
      const children = item.children ? visit(item.children) : item.children
      if (children !== item.children) {
        changed = true
        next.push({ ...item, children })
      } else {
        next.push(item)
      }
    }
    return next
  }
  const next = visit(nodes)
  return changed ? next : nodes
}

function addRunningSessionKeys(nodes: SessionInfo[], target: Set<string>): void {
  for (const node of nodes) {
    if (node.running || node.status === 'running' || node.status === 'pending') {
      target.add(sessionKey(node))
    }
    addRunningSessionKeys(node.children || [], target)
  }
}

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
  const switchSeqRef = useRef(0)
  const subAgentRefreshTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const transientSubAgentsRef = useRef(new Map<string, TransientSubAgent>())
  const executingSessionsRef = useRef(new Set<string>())

  const refresh = useCallback(async () => {
    const seq = ++refreshSeqRef.current
    const initialLoad = sessionsRef.current.length === 0
    if (initialLoad) setLoading(true)
    setError(null)
    try {
      const data = await postAPI<ListSessionTreeResponse>('/api/session-tree')
      if (seq !== refreshSeqRef.current) return
      const normalized = normalizeCanonicalSessionTree(data.sessions || [], data.orphan_subagents || [])
      const { mainSessions, agents } = mergeTransientSubAgents(normalized.mainSessions, transientSubAgentsRef.current)
      if (initialLoad) addRunningSessionKeys(mainSessions, executingSessionsRef.current)
      const { sessions: markedSessions, active } = reconcileActiveSession(mainSessions, activeSessionRef.current)
      const withUnread = applyPersistedUnreadStatuses(markedSessions, new Set(unreadIdsRef.current), active)
      const cachedSessions = mergeStatus(sessionsRef.current, withUnread, executingSessionsRef.current)
      sessionsRef.current = cachedSessions
      saveSessionTreeCache(cachedSessions, agents)
      setSessions((prev) => (sameSessionList(prev, cachedSessions) ? prev : cachedSessions))
      setSubAgents((prev) => (sameSessionList(prev, agents) ? prev : agents))
      if (active) setActiveSession(active)
    } catch (e) {
      if (seq !== refreshSeqRef.current) return
      setError(e instanceof Error ? e.message : 'network error')
    } finally {
      if (seq === refreshSeqRef.current && initialLoad) setLoading(false)
    }
  }, [])

  /* Preserve live status across refresh: a fresh fetch resets every row to
   * 'idle', so carry over the inferred status keyed by chatID.
   *
   * Running status is resolved from `executingSessionsRef` — the authoritative
   * set of sessions that received SSE session(busy) without a matching
   * session(idle). This avoids two bugs:
   *   1. Stale idle carry-over: session goes busy (HTTP says running) but prev
   *      still has idle → mergeStatus wrongly carried over idle.
   *   2. Stale HTTP running: session just went idle (SSE idle fired, key removed
   *      from executingSessionsRef) but HTTP still says running → we trust
   *      executingSessionsRef (not running) over the stale HTTP response.
   * For the brief window where HTTP is stale after idle, the next refresh
   * cycle (≤2s) corrects it — much better than being stuck on idle when busy.
   */
  function mergeStatus(prev: SessionInfo[], next: SessionInfo[], executingKeys: Set<string>): SessionInfo[] {
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
      // Running status: executingSessionsRef is authoritative.
      // If SSE busy was received (key in set), force running.
      // If SSE idle was received (key not in set), don't carry over a stale
      // idle from prev — trust the HTTP response which may now say running
      // (the session went busy after the last SSE idle).
      if (executingKeys.has(sessionKey(node))) {
        return { ...node, status: 'running', running: true, children }
      }
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
    if (created) {
      if (running) {
        transientSubAgentsRef.current.set(sessionKey(created), { session: created, updatedAt: Date.now() })
      } else {
        transientSubAgentsRef.current.delete(sessionKey(created))
      }
    }
    const merged = mergeTransientSubAgents(sessionsRef.current, transientSubAgentsRef.current, Date.now(), false)
    const mainSessions = running
      ? markSubAgentLifecycle(merged.mainSessions, ev.role, ev.instance, ev.parent_id || ev.chat_id, true, ev.session_key)
      : removeSubAgentLifecycle(merged.mainSessions, ev.role, ev.instance, ev.parent_id || ev.chat_id, ev.session_key)
    const agents = flattenTreeAgents(mainSessions)
    sessionsRef.current = mainSessions
    setSessions((prev) => (sameSessionList(prev, mainSessions) ? prev : mainSessions))
    setSubAgents((prev) => (sameSessionList(prev, agents) ? prev : agents))
  }, [])

  const createSession = useCallback(
    async (label?: string, workPath?: string): Promise<string | null> => {
      let chatID: string
      let appliedWorkDir: string | undefined
      try {
        const data = await postAPI<CreateChatResponse>('/api/chats/create', { label: label ?? '' })
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
      // Clear ALL executing sessions — stale busy keys (from lost idle events)
      // would force running in mergeStatus, overriding the server's correct state.
      executingSessionsRef.current.clear()
      // Immediately query the server for the latest session status — the
      // local sessions list may be stale (e.g. a previous busy/idle event
      // failed to arrive). This ensures the sidebar and AgentPanel show the
      // correct running state right after switching.
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
    return ws.onSession((ev) => {
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
          executingSessionsRef.current.add(sessionKey(selector))
          setStatus(selector, 'running')
          break
        case 'idle': {
          const wasExecuting = executingSessionsRef.current.delete(sessionKey(selector))
          if (wasExecuting && !sameSession(activeSessionRef.current, selector)) {
            setStatus(selector, 'unread')
            addUnread(sessionKey(selector))
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
          executingSessionsRef.current.delete(sessionKey(selector))
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
  }, [ws, setStatus, applySubAgentLifecycle, addUnread, markRead])

  // PhaseDone arrives via SSE faster than session(idle) (which fires after
  // Run() fully exits). Listen for it to clear running state immediately.
  useEffect(() => {
    const handler = () => {
      const active = activeSessionRef.current
      if (!active) return
      const selector = { channel: active.channel || DEFAULT_CHANNEL, chatID: active.chatID }
      executingSessionsRef.current.delete(sessionKey(selector))
      setStatus(selector, 'idle')
    }
    window.addEventListener('agent-idle', handler)
    return () => window.removeEventListener('agent-idle', handler)
  }, [setStatus])

  useEffect(() => {
    return () => {
      if (subAgentRefreshTimerRef.current) clearTimeout(subAgentRefreshTimerRef.current)
    }
  }, [])

  // ask_user → waiting_input + store prompt for the session.
  useEffect(() => {
    return ws.onMessage((msg) => {
      if (msg.type !== 'ask_user') return
      const explicitChatID = (msg as AskUserEnvelope).chat_id
      const fallback = activeSessionRef.current
      const chatID = explicitChatID ?? ws.chatID ?? fallback?.chatID
      const channel = msg.channel
        ?? (chatID === ws.chatID ? ws.channel : null)
        ?? (fallback && chatID === fallback.chatID ? fallback.channel : DEFAULT_CHANNEL)
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
            if (!question) continue
            const options = Array.isArray(o.options)
              ? o.options.filter((x): x is string => typeof x === 'string')
              : undefined
            questions.push({ question, options })
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
  }, [ws, setStatus])

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
    refresh,
    toggleStar,
    createSession,
    switchSession,
    renameSession,
    deleteSession,
    clearAskUserPrompt,
  }), [sessions, groups, sortedSessions, activeSessionId, activeSession, starredIds, category, unreadIds, activeChannel, loading, error, subAgents,
    askUserPrompts, setCategory, setActiveChannel, markRead, refresh, toggleStar, createSession, switchSession, renameSession, deleteSession, clearAskUserPrompt])
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
