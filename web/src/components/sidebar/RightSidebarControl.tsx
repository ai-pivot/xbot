import { createContext, useContext } from 'react'

/**
 * Panel identifiers for the bottom panel section of the left sidebar.
 * Replaces the old right-sidebar panel system.
 */
export type SidebarPanel = 'files' | 'search' | 'info' | 'tasks' | 'terminal'

export interface SidebarControl {
  openPanel: (panel: SidebarPanel) => void
}

export const SidebarControlContext = createContext<SidebarControl | null>(null)

export function useSidebarControl(): SidebarControl | null {
  return useContext(SidebarControlContext)
}

// --- Legacy aliases (for gradual migration) ---
export type RightSidebarControl = SidebarControl
export const RightSidebarControlContext = SidebarControlContext
export function useRightSidebarControl(): SidebarControl | null {
  return useContext(SidebarControlContext)
}
