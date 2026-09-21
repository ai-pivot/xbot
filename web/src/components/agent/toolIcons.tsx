/**
 * Tool icon mapping — tool name → Lucide icon (Spec B §1).
 *
 * Every tool call renders with a Lucide line icon instead of emoji.
 * Unmapped tools fall back to `Wrench`.
 *
 * Icon style: 14–16px, color `var(--text-muted)`, shrink-0.
 */
import {
  SquareTerminal, FileText, Search, FolderSearch, FilePlus, FilePen,
  Globe, Download, Sparkles, Wrench, GitBranch, FolderOpen,
  Clock, MessageSquare, Users, Settings, ListTodo, Edit, Zap,
  Layers, HelpCircle, Upload, type LucideIcon,
} from 'lucide-react'

const TOOL_ICON_MAP: Record<string, LucideIcon> = {
  // File operations
  Shell:        SquareTerminal,
  Read:         FileText,
  Grep:         Search,
  Glob:         FolderSearch,
  Cd:           FolderOpen,
  FileCreate:   FilePlus,
  FileReplace:  FilePen,
  Edit:         Edit,

  // Network
  WebSearch:    Globe,
  Fetch:        Download,
  WebFetch:     Download,

  // Agent related
  SubAgent:     Sparkles,
  CreateChat:   Sparkles,
  SendMessage:  MessageSquare,
  Worktree:     GitBranch,
  AskUser:      HelpCircle,

  // Tool management
  ManageTools:  Wrench,
  Skill:        Zap,
  config:       Settings,
  tui_control:  Settings,
  TodoWrite:    ListTodo,
  context_edit: Edit,

  // Time / scheduling
  Cron:         Clock,

  // Memory
  memory_write: Layers,
  memory_list:  Layers,

  // File operations (download)
  DownloadFile: Download,

  // 发布/分享（share_file：把本地文件发布成可嵌入的 URL）——
  // ⚠️ 内置工具**必须**有专属 glyph：此前落到 `FALLBACK_ICON`（Wrench = "未知工具"），
  // 在一排工具 pill 里既不表意也不好看（用户 2026-09-19：「折叠版本的 icon 搞好看点」）。
  // `Upload`（托盘 + 上箭头）与 `Download`（Fetch/DownloadFile）成镜像对：进 / 出。
  share_file:   Upload,

  // Group / peers
  JoinGroup:        Users,
  LeaveGroup:       Users,
  ListGroupMembers: Users,

  // Chat history
  ChatHistory:  MessageSquare,

  // Event triggers
  EventTrigger: Zap,
}

/** Fallback icon for unmapped tool names. */
const FALLBACK_ICON = Wrench

/**
 * Resolve the Lucide icon component for a given tool name.
 * Returns `Wrench` for any tool not in the mapping table.
 */
export function getToolIcon(toolName: string): LucideIcon {
  return TOOL_ICON_MAP[toolName] ?? FALLBACK_ICON
}
