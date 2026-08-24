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
import { PluginPanelContainer } from '@/plugins/manager/PluginPanelContainer'

export function InfoBar() {
  // WidgetZone renders non-git spans (git spans excluded — the fancy
  // GitStatusPanel in the plugin view container renders them instead).
  //
  // Height absorbs the bottom safe area (iOS PWA home-indicator / rounded
  // corners): the bar's bg-[--bg-elevated] paints through it, and the content
  // stays inside the top 1.5rem. Desktop / browser mode: inset is 0 — the bar
  // stays a plain h-6 strip, unchanged.
  return (
    <div
      className="flex min-w-0 shrink-0 items-center gap-2 overflow-hidden border-t border-[var(--border)] bg-[var(--bg-elevated)] px-3 text-xs"
      style={{ height: 'calc(1.5rem + var(--safe-area-bottom))', paddingBottom: 'var(--safe-area-bottom)' }}
    >
      <PluginPanelContainer container="info_bar" />
      <PluginPanelContainer container="status_bar_right" />
      <WidgetZone zone="infoBar" className="min-w-0 flex-1" excludePrefixes={['git:']} />
    </div>
  )
}
