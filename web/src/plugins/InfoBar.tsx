/**
 * InfoBar — renders the plugin widget `infoBar` zone as a slim status bar
 * BELOW the workspace (VSCode status-bar style). Plugins contribute styled
 * spans via the widget registry; the web channel renders them structured and
 * pushes via web_widgets SSE.
 *
 * The bar is ALWAYS rendered (even with no plugin content) so it does not
 * suddenly pop in/out as plugins appear/disappear — a stable fixed-height
 * strip keeps the layout rock-solid (user request: 不要突然跳出来，一直渲染).
 */
import { WidgetZone } from '@/plugins/WidgetZone'

export function InfoBar() {
  // WidgetZone consumes the plugin widget registry itself — this component only
  // provides the always-rendered fixed-height strip.
  return (
    <div className="flex h-6 min-w-0 shrink-0 items-center gap-2 overflow-hidden border-t border-[var(--border)] bg-[var(--bg-elevated)] px-3 text-xs">
      <WidgetZone zone="infoBar" className="min-w-0 flex-1" />
    </div>
  )
}
