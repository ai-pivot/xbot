import { useTranslation } from '../i18n'
import { useState, useMemo, memo } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import { getCodeBlockProps } from './CodeBlock'
import Lightbox from './Lightbox'

// Pre-built plugins array for Markdown rendering
const remarkPluginsList = [remarkGfm, remarkMath]
const rehypePluginsList = [rehypeKatex]
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
    <div className="text-[10px] text-indigo-400/70 font-medium mb-0.5">💭 Reasoning</div>
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
  icon: string
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

export default memo(function AssistantTurn({ messages, progress, liveIterations, loading, savedProgress, onDelete, onRegenerate, onReply, onScrollToMessage, streamingLength }: AssistantTurnProps) {
  const [copied, setCopied] = useState(false)
  const [lightbox, setLightbox] = useState<{ src: string; alt: string } | null>(null)
  const codeBlockProps = useMemo(() => getCodeBlockProps((src, alt) => setLightbox({ src, alt })), [])
  const { t } = useTranslation()

  // Classify messages
  const thinkingMsgs: Message[] = []
  const textMsgs: Message[] = []

  for (const msg of messages) {
    if (isThinkingContent(msg.content)) {
      thinkingMsgs.push(msg)
    } else {
      textMsgs.push(msg)
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
  const phaseIcon = effectiveProgress?.phase === 'thinking' ? '💭'
    : effectiveProgress?.phase === 'tool_exec' ? '⚡'
    : effectiveProgress?.phase === 'compressing' ? '📦'
    : effectiveProgress?.phase === 'retrying' ? '🔄'
    : effectiveProgress?.phase === 'done' ? '✅'
    : null

  const displayLiveIterations = computeDisplayIterations(liveIterations, progress)

  const currentThinking = (progress?.thinking || '').trim()
  const seenThinkings = new Set(displayLiveIterations.map(s => (s.thinking || '').trim()).filter(Boolean))
  const shouldShowCurrentThinking = currentThinking.length > 0 && !seenThinkings.has(currentThinking)

  return (
    <div className="flex justify-start">
      <div className="assistant-turn-container group relative" data-testid="assistant-turn">
        {/* Message actions — visible on hover */}
        {textMsgs.length > 0 && !loading && (
          <MessageActions
            onCopy={handleCopy}
            onDelete={onDelete}
            onRegenerate={onRegenerate}
            onReply={onReply}
            copied={copied}
          />
        )}
        {/* Reply preview */}
        {textMsgs.length > 0 && textMsgs[0].replyTo && onScrollToMessage && (
          <ReplyPreview
            replyTo={textMsgs[0].replyTo}
            onClick={() => onScrollToMessage(textMsgs[0].replyTo!.id)}
          />
        )}

        {/* Collapsible: Thinking section */}
        {thinkingMsgs.length > 0 && (
          <CollapsibleSection icon="💭" title={t("thinkingProcess")} badge={thinkingMsgs.length} className="thinking-section">
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
            icon="📋"
            title={t("iterationProcess")}
            badge={displayLiveIterations.length}
            defaultOpen={true}
          >
            <div className="divide-y divide-slate-700/30">
              {displayLiveIterations.map(snap => (
                <div key={snap.iteration} className="px-3 py-2">
                  <div className="text-[11px] text-slate-600/90 font-mono mb-1">
                    #{snap.iteration}
                  </div>
                  {snap.reasoning && (
                    <div className="px-2 py-1.5 mb-1 rounded bg-indigo-500/10 border-l-2 border-indigo-500/40">
                      <div className="text-[10px] text-indigo-400/70 font-medium mb-0.5">💭 Reasoning</div>
                      <div className="text-xs text-indigo-300/90 whitespace-pre-wrap break-words">{snap.reasoning}</div>
                    </div>
                  )}
                  {snap.thinking && (
                    <div className="px-2 py-1.5 mb-1 rounded bg-amber-500/10 border-l-2 border-amber-500/40">
                      <div className="text-[10px] text-amber-400/70 font-medium mb-0.5">💡 Thinking</div>
                      <div className="text-xs text-amber-300/80 italic whitespace-pre-wrap break-words">{snap.thinking}</div>
                    </div>
                  )}
                  <div className="space-y-0.5">
                    {snap.tools.map((tool, i) => (
                      <div key={`${snap.iteration}-${i}`} className="px-2 py-1 text-sm">
                        <div className="flex items-center gap-2">
                          <span>{tool.status === 'error' ? '❌' : '✅'}</span>
                          <span className="font-mono text-xs text-slate-400 flex-1 truncate">
                            {tool.label || tool.name}
                          </span>
                          {tool.elapsed_ms != null && tool.elapsed_ms > 0 && (
                            <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>
                          )}
                        </div>
                        {tool.summary && (
                          <div className="text-[10px] text-slate-500 truncate pl-5 mt-0.5">{tool.summary}</div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              ))}
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
                      <span className="tool-pulse">⏳</span>
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
            icon="📋"
            title={t("iterationProcess")}
            badge={messages[messages.length - 1].iterationHistory!.length}
          >
            <div className="divide-y divide-slate-700/30">
              {messages[messages.length - 1].iterationHistory!.map((snap) => (
                <CompletedIteration key={snap.iteration} snap={snap} />
              ))}
            </div>
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
      </div>
      {/* Lightbox portal */}
      {lightbox && (
        <Lightbox src={lightbox.src} alt={lightbox.alt} onClose={() => setLightbox(null)} />
      )}
    </div>
  )
})
