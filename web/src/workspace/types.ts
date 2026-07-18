/**
 * Aggregated context value bridged from the outer React tree into the
 * isolated dockview panel/tab roots.
 *
 * dockview v7 mounts each panel/tab renderer on a detached DOM element with
 * its own React root, so those subtrees do NOT inherit the app's Context
 * providers. We bridge all needed values through a single `DockviewContext`
 * so panels read from one typed source via `useDockviewContext()`.
 */
import { createContext, useContext } from 'react'

import type { ThemeContextValue } from '@/types/theme'
import type { I18nContextValue } from '@/providers/i18n'
import type { WSConnection } from '@/types/ws'
import type { CwdContextValue } from '@/providers/CwdProvider'
import type { AuthContextValue } from '@/providers/AuthProvider'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { RightSidebarControl } from '@/components/sidebar/RightSidebarControl'
import type { TabManager } from '@/hooks/useTabManager'

export interface DockviewContextValue {
  theme: ThemeContextValue
  i18n: I18nContextValue
  ws: WSConnection
  cwd: CwdContextValue
  auth: AuthContextValue
  sessionStore: SessionStore
  rightSidebar: RightSidebarControl
  openTab?: TabManager['openTab']
}

export const DockviewContext = createContext<DockviewContextValue | null>(null)

/**
 * Read the aggregated dockview context. Throws if used outside a
 * `DockviewContext.Provider` (i.e. outside a dockview panel/tab root).
 */
export function useDockviewContext(): DockviewContextValue {
  const ctx = useContext(DockviewContext)
  if (!ctx) throw new Error('useDockviewContext must be used within DockviewContext.Provider')
  return ctx
}
