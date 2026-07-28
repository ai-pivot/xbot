/**
 * MessageItem — the virtualized-list row renderer (Spec 4 §3.4).
 *
 * Dispatches by role to UserMessage / AssistantMessage. The component is
 * memoized with a stable props surface so the virtualizer can keep an item
 * mounted across scroll without re-rendering it. `liveProgress` is passed only
 * for the single streaming item; all others get a stable null.
 *
 * Spec C: UserMessage now carries inline-edit state (editingMessageId).
 */
import { memo } from 'react'

import { AssistantMessage } from './AssistantMessage'
import { UserMessage } from './UserMessage'
import type { ChatMessage, LiveProgress } from '@/types/agent'

interface MessageItemProps {
  message: ChatMessage
  /** Live progress snapshot for the streaming assistant message, else null. */
  liveProgress?: LiveProgress | null
  /** Active collapse-level preference. */
  collapseLevel: 'all' | 'minimal' | 'none'
  /** Whether to merge consecutive tools. Default true. */
  mergeTools?: boolean
  /** Rewind callback — now receives the edited content string. */
  onRewind?: (editedContent: string) => void
  /** Whether this specific message is currently being edited. */
  isEditing?: boolean
  /** Callback to start editing this message. */
  onStartEdit?: () => void
  /** Callback to end editing this message. */
  onEndEdit?: () => void
  /** Whether editing is disabled (another message is being edited). */
  editDisabled?: boolean
}

export const MessageItem = memo(function MessageItem({
  message,
  liveProgress,
  collapseLevel,
  mergeTools = true,
  onRewind,
  isEditing = false,
  onStartEdit,
  onEndEdit,
  editDisabled = false,
}: MessageItemProps) {
  if (message.role === 'user') {
    return (
      <UserMessage
        content={message.content}
        onRewind={onRewind}
        isEditing={isEditing}
        onStartEdit={onStartEdit}
        onEndEdit={onEndEdit}
        editDisabled={editDisabled}
        sending={message.sending}
        isNotification={message.isNotification}
      />
    )
  }
  return (
    <AssistantMessage
      message={message}
      progress={liveProgress}
      collapseLevel={collapseLevel}
      mergeTools={mergeTools}
    />
  )
})
