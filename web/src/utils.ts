import type React from 'react'
import type { WsProgressPayload, IterationSnapshot } from './components/ProgressPanel'
import type { Message } from './types'

/**
 * Format milliseconds into a human-readable string.
 * Duplicated in AssistantTurn.tsx and ProgressPanel.tsx — now unified here.
 */
export function formatElapsed(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

/**
 * Format a Unix timestamp (seconds) into a localized time string.
 */
export function formatTime(ts: number): string {
  return new Date(ts * 1000).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
  })
}

/**
 * Format a byte count into a human-readable file size string.
 */
export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}

/**
 * Normalize raw iteration history data into a clean IterationSnapshot[].
 * Handles case-insensitive field names from backend, deduplicates thinking, sorts by iteration.
 */
export function normalizeIterationHistory(input: unknown): IterationSnapshot[] {
  if (!Array.isArray(input) || input.length === 0) return []

  const toNumber = (v: unknown): number | undefined => (typeof v === 'number' ? v : undefined)

  const normalized: IterationSnapshot[] = []
  for (const raw of input) {
    if (!raw || typeof raw !== 'object') continue
    const snap = raw as Record<string, unknown>

    const iteration = toNumber(snap.iteration ?? snap.Iteration)
    if (iteration == null) continue

    const thinkingRaw = snap.thinking ?? snap.Thinking
    const thinking = typeof thinkingRaw === 'string' ? thinkingRaw : undefined

    const reasoningRaw = snap.reasoning ?? snap.Reasoning
    const reasoning = typeof reasoningRaw === 'string' ? reasoningRaw : undefined

    const rawTools = Array.isArray(snap.tools) ? snap.tools : (Array.isArray(snap.Tools) ? snap.Tools : [])
    const tools = rawTools
      .filter((t): t is Record<string, unknown> => !!t && typeof t === 'object')
      .map((t) => {
        const name = typeof (t.name ?? t.Name) === 'string' ? String(t.name ?? t.Name) : ''
        const label = typeof (t.label ?? t.Label) === 'string' ? String(t.label ?? t.Label) : undefined
        const status = typeof (t.status ?? t.Status) === 'string' ? String(t.status ?? t.Status) : 'done'

        const elapsedMsLower = toNumber(t.elapsed_ms)
        const elapsedNsLegacy = toNumber(t.Elapsed)
        const elapsedMs = elapsedMsLower ?? (elapsedNsLegacy != null ? Math.round(elapsedNsLegacy / 1_000_000) : undefined)

        return {
          name,
          label,
          status,
          elapsed_ms: elapsedMs,
        }
      })

    normalized.push({
      iteration,
      thinking,
      reasoning,
      tools,
    })
  }

  const byIteration = new Map<number, IterationSnapshot>()
  for (const snap of normalized) {
    if (typeof snap?.iteration !== 'number') continue
    byIteration.set(snap.iteration, {
      iteration: snap.iteration,
      thinking: snap.thinking,
      reasoning: snap.reasoning,
      tools: Array.isArray(snap.tools) ? snap.tools : [],
    })
  }

  const sorted = Array.from(byIteration.values()).sort((a, b) => a.iteration - b.iteration)
  const seenThinking = new Set<string>()

  return sorted.map((snap) => {
    const thinking = (snap.thinking || '').trim()
    const dedupedThinking = thinking && !seenThinking.has(thinking) ? snap.thinking : undefined
    if (thinking && !seenThinking.has(thinking)) {
      seenThinking.add(thinking)
    }
    return {
      ...snap,
      thinking: dedupedThinking,
    }
  })
}

/**
 * Parameters for createResetProgress — the refs and setters that need resetting.
 */
export interface ResetProgressParams {
  setProgress: (v: null) => void
  setLiveIterations: (v: IterationSnapshot[]) => void
  prevIterationRef: React.MutableRefObject<number>
  progressRef: React.MutableRefObject<WsProgressPayload | null>
  reasoningRef: React.MutableRefObject<string>
  streamingContentRef: React.MutableRefObject<string>
}

/**
 * Create a reusable reset function that clears all progress-related state.
 * Returns a function that performs the standard reset sequence:
 *   setProgress(null) → setLiveIterationsSync([]) → prevIterationRef = -1
 *   → progressRef = null → reasoningRef = '' → streamingContentRef = ''
 */
export function createResetProgress(params: ResetProgressParams): () => void {
  return () => {
    params.setProgress(null)
    params.setLiveIterations([])
    params.prevIterationRef.current = -1
    params.progressRef.current = null
    params.reasoningRef.current = ''
    params.streamingContentRef.current = ''
  }
}

/**
 * Compute display iterations by inferring a previous-iteration snapshot from progress.
 * Shared by AssistantTurn and ProgressPanel.
 */
export function computeDisplayIterations(
  baseIterations: IterationSnapshot[] | undefined,
  progress: WsProgressPayload | null,
): IterationSnapshot[] {
  const base = baseIterations ?? []
  if (!progress || progress.iteration <= 0 || (progress.completed_tools?.length ?? 0) === 0) {
    return base
  }
  const prevIteration = progress.iteration - 1
  if (base.some(s => s.iteration === prevIteration)) return base
  return [...base, {
    iteration: prevIteration,
    tools: (progress.completed_tools ?? []).map(t => ({
      name: t.name,
      label: t.label,
      status: t.status,
      elapsed_ms: t.elapsed_ms,
      summary: t.summary,
    })),
  }].sort((a, b) => a.iteration - b.iteration)
}

// ── Chat Export Utilities ──

/**
 * Export messages as Markdown format.
 */
export function exportAsMarkdown(messages: Message[]): string {
  const date = new Date().toLocaleString()
  const lines = [`# Chat Export — ${date}`, '']
  for (const msg of messages) {
    const role = msg.type === 'user' ? 'User' : msg.type === 'assistant' ? 'Assistant' : 'System'
    lines.push(`## ${role}`, '')
    lines.push(msg.content)
    lines.push('')
    lines.push('---')
    lines.push('')
  }
  return lines.join('\n')
}

/**
 * Export messages as JSON format.
 */
export function exportAsJSON(messages: Message[]): string {
  return JSON.stringify(messages.map(m => ({
    id: m.id,
    type: m.type,
    content: m.content,
    ts: m.ts,
  })), null, 2)
}

/** Format a timestamp as a relative time string (e.g., "just now", "5m ago"). */
export function formatRelativeTime(ts: number): string {
  const now = Date.now()
  const diff = Math.max(0, Math.floor((now - ts) / 1000))
  if (diff < 60) return diff < 5 ? 'just now' : `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  if (diff < 604800) return `${Math.floor(diff / 86400)}d ago`
  return formatTime(Math.floor(ts / 1000))
}

/**
 * Trigger a browser download for the given content.
 */
export function downloadFile(content: string, filename: string, mimeType: string): void {
  const blob = new Blob([content], { type: mimeType })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}
