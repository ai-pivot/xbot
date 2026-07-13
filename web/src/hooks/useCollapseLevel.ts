/**
 * useCollapseLevel — reads/writes the Agent intermediate-process collapse
 * preference (Spec A §4).
 *
 * Uses useSyncExternalStore for same-window synchronisation: when the
 * settings panel changes the level, every component instance that calls
 * useCollapseLevel re-renders immediately. Cross-window sync is handled
 * by the storage event listener which updates the same global store.
 *
 * Also exports useMergeTools — an orthogonal toggle (Spec A §3.1) stored at
 * localStorage key `xbot-merge-tools` (default: true).
 */
import { useCallback, useSyncExternalStore } from 'react'

import {
  COLLAPSE_LEVELS,
  COLLAPSE_LEVEL_STORAGE_KEY,
  DEFAULT_COLLAPSE_LEVEL,
  DEFAULT_MERGE_TOOLS,
  MERGE_TOOLS_STORAGE_KEY,
  type CollapseLevel,
} from '@/types/agent'

// ── Global store for collapse level (useSyncExternalStore compatible) ──────

function readStoredLevel(): CollapseLevel {
  try {
    const v = localStorage.getItem(COLLAPSE_LEVEL_STORAGE_KEY)
    if (v && (COLLAPSE_LEVELS as string[]).includes(v)) return v as CollapseLevel
  } catch {
    /* ignore */
  }
  return DEFAULT_COLLAPSE_LEVEL
}

function readStoredMergeTools(): boolean {
  try {
    const v = localStorage.getItem(MERGE_TOOLS_STORAGE_KEY)
    if (v === 'false') return false
    if (v === 'true') return true
  } catch {
    /* ignore */
  }
  return DEFAULT_MERGE_TOOLS
}

type Listener = () => void

const collapseListeners = new Set<Listener>()
let cachedLevel: CollapseLevel = readStoredLevel()

const mergeListeners = new Set<Listener>()
let cachedMergeTools: boolean = readStoredMergeTools()

function notifyCollapse() {
  cachedLevel = readStoredLevel()
  collapseListeners.forEach((l) => l())
}

function notifyMerge() {
  cachedMergeTools = readStoredMergeTools()
  mergeListeners.forEach((l) => l())
}

// Cross-window sync via storage event
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e: StorageEvent) => {
    if (e.key === COLLAPSE_LEVEL_STORAGE_KEY) notifyCollapse()
    if (e.key === MERGE_TOOLS_STORAGE_KEY) notifyMerge()
  })
}

function subscribeCollapse(listener: Listener): () => void {
  collapseListeners.add(listener)
  return () => collapseListeners.delete(listener)
}

function getSnapshotLevel(): CollapseLevel {
  return cachedLevel
}

function subscribeMerge(listener: Listener): () => void {
  mergeListeners.add(listener)
  return () => mergeListeners.delete(listener)
}

function getSnapshotMergeTools(): boolean {
  return cachedMergeTools
}

// ── Types ──────────────────────────────────────────────────────────────────


export type BlockType = 'reasoning' | 'tool' | 'text' | 'iteration'

export interface UseCollapseLevelResult {
  level: CollapseLevel
  setLevel: (level: CollapseLevel) => void
  /** Whether a given collapsible group should start open for this level. */
  defaultOpen: (blockType: BlockType) => boolean
}

/**
 * Resolve the default-open state for a collapsible block under a collapse level.
 * Pure helper, exported for components that manage their own open state.
 *
 * BlockType mapping (WebIteration fields):
 *   'reasoning' = T — reasoning block (always folded)
 *   'tool'      = C — tool call
 *   'text'      = O — text output (always shown, not folded)
 *   'iteration' = iteration container
 *
 *   all     → everything closed (summary + final O only)
 *   minimal → everything folded but the folded *rows* are rendered (T/C folded, O shown)
 *   none    → everything expands except reasoning (T is always folded)
 */
export function defaultOpenForLevel(level: CollapseLevel, blockType: BlockType): boolean {
  switch (level) {
    case 'none':
      // Everything expands except reasoning (T blocks are always collapsed).
      return blockType !== 'reasoning'
    case 'all':
      return false // full collapse
    case 'minimal':
      return false // full collapse (header shows summary, click to expand detail)
  }
}

// ── Hooks ──────────────────────────────────────────────────────────────────

export function useCollapseLevel(): UseCollapseLevelResult {
  const level = useSyncExternalStore(subscribeCollapse, getSnapshotLevel, getSnapshotLevel)

  const setLevel = useCallback((next: CollapseLevel) => {
    try {
      localStorage.setItem(COLLAPSE_LEVEL_STORAGE_KEY, next)
    } catch {
      /* ignore */
    }
    notifyCollapse()
  }, [])

  const defaultOpen = useCallback(
    (blockType: BlockType) => defaultOpenForLevel(level, blockType),
    [level],
  )

  return { level, setLevel, defaultOpen }
}

export function useMergeTools(): {
  mergeTools: boolean
  setMergeTools: (value: boolean) => void
} {
  const mergeTools = useSyncExternalStore(subscribeMerge, getSnapshotMergeTools, getSnapshotMergeTools)

  const setMergeTools = useCallback((value: boolean) => {
    try {
      localStorage.setItem(MERGE_TOOLS_STORAGE_KEY, String(value))
    } catch {
      /* ignore */
    }
    notifyMerge()
  }, [])

  return { mergeTools, setMergeTools }
}
