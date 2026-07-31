/**
 * ShimmerThinking — bold borderless "正在思考" text using the shared
 * CSS-driven status sweep.
 */
import { memo } from 'react'

import { useI18n } from '@/providers/i18n'
import { SweepText } from './SweepText'

export const ShimmerThinking = memo(function ShimmerThinking() {
  const { t } = useI18n()
  const text = t('agent.reasoningStreaming') // "思考中…" / "thinking…"

  return (
    <div className="mt-1">
      <SweepText text={text} className="text-sm font-bold" />
    </div>
  )
})
