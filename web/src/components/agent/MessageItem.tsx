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
  /** Rewind callback — receives the edited content + the row it belongs to.
   *  ⚠️ 必须由调用方以稳定引用传入（row 由本组件回填）：inline 箭头会让
   *  memo 在**每个流式帧**失效 → 整个虚拟列表可见行全部重渲染
   *  （Trace-20260912T100816：每帧 O(可见行) 的 MessageItem/TurnBody 重入）。 */
  onRewind?: (editedContent: string, row: ChatMessage) => void
  /** Whether this specific message is currently being edited. */
  isEditing?: boolean
  /** Callback to start editing this message (receives the row id). */
  onStartEdit?: (rowId: string) => void
  /** Callback to end editing this message. */
  onEndEdit?: () => void
  /** Whether editing is disabled (another message is being edited). */
  editDisabled?: boolean
  /**
   * 迭代块高度/冻结裁决的作用域（会话身份 + 布局宽度，见 TurnBody）。
   * 必须由调用方传入：它决定"同一内容重挂载时能否复用先前实测高度"。
   */
  heightScope?: string
}

export const MessageItem = memo(function MessageItem({
  message,
  liveProgress,
  onRewind,
  isEditing = false,
  onStartEdit,
  onEndEdit,
  editDisabled = false,
  heightScope,
}: MessageItemProps) {
  if (message.role === 'user') {
    return (
      <UserMessage
        content={message.content}
        onRewind={onRewind ? (editedContent: string) => onRewind(editedContent, message) : undefined}
        isEditing={isEditing}
        onStartEdit={onStartEdit ? () => onStartEdit(message.id) : undefined}
        onEndEdit={onEndEdit}
        editDisabled={editDisabled}
        sending={message.sending}
        queued={message.queued}
        isNotification={message.isNotification}
      />
    )
  }
  return (
    <AssistantMessage
      message={message}
      progress={liveProgress}
      heightScope={heightScope}
    />
  )
})
