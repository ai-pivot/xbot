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
import { SWUpdateButton } from '@/components/SWUpdateButton'

export function InfoBar() {
  // 底部状态栏只放 SW 更新按钮（用户要求：上方显示连接状态，下方改更新按钮，
  // 避免两端重复"已连接"）。常驻固定高度条，更新按钮三态（检查/下载/重启）。
  return (
    <div
      className="relative flex min-w-0 shrink-0 items-center gap-2 overflow-hidden border-t border-[var(--border)] bg-[var(--bg-elevated)] px-3 text-xs"
      style={{ height: 'calc(1.5rem + var(--safe-area-bottom))', paddingBottom: 'var(--safe-area-bottom)' }}
    >
      <PluginPanelContainer container="info_bar" />
      <WidgetZone zone="infoBar" className="min-w-0 flex-1" excludePrefixes={['git:']} />
      <SWUpdateButton />
    </div>
  )
}
