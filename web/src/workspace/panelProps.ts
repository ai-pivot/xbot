/**
 * panelProps —— 面板组件的 `PanelProps` 构造（**无 Dockview** 场景共用）。
 *
 * 桌面端面板由 Dockview 提供 `PanelProps`；移动端外壳与宿主嵌入模式都没有 Dockview
 * ⇒ 只能自己拼。⛔ 两处各拼一份必然分叉（`tabId`/`terminalId` 少一项终端面板就白屏），
 * 所以只保留这一份真源。
 */
import type { PanelProps } from '@/workspace/panels/types'

/** 为形如「全屏呈现的终端会话」构造 `PanelProps`（桌面端由 Dockview tab 提供）。 */
export function mobileTerminalProps(terminalId: string): PanelProps {
  return {
    params: {
      tabId: `mobile-terminal-${terminalId}`,
      type: 'terminal',
      title: 'Terminal',
      icon: 'terminal',
      closable: true,
      active: true,
      terminalId,
    },
    api: {} as PanelProps['api'],
    containerApi: {} as PanelProps['containerApi'],
  }
}
