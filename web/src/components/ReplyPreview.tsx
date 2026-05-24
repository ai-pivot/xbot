import { useTranslation } from '../i18n'
import { IconUser, IconBot } from './Icons'
import type { ReplyInfo } from '../types'
import { REPLY_PREVIEW_LENGTH } from '../constants'

interface ReplyPreviewProps {
  replyTo: ReplyInfo
  onClick: () => void
}

export default function ReplyPreview({ replyTo, onClick }: ReplyPreviewProps) {
  const { t } = useTranslation()
  const preview = replyTo.content.length > REPLY_PREVIEW_LENGTH
    ? replyTo.content.slice(0, REPLY_PREVIEW_LENGTH) + '...'
    : replyTo.content
  const icon = replyTo.type === 'user' ? <IconUser className="inline" /> : <IconBot className="inline" />

  return (
    <button
      className="reply-preview"
      onClick={onClick}
      data-testid="reply-preview"
      title={t('replyingTo')}
    >
      <span className="reply-preview-bar" />
      <div className="reply-preview-content">
        <span className="reply-preview-icon">{icon}</span>
        <span className="reply-preview-text">{preview}</span>
      </div>
    </button>
  )
}
