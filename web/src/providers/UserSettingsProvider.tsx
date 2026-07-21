/**
 * UserSettingsProvider — syncs UI preferences from server on app load.
 *
 * On mount, calls syncAndMigrateSettings() which fetches `web:ui:*` settings
 * from the server and writes them to localStorage (server is authoritative).
 * If the server has no UI settings yet, pushes localStorage values (migration).
 *
 * Does NOT block rendering — children render immediately from localStorage
 * (optimistic). When the sync completes, a SETTINGS_SYNCED_EVENT is dispatched
 * and components that listen for it re-read from localStorage.
 *
 * Must be placed OUTSIDE ThemeProvider and I18nProvider so the sync runs
 * before they initialize (though they render optimistically from localStorage
 * first, then update on the sync event).
 */
import { useEffect, type ReactNode } from 'react'
import { syncAndMigrateSettings } from '@/lib/userSettings'

export function UserSettingsProvider({ children }: { children: ReactNode }) {
  useEffect(() => {
    void syncAndMigrateSettings()
  }, [])

  return <>{children}</>
}
