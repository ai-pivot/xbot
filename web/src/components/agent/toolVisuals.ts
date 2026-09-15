/**
 * Tool pill 视觉语言（用户 2026-09-15 定稿）：
 *   - 分类色 9 套：执行/读取/写入/检索/代理/任务/记忆/UI/系统 —— 用于**图标与工具名**；
 *   - 状态色与分类色**解耦**：成功安静（描边绿勾）、失败吵闹（红底红边红条+实心红叉+标签）、
 *     终止灰虚线、进行中分类色脉动；
 *   - 假工具（注入型）用 **kind 颜色 + 头像缩写**，配色与 `SyntheticToolCard` 保持一致（避免两套色）。
 * 这里只有数据与纯函数，渲染在 `FoldedToolGroup.tsx` 的 `toolPill()` 里。
 */

export type ToolCategory = 'exec' | 'read' | 'write' | 'search' | 'agent' | 'task' | 'memory' | 'ui' | 'sys'

export const CATEGORY_COLOR: Record<ToolCategory, string> = {
  exec: '#38bdf8',
  read: '#818cf8',
  write: '#fbbf24',
  search: '#c084fc',
  agent: '#34d399',
  task: '#fb7185',
  memory: '#e879f9',
  ui: '#fb923c',
  sys: '#94a3b8',
}

export const CATEGORY_LABEL: Record<ToolCategory, string> = {
  exec: '执行',
  read: '读取',
  write: '写入',
  search: '检索',
  agent: '代理',
  task: '任务',
  memory: '记忆',
  ui: '界面',
  sys: '系统',
}

const NAMED: Record<string, ToolCategory> = {
  // 执行
  Shell: 'exec',
  // 读取
  Read: 'read',
  ChatHistory: 'read',
  // 写入
  FileCreate: 'write',
  FileReplace: 'write',
  // 检索
  Grep: 'search',
  Glob: 'search',
  WebSearch: 'search',
  Fetch: 'search',
  search_tools: 'search',
  // 代理/协作
  SubAgent: 'agent',
  SendMessage: 'agent',
  CreateChat: 'agent',
  JoinGroup: 'agent',
  LeaveGroup: 'agent',
  ListGroupMembers: 'agent',
  // 任务/调度
  task_status: 'task',
  task_read: 'task',
  task_wait: 'task',
  task_kill: 'task',
  TodoWrite: 'task',
  TodoList: 'task',
  Cron: 'task',
  EventTrigger: 'task',
  Worktree: 'task',
  // 记忆/技能
  Skill: 'memory',
  compact_context: 'memory',
  context_edit: 'memory',
  offload_recall: 'memory',
  recall_masked: 'memory',
  rethink: 'memory',
  // UI/卡片
  display_html: 'ui',
  view_image: 'ui',
  AskUser: 'ui',
  // 系统
  config: 'sys',
  tui_control: 'sys',
}

/** 工具 → 分类（前缀兜底，保证任何工具都有分类色，不会退化成无色）。 */
export function toolCategory(name: string): ToolCategory {
  const named = NAMED[name]
  if (named) return named
  if (name.startsWith('memory_') || name.startsWith('archival_') || name.startsWith('core_')) return 'memory'
  if (name.startsWith('task_')) return 'task'
  if (name.startsWith('card_')) return 'ui'
  if (name.startsWith('bg_subagent_') || name.startsWith('background_')) return 'task'
  if (name.startsWith('reply_') || name.startsWith('trend_')) return 'exec'
  return 'sys'
}

/** 假工具 kind → 颜色（与 SyntheticToolCard 的 kind 配色一致）。 */
export const SYNTHETIC_KIND_COLOR: Record<string, string> = {
  bg_task: '#38bdf8',
  subagent: '#a78bfa',
  cron: '#fbbf24',
  async: '#2dd4bf',
  delivered: '#2dd4bf',
  interrupt: '#a78bfa',
  cancel: '#94a3b8',
  pre_turn_end: '#94a3b8',
  loop: '#f59e0b',
}

export function syntheticKindColor(kind: string): string {
  return SYNTHETIC_KIND_COLOR[kind] ?? CATEGORY_COLOR.sys
}

/** 假工具 kind → 头像缩写（2 字母；与设计稿一致）。 */
const KIND_BADGE: Record<string, string> = {
  bg_task: 'BG',
  subagent: 'SA',
  cron: 'CR',
  async: 'AS',
  delivered: 'DL',
  interrupt: 'IN',
  cancel: 'CC',
  pre_turn_end: 'PT',
  loop: 'LP',
}

export function syntheticKindBadge(kind: string): string {
  return KIND_BADGE[kind] ?? 'SY'
}
