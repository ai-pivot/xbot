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
  // 手机端底部状态栏（MobileAppShell 渲染；桌面端 rail row 已删，检查更新
  // 按钮在 PanelLauncher chip 栏）。SWUpdateButton 默认 chip 栏尺寸（size-7），
  // 这里覆盖为状态条内联尺寸。
  return (
    <div
      className="relative flex min-w-0 shrink-0 items-center gap-2 overflow-hidden border-t border-[var(--border)] bg-[var(--surface-bg)] px-3 text-xs"
      style={{ height: 'calc(1.5rem + var(--safe-area-bottom))', paddingBottom: 'var(--safe-area-bottom)' }}
    >
      <PluginPanelContainer container="info_bar" />
      <WidgetZone zone="infoBar" className="min-w-0 flex-1" excludePrefixes={['git:']} />
      <SWUpdateButton className="h-5 w-5 hover:bg-transparent hover:text-text-primary" />
    </div>
  )
}
