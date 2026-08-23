/**
 * 插件视图图标映射：view.icon 声明的是字符串名（如 "blocks"），
 * 宿主侧栏渲染 tab 时映射到 lucide 组件。未知图标回退到 Puzzle。
 */
import { Blocks, Boxes, ChartColumn, FileCode2, GitBranch, LayoutGrid, Puzzle, Sparkles, Wrench, type LucideIcon } from 'lucide-react'

const ICON_MAP: Record<string, LucideIcon> = {
  blocks: Blocks,
  boxes: Boxes,
  chart: ChartColumn,
  code: FileCode2,
  git: GitBranch,
  grid: LayoutGrid,
  puzzle: Puzzle,
  sparkles: Sparkles,
  wrench: Wrench,
}

export function pluginIcon(name?: string): LucideIcon {
  if (!name) return Puzzle
  return ICON_MAP[name.toLowerCase()] ?? Puzzle
}