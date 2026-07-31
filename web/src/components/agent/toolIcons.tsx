/**
 * Tool icon mapping — tool name → Lucide icon (Spec B §1).
 *
 * Every tool call renders with a Lucide line icon instead of emoji.
 * Unmapped tools fall back to `Wrench`.
 *
 * Icon style: 14–16px, color `var(--text-muted)`, shrink-0.
 */
import {
  Terminal, FileText, Search, FolderSearch, FilePlus, FilePen,
  Globe, Download, Sparkles, Wrench, GitBranch, FolderOpen,
  Clock, MessageSquare, Users, Settings, ListTodo, Edit, Zap,
  Layers, HelpCircle, type LucideIcon,
} from 'lucide-react'

const TOOL_ICON_MAP: Record<string, LucideIcon> = {
  // File operations
  Shell:        Terminal,
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
