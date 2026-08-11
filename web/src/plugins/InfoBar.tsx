/**
 * InfoBar — renders the plugin widget `infoBar` zone as a slim banner above
 * the workspace. Plugins contribute styled spans via the widget registry;
 * the web channel renders them structured and pushes via web_widgets SSE.
 */
import { WidgetZone } from '@/plugins/WidgetZone'
import { usePluginWidgets } from '@/plugins/PluginWidgetProvider'

export function InfoBar() {
  const { zones } = usePluginWidgets()
  const hasContent = (zones.infoBar?.length ?? 0) > 0
  if (!hasContent) return null
  return (
    <div className="flex h-6 shrink-0 items-center gap-2 overflow-hidden border-b border-[var(--border)] bg-[var(--bg-elevated)] px-3 text-xs">
      <WidgetZone zone="infoBar" />
    </div>
  )
}
