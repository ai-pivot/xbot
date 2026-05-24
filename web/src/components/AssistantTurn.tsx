import { useTranslation } from '../i18n'
import { useState, useMemo, memo, type ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import { IconList, IconThinking, IconZap, IconPackage, IconCheck, IconClock, IconRefresh } from './Icons'
import { sanitizeStreamContent } from '../utils'
import rehypeKatex from 'rehype-katex'
import { getCodeBlockProps } from './CodeBlock'
import Lightbox from './Lightbox'
import type { PluggableList } from 'unified'

// Pre-built plugins array for Markdown rendering
// remark-math: single-dollar inline math disabled to prevent false positives
//   (e.g. "$景$" in Chinese text triggers KaTeX unicodeTextInMathMode warnings → infinite re-render)
// rehype-katex: suppress strict warnings to prevent console spam causing layout thrash
const remarkPluginsList: PluggableList = [remarkGfm, [remarkMath, { singleDollarTextMath: false }]]
const rehypePluginsList: PluggableList = [[rehypeKatex, { strict: false, trust: false, throwOnError: false }]]
import MessageActions from './MessageActions'

import type { WsProgressPayload, IterationSnapshot } from './ProgressPanel'
import { CompletedIteration, BouncingDots, SubAgentTree } from './ProgressPanel'
import type { Message } from '../types'
import ReplyPreview from './ReplyPreview'
import { formatElapsed, computeDisplayIterations } from '../utils'


import CollapsibleContent from './CollapsibleContent'


// Memoized thinking display — only re-renders when content actually changes
const ThinkingBlock = memo(({ content }: { content: string }) => (
  <div className="px-2 py-1.5 rounded bg-indigo-500/10 border-l-2 border-indigo-500/40">
    <div className="text-[10px] text-indigo-400/70 font-medium mb-0.5"><IconThinking className="inline" /> Reasoning</div>
    <div className="text-xs text-indigo-300/90 whitespace-pre-wrap break-words">{content}</div>
  </div>
))

interface AssistantTurnProps {
  messages: Message[]
  progress: WsProgressPayload | null
  liveIterations?: IterationSnapshot[]
  loading: boolean
  // Saved progress from a completed response (for showing intermediate process collapsed)
  savedProgress?: WsProgressPayload | null
  onDelete?: () => void
  onRegenerate?: () => void
  onReply?: () => void
  /** Scroll to a message by ID (for reply click) */
  onScrollToMessage?: (id: string) => void
  /** Streaming content length for progress display */
  streamingLength?: number
  /** Double-click to reply */
  onDoubleClickReply?: () => void

}



/** Collapsible section with a header bar */
function CollapsibleSection({
  icon,
  title,
  badge,
  defaultOpen = false,
  children,
  className = '',
}: {
  icon: ReactNode
  title: string
  badge?: string | number
  defaultOpen?: boolean
  children: React.ReactNode
  className?: string
}) {
  const [open, setOpen] = useState(defaultOpen)

  return (
    <div className={`assistant-turn-section ${className}`}>
      <button
        className="assistant-turn-section-header"
        onClick={() => setOpen(!open)}
      >
        <span className="flex items-center gap-2">
          <span>{icon}</span>
          <span className="text-xs text-slate-400 font-medium">{title}</span>
          {badge !== undefined && (
            <span className="text-xs text-slate-500 font-mono">({badge})</span>
          )}
        </span>
        <span className={`assistant-turn-chevron ${open ? 'assistant-turn-chevron-open' : ''}`}>▸</span>
      </button>
      {open && (
        <div className="assistant-turn-section-body">
          {children}
        </div>
      )}
    </div>
  )
}

/**
 * Detect if a message content looks like "thinking" output.
 * Heuristic: starts with 💭 or is wrapped in <think/> tags or contains
 * typical thinking markers.
 */
function isThinkingContent(content: string): boolean {
  const trimmed = content.trim()
  // Only match explicit thinking markers — NOT regular assistant text
  if (trimmed.startsWith('💭') && trimmed.length > 12) return true
  if (trimmed.startsWith('<think')) return true
  if (trimmed.startsWith('<thinking')) return true
  if (trimmed.startsWith('【思考】')) return true
  return false
}

export default memo(function AssistantTurn({ messages, progress, liveIterations, loading, savedProgress, onDelete, onRegenerate, onReply, onScrollToMessage, streamingLength, onDoubleClickReply }: AssistantTurnProps) {
  const [copied, setCopied] = useState(false)
  const [lightbox, setLightbox] = useState<{ src: string; alt: string } | null>(null)
  const codeBlockProps = useMemo(() => getCodeBlockProps((src, alt) => setLightbox({ src, alt })), [])
  const { t } = useTranslation()

  // Classify messages
  const thinkingMsgs: Message[] = []
  const textMsgs: Message[] = []
  const intermediateMsgs: Message[] = []

  for (const msg of messages) {
    if (isThinkingContent(msg.content)) {
      thinkingMsgs.push(msg)
    } else if (msg.content.trim() === '' && msg.iterationHistory) {
      // Content cleared (tool narration stripped) but has structured iteration history.
      // Skip as text — iteration panel will show the structured data via savedProgress.
    } else {
      textMsgs.push(msg)
    }
  }

  // When multiple text messages exist, only the LAST one with substantial content
  // is the final reply. Earlier messages are intermediate LLM output (tool narration,
  // progress descriptions, system-reminder artifacts) that should be collapsed.
  if (textMsgs.length > 1) {
    // Find the last message with real content (non-empty, non-whitespace)
    let lastContentIdx = -1
    for (let i = textMsgs.length - 1; i >= 0; i--) {
      if (textMsgs[i].content.trim().length > 0) {
        lastContentIdx = i
        break
      }
    }
    if (lastContentIdx > 0) {
      // Move everything before the last content message to intermediate
      intermediateMsgs.push(...textMsgs.splice(0, lastContentIdx))
    }
  }

  const handleCopy = () => {
    const text = textMsgs.map(m => m.content).join('\n\n')
    if (!text) return
    navigator.clipboard.writeText(text).then(
      () => { setCopied(true); setTimeout(() => setCopied(false), 2000) },
      () => { /* clipboard unavailable (e.g. HTTP) */ },
    )
  }

  // Use live progress when loading, fall back to savedProgress for completed turns
  const effectiveProgress = loading ? progress : (savedProgress ?? null)
  const hasTools = effectiveProgress
    ? (effectiveProgress.completed_tools?.length ?? 0) + (loading ? (progress?.active_tools?.length ?? 0) : 0) > 0
    : false

  // Determine phase display
  const phaseIcon = effectiveProgress?.phase === 'thinking' ? <IconThinking className="inline" />
    : effectiveProgress?.phase === 'tool_exec' ? <IconZap className="inline" />
    : effectiveProgress?.phase === 'compressing' ? <IconPackage className="inline" />
    : effectiveProgress?.phase === 'retrying' ? <IconRefresh className="inline" />
    : effectiveProgress?.phase === 'done' ? <IconCheck className="inline" />
    : null

  const displayLiveIterations = computeDisplayIterations(liveIterations, progress)

  const currentThinking = (progress?.thinking || '').trim()
  const seenThinkings = new Set(displayLiveIterations.map(s => (s.thinking || '').trim()).filter(Boolean))
  const shouldShowCurrentThinking = currentThinking.length > 0 && !seenThinkings.has(currentThinking)

  return (
    <div className="flex justify-start w-full">
      <div className="assistant-turn-container flex-1 min-w-0" data-testid="assistant-turn" onDoubleClick={onDoubleClickReply}>
        {/* Reply preview */}
        {textMsgs.length > 0 && textMsgs[0].replyTo && onScrollToMessage && (
          <ReplyPreview
            replyTo={textMsgs[0].replyTo}
            onClick={() => onScrollToMessage(textMsgs[0].replyTo!.id)}
          />
        )}

        {/* Collapsible: Thinking section */}
        {thinkingMsgs.length > 0 && (
          <CollapsibleSection icon={<IconList />} title={t("thinkingProcess")} badge={thinkingMsgs.length} defaultOpen={false} className="thinking-section">
            <div className="space-y-2 pl-1">
              {thinkingMsgs.map((msg) => (
                <div key={msg.id} className="text-sm text-slate-400 italic">
                  <Markdown components={codeBlockProps} remarkPlugins={remarkPluginsList} rehypePlugins={rehypePluginsList}>
                    {msg.content.replace(/^💭\s*/, '')}
                  </Markdown>
                </div>
              ))}
            </div>
          </CollapsibleSection>
        )}

        {/* Live completed iterations (during loading) */}
        {loading && (displayLiveIterations.length ?? 0) > 0 && (
          <CollapsibleSection
            icon={<IconList />}
            title={t("iterationProcess")}
            badge={displayLiveIterations.length}
            defaultOpen={true}
          >
            <div className="divide-y divide-slate-700/30">
              {displayLiveIterations.map(snap => <CompletedIteration key={snap.iteration} snap={snap} />)}
            </div>
          </CollapsibleSection>
        )}



        {/* Live progress — current iteration (only during loading) */}
        {loading && progress && progress.phase !== 'done' && (
          <div className="mb-2 rounded border border-slate-700/30 overflow-hidden">
            <div className="px-3 py-2">
              <div className="text-[11px] text-slate-600/90 font-mono mb-1">
                #{progress.iteration}
              </div>
              {shouldShowCurrentThinking && (
                <ThinkingBlock content={progress.thinking} />
              )}
              {(progress.active_tools?.length ?? 0) > 0 ? (
                <div className="space-y-0.5">
                  {progress.active_tools!.map((tool, i) => (
                    <div key={`active-${i}`} className="flex items-center gap-2 px-2 py-1 text-sm">
                      <span className="tool-pulse"><IconClock className="inline" /></span>
                      <span className="font-mono text-xs text-slate-400 flex-1 truncate">
                        {tool.label || tool.name}
                      </span>
                      {tool.elapsed_ms > 0 && (
                        <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>
                      )}
                    </div>
                  ))}
                </div>
              ) : !shouldShowCurrentThinking ? (
                <div className="flex items-center gap-2 px-2 py-1">
                  <BouncingDots />
                  <span className="text-xs text-slate-500 italic">
                    {progress.phase === 'thinking' ? t('thinking') : progress.phase === 'tool_exec' ? t('executingTool') : t('processing')}
                  </span>
                </div>
              ) : null}
              {/* SubAgent tree */}
              {progress.sub_agents && progress.sub_agents.length > 0 && (
                <div className="mt-2 pt-2 border-t border-slate-700/30">
                  <SubAgentTree agents={progress.sub_agents} />
                </div>
              )}
            </div>
          </div>
        )}

        {/* Loading but no progress yet — show animated placeholder */}
        {loading && (!progress || progress.phase === 'done') && (
          <div className="mb-2 rounded border border-slate-700/30 overflow-hidden">
            <div className="px-3 py-2">
              <BouncingDots text={t('preparing')} />
            </div>
          </div>
        )}



        {/* Collapsible: Iteration history (from saved snapshots) */}
        {!loading && messages.length > 0 && messages[messages.length - 1]?.iterationHistory && messages[messages.length - 1].iterationHistory!.length > 0 && (
          <CollapsibleSection
            icon={<IconList />}
            title={t("iterationProcess")}
            badge={messages[messages.length - 1].iterationHistory!.length}
            defaultOpen={false}
          >
            <div className="divide-y divide-slate-700/30">
              {messages[messages.length - 1].iterationHistory!.map((snap) => (
                <CompletedIteration key={snap.iteration} snap={snap} />
              ))}
            </div>
          </CollapsibleSection>
        )}

        {/* Intermediate LLM output (tool narration, progress) — collapsed by default */}
        {intermediateMsgs.length > 0 && (
          <CollapsibleSection icon="📝" title={t('intermediateOutput')} badge={intermediateMsgs.length}>
            {intermediateMsgs.map((msg) => (
              <div key={msg.id} className="markdown-body text-sm opacity-60">
                <Markdown components={codeBlockProps} remarkPlugins={remarkPluginsList} rehypePlugins={rehypePluginsList}>
                  {sanitizeStreamContent(msg.content)}
                </Markdown>
              </div>
            ))}
          </CollapsibleSection>
        )}

        {/* Main text content — always visible */}
        {textMsgs.length > 0 && (
          <div className="assistant-turn-content">
            <CollapsibleContent>
              {textMsgs.map((msg) => (
                <div key={msg.id} className="markdown-body">
                  <Markdown components={codeBlockProps} remarkPlugins={remarkPluginsList} rehypePlugins={rehypePluginsList}>
                    {msg.content}
                  </Markdown>
                </div>
              ))}
            </CollapsibleContent>
          </div>
        )}

        {/* Loading pulse when no content yet and no iteration/progress placeholders */}
        {loading && textMsgs.length === 0 && !hasTools && !phaseIcon && !progress && (
            <div className="thinking-orb thinking-orb-sm">
              <div className="thinking-orb-ring thinking-orb-ring-1" />
              <div className="thinking-orb-ring thinking-orb-ring-2" />
              <div className="thinking-orb-core" />
            </div>
        )}

        {/* Loading indicator at bottom of content when still streaming */}
        {loading && textMsgs.length > 0 && (
          <div className="assistant-turn-streaming-indicator">
            <span className="assistant-turn-cursor" />
            {streamingLength != null && streamingLength > 0 && (
              <span className="text-xs text-slate-500 ml-2">{streamingLength} chars</span>
            )}
          </div>
        )}

        {/* Edited indicator */}
        {!loading && textMsgs.length > 0 && textMsgs[textMsgs.length - 1]?.edited && (
          <div className="text-xs text-slate-600 mt-1 italic">(edited)</div>
        )}

        {/* Message actions — bottom-left, always visible */}
        {textMsgs.length > 0 && !loading && (
          <MessageActions
            onCopy={handleCopy}
            onDelete={onDelete}
            onRegenerate={onRegenerate}
            onReply={onReply}
            copied={copied}
          />
        )}
      </div>
      {/* Lightbox portal */}
      {lightbox && (
        <Lightbox src={lightbox.src} alt={lightbox.alt} onClose={() => setLightbox(null)} />
      )}
    </div>
  )
})
