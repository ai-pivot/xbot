import { createContext, useContext } from 'react'
import type { SidebarPanel } from './RightSidebar'

export interface RightSidebarControl {
  openPanel: (panel: SidebarPanel) => void
}

export const RightSidebarControlContext = createContext<RightSidebarControl | null>(null)

export function useRightSidebarControl(): RightSidebarControl | null {
  return useContext(RightSidebarControlContext)
}
