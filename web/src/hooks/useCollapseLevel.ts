/**
 * useCollapseLevel — reads/writes the Agent intermediate-process collapse
 * preference (Spec 4 §3.3 / §3.10).
 *
 * Persisted at localStorage key `xbot-collapse-level`. Values:
 *   'all'     — collapse all intermediate steps, show only final output
 *   'minimal' — show tool name + summary, collapse details
 *   'none'    — expand everything
 *
 * Spec 7's settings panel will offer the same control; Spec 4 only needs the
 * read + a lightweight in-component override. The hook keeps one global state
 * instance so all Agent panels stay in sync and the storage event keeps tabs
 * consistent across windows.
 */
import { useCallback, useEffect, useState } from 'react'

import {
  COLLAPSE_LEVELS,
  COLLAPSE_LEVEL_STORAGE_KEY,
  DEFAULT_COLLAPSE_LEVEL,
  type CollapseLevel,
} from '@/types/agent'

function readStored(): CollapseLevel {
  try {
    const v = localStorage.getItem(COLLAPSE_LEVEL_STORAGE_KEY)
    if (v && (COLLAPSE_LEVELS as string[]).includes(v)) return v as CollapseLevel
  } catch {
    /* ignore */
  }
  return DEFAULT_COLLAPSE_LEVEL
}

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

export function useCollapseLevel(): UseCollapseLevelResult {
  const [level, setLevelState] = useState<CollapseLevel>(readStored)

  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key === COLLAPSE_LEVEL_STORAGE_KEY) setLevelState(readStored())
    }
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])

  const setLevel = useCallback((next: CollapseLevel) => {
    setLevelState(next)
    try {
      localStorage.setItem(COLLAPSE_LEVEL_STORAGE_KEY, next)
    } catch {
      /* ignore */
    }
  }, [])

  const defaultOpen = useCallback(
    (blockType: BlockType) => defaultOpenForLevel(level, blockType),
    [level],
  )

  return { level, setLevel, defaultOpen }
}
