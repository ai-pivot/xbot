/**
 * PluginComponentPanel — renders declarative web_ui components for a slot.
 *
 * Consumes `components` from PluginWidgetProvider, filters by slot, and
 * renders each via the component registry (or SandboxedUI for code/src).
 * Interactive components route through `onAction` → web_ui_action RPC.
 */
import { useState, useCallback } from 'react'

import { usePluginWidgets } from './PluginWidgetProvider'
import { renderDeclarativeComponent } from './components'
import { SandboxedUI } from './SandboxedUI'
import { sendWebUIAction } from './api'
import type { WebUIComponentDecl } from '@/types/shared'

export interface PluginComponentPanelProps {
  slot: string
  /** Empty-state placeholder. */
  empty?: React.ReactNode
  className?: string
}

export function PluginComponentPanel({ slot, empty = null, className }: PluginComponentPanelProps) {
  const { components } = usePluginWidgets()
  const [busy, setBusy] = useState(false)
  const list = components.filter((c) => c.slot === slot)
  if (list.length === 0) return <>{empty}</>

  // Route a component interaction to the backend (web_ui_action RPC).
  const onAction = useCallback(
    async (widgetId: string, action: string, data?: unknown) => {
      setBusy(true)
      try {
        await sendWebUIAction({ widget_id: widgetId, action, data })
      } catch {
        // best-effort
      } finally {
        setBusy(false)
      }
    },
    [],
  )

  return (
    <div className={`flex flex-col gap-3 ${className ?? ''}`}>
      {busy && <div className="text-right text-[10px] text-indigo-500">↻ 处理中…</div>}
      {list.map((decl) => (
        <PluginComponent key={decl.widget_id} decl={decl} onAction={onAction} />
      ))}
    </div>
  )
}

function PluginComponent({
  decl,
  onAction,
}: {
  decl: WebUIComponentDecl
  onAction: (widgetId: string, action: string, data?: unknown) => void
}) {
  const title = decl.title ? (
    <div className="mb-1 text-xs font-semibold text-slate-600">{decl.title}</div>
  ) : null

  // Free-form source code / external URL → sandboxed iframe.
  if (decl.code) {
    return (
      <div className="overflow-hidden rounded-lg border border-slate-200">
        {title}
        <SandboxedUI code={decl.code} widgetId={decl.widget_id} onAction={onAction} />
      </div>
    )
  }
  if (decl.src) {
    return (
      <div className="overflow-hidden rounded-lg border border-slate-200">
        {title}
        <SandboxedUI src={decl.src} widgetId={decl.widget_id} onAction={onAction} />
      </div>
    )
  }
  // Declarative component.
  const type = decl.component?.type ?? ''
  const props = decl.component?.props ?? {}
  return (
    <div className="rounded-lg border border-slate-200 p-2">
      {title}
      {renderDeclarativeComponent(type, props, (action, data) => onAction(decl.widget_id, action, data))}
    </div>
  )
}
