/**
 * 插件视图图标映射：view.icon 声明的是字符串名（如 "blocks"），
 * 宿主侧栏渲染 tab 时映射到 lucide 组件。未知图标回退到 Puzzle。
 *
 * 这是通用协议层——插件在 manifest 的 view contribution 里声明 icon: "name"，
 * 宿主在此映射表查找对应 lucide 图标。插件无需 import 任何宿主模块。
 * 新增图标只需在此表添加一行 + import 对应 lucide 组件。
 */
import {
  Activity,
  Blocks,
  Boxes,
  ChartColumn,
  FileCode2,
  Files,
  GitBranch,
  GitCommitHorizontal,
  Info,
  ListTodo,
  MessageCircle,
  Puzzle,
  Search,
  Sparkles,
  Terminal,
  Wrench,
  type LucideIcon,
} from 'lucide-react'

const ICON_MAP: Record<string, LucideIcon> = {
  // 通用/结构类
  blocks: Blocks,
  boxes: Boxes,
  chart: ChartColumn,
  code: FileCode2,
  puzzle: Puzzle,
  sparkles: Sparkles,
  wrench: Wrench,

  // 内置面板图标（builtinPanels.tsx 声明）
  message: MessageCircle,
  files: Files,
  search: Search,
  info: Info,
  tasks: ListTodo,
  terminal: Terminal,

  // 插件声明的图标
  activity: Activity,
  'git-branch': GitBranch,
  'git-commit-horizontal': GitCommitHorizontal,
  git: GitBranch,
}

export function pluginIcon(name?: string): LucideIcon {
  if (!name) return Puzzle
  const icon = ICON_MAP[name.toLowerCase()]
  // grid 已弃用（v5.1 前布局项使用）—— 回退到 Puzzle 而非 undefined
  return icon ?? Puzzle
}
