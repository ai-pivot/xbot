import type { WsProgressPayload, IterationSnapshot } from './components/ProgressPanel'

/** Preset command stored in user_settings (key: preset_commands) */
export interface PresetCommand {
  id: string
  label: string
  icon: string
  content: string
  fill?: boolean  // true = fill editor instead of direct send
  sort: number
}

/** Reply reference information for quoted messages */
export interface ReplyInfo {
  id: string
  /** Truncated preview of the original message content */
  content: string
  type: 'user' | 'assistant'
}

/** Unified Message type used across ChatPage and AssistantTurn */
export interface Message {
  id: string
  type: 'user' | 'assistant' | 'system'
  content: string
  ts?: number
  // Saved progress snapshot when this message was finalized (for showing intermediate process)
  savedProgress?: WsProgressPayload | null
  // Full iteration history (persisted across refreshes)
  iterationHistory?: IterationSnapshot[] | null
  /** Reply reference — links to the message being replied to */
  replyTo?: ReplyInfo
  /** Sending status — only meaningful for user messages */
  status?: 'sending' | 'sent' | 'failed'
  /** Whether this message was edited after initial send */
  edited?: boolean
  /** Reactions on this message (frontend-only state) */
  reactions?: Reaction[]
  /** Thread replies (frontend-only state) */
  threadMessages?: ThreadMessage[]
  /** Thread count for quick display */
  threadCount?: number
}

/** Reaction on a message */
export interface Reaction {
  id: string
  emoji: string
  /** User IDs who reacted */
  users: string[]
  /** Current user reacted */
  byMe: boolean
}

/** Message in a thread */
export interface ThreadMessage {
  id: string
  parentId: string
  type: 'user' | 'assistant'
  content: string
  ts: number
  author?: string
}

/** Notification item for notification center */
export interface NotificationItem {
  id: string
  type: 'message' | 'reply' | 'mention' | 'ws_connected' | 'ws_disconnected' | 'ws_reconnecting' | 'system'
  title: string
  body: string
  ts: number
  read: boolean
  messageId?: string
}

/** Sound feedback configuration */
export interface SoundConfig {
  enabled: boolean
  volume: number  // 0-1
  sentSound: 'beep' | 'chime' | 'pop' | 'none'
  receiveSound: 'beep' | 'chime' | 'pop' | 'none'
  notifySound: 'beep' | 'chime' | 'pop' | 'none'
}

/** Turn-based message grouping (Codex style) */
export type Turn =
  | { type: 'user'; message: Message }
  | { type: 'assistant'; messages: Message[] }
