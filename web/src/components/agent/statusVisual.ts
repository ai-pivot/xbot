/**
 * Shared helpers for rendering tool/reasoning state chips (Spec 4).
 *
 * The visual (icon/color) is data-driven; the human label is resolved by the
 * caller via i18n using `labelKey`, so this module stays free of locale.
 */
import { Check, CircleDashed, Loader, OctagonAlert } from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'

import { toolStatusKind, type ToolStatusKind } from '@/types/agent'

export interface ToolStatusVisual {
  icon: ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>
  /** i18n key under `agent.*` for the status label. */
  labelKey: 'statusDone' | 'statusRunning' | 'statusError' | 'statusPending'
  color: string
}

/** Whether a tool is still preparing or executing. */
export function isToolInProgress(status: string | undefined): boolean {
  return status === 'pending' || status === 'generating' || status === 'running'
}

/** Visual mapping for a tool status kind, using CSS variables for theming. */
export function toolStatusVisual(status: string | undefined): ToolStatusVisual {
  switch (toolStatusKind(status)) {
    case 'done':
      return { icon: Check, labelKey: 'statusDone', color: 'var(--status-running)' }
    case 'error':
      return { icon: OctagonAlert, labelKey: 'statusError', color: 'var(--status-error)' }
    case 'running':
      return { icon: Loader, labelKey: 'statusRunning', color: 'var(--status-running)' }
    case 'pending':
    default:
      return { icon: CircleDashed, labelKey: 'statusPending', color: 'var(--status-idle)' }
  }
}

export type { ToolStatusKind }
