/**
 * Shared panel prop shape — the dockview React bridge hands each panel content
 * component this object so Spec 4/5 panels can read their logical params + api.
 */
import type { DockviewPanelApi, DockviewApi } from 'dockview'
import type { PanelParams } from '@/types/tab'

export interface PanelProps {
  params: PanelParams
  api: DockviewPanelApi
  containerApi: DockviewApi
}
