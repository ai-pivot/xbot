/**
 * Tab param types shared between useTabManager and DockviewContainer.
 *
 * `PanelParams` is what dockview hands to a panel content renderer and to a
 * custom tab header renderer (via the panel's `.params`). It carries the
 * logical tab id/type plus the domain payload (agent sessionId / file path).
 */
import type { TabType } from './shared'

export interface PanelParams {
  tabId: string
  type: TabType
  title: string
  /** Lucide icon name resolved by the TabHeader. */
  icon?: string
  sessionId?: string
  filePath?: string
  /** Frontend terminal id (TerminalSession.id) for terminal tabs. */
  terminalId?: string
  /** False suppresses the close button and blocks closeTab (agent tabs). */
  closable: boolean
  /** SubAgent role (only for agent tabs viewing a SubAgent conversation). */
  subAgentRole?: string
  /** SubAgent instance (only for agent tabs viewing a SubAgent conversation). */
  subAgentInstance?: string
  /** Parent chatID for SubAgent tabs. */
  parentChatID?: string
  /** Parent channel for SubAgent tabs. */
  parentChannel?: string
  /** Full persisted agent tenant chatID for historical SubAgent tabs. */
  agentChatID?: string
  /** Background task id for background-output tabs. */
  taskID?: string
  /** Background task command for tab title/content. */
  command?: string
  /** Session channel for background task RPCs. */
  taskChannel?: string
  /** Session chatID for background task RPCs. */
  taskChatID?: string
  /** True when this dockview panel is the active panel. */
  active?: boolean
}
