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
import { useWSConnection } from '@/hooks/useWSConnection'

export function InfoBar() {
  // VSCode 式默认状态：左下角连接状态指示（有内容时也常驻，空时至少有点东西，
  // 避免空条太素）。绿点=已连接，红点=连接中。
  const ws = useWSConnection()
  const connected = ws.connected
  return (
    <div
      className="flex min-w-0 shrink-0 items-center gap-2 overflow-hidden border-t border-[var(--border)] bg-[var(--bg-elevated)] px-3 text-xs"
      style={{ height: 'calc(1.5rem + var(--safe-area-bottom))', paddingBottom: 'var(--safe-area-bottom)' }}
    >
      <span className="flex shrink-0 items-center gap-1.5 whitespace-nowrap">
        <span
          className={
            connected
              ? 'size-1.5 rounded-full bg-emerald-500'
              : 'size-1.5 animate-pulse rounded-full bg-amber-500'
          }
        />
        <span className="text-text-muted">{connected ? '已连接' : '连接中…'}</span>
      </span>
      <PluginPanelContainer container="info_bar" />
      <WidgetZone zone="infoBar" className="min-w-0 flex-1" excludePrefixes={['git:']} />
    </div>
  )
}
