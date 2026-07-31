/**
 * useCompletion — tab-completion logic for the message input.
 *
 * Detects `/` (command completion via /api/commands) and `@`
 * (file completion via REST `/api/fs/list`) triggers at the current cursor
 * position, fetches candidates, filters them, and exposes keyboard navigation.
 *
 * The popup state is fully derived from the input value — clearing the input
 * (e.g. after sending a message or switching sessions) automatically hides it.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchCommands } from '@/components/agent/api'
import type { WSConnection } from '@/types/ws'
import { postAPI } from '@/lib/api'

export interface CompletionCandidate {
  /** Display label (e.g. "/cancel" or "README.md"). */
  label: string
  /** Insert text — replaces the trigger word (e.g. "/cancel" or "README.md"). */
  insertText: string
  /** Optional description (commands only). */
  description?: string
  /** True for directories (file completion only). */
  isDir?: boolean
}

export interface CompletionState {
  candidates: CompletionCandidate[]
  selectedIndex: number
  visible: boolean
  triggerType: 'command' | 'file' | null
  completeCandidate: (index: number) => void
}

interface UseCompletionOptions {
  value: string
  setValue: (v: string) => void
  textareaRef: React.RefObject<HTMLTextAreaElement | null>
  ws: WSConnection
  cwd: string | null
}

interface CommandInfo {
  name: string
  aliases?: string[]
  description?: string
}

interface FsEntry {
  name: string
  isDir: boolean
}

const MAX_CANDIDATES = 20
export const WEB_LOCAL_COMMANDS: CommandInfo[] = [
  { name: '/cancel' },
  { name: '/channel' },
  { name: '/chat' },
  { name: '/clear' },
  { name: '/commands' },
  { name: '/compress' },
  { name: '/context' },
  { name: '/exit' },
  { name: '/help' },
  { name: '/list-sessions' },
  { name: '/llm' },
  { name: '/models' },
  { name: '/new' },
  { name: '/palette' },
  { name: '/plugin' },
  { name: '/quit' },
  { name: '/rename' },
  { name: '/rewind' },
  { name: '/search' },
  { name: '/sessions' },
  { name: '/set-llm' },
  { name: '/set-model' },
  { name: '/settings' },
  { name: '/setup' },
  { name: '/ss' },
  { name: '/su' },
  { name: '/tasks' },
  { name: '/unset-llm' },
  { name: '/update' },
  { name: '/usage' },
  { name: '/user' },
]

/**
 * Find the current "word" being typed — from the cursor backwards to the last
 * whitespace or the start of the input. Returns { start, text } or null.
 */
function currentWord(value: string, cursorPos: number): { start: number; text: string } | null {
  if (cursorPos < 0 || cursorPos > value.length) return null
  let start = cursorPos
  while (start > 0) {
    const ch = value[start - 1]
    if (ch === ' ' || ch === '\n' || ch === '\t') break
    start--
  }
  return { start, text: value.slice(start, cursorPos) }
}

function detectAtPrefix(value: string, cursorPos: number): { start: number; prefix: string } | null {
  if (cursorPos <= 0 || cursorPos > value.length) return null
  const input = value.slice(0, cursorPos)
  if (input.endsWith(' ') || input.endsWith('\n') || input.endsWith('\t')) return null
  let i = input.length - 1
  while (i >= 0 && input[i] !== ' ' && input[i] !== '\n' && input[i] !== '\t' && input[i] !== '@') {
    i--
  }
  if (i < 0 || input[i] !== '@') return null
  if (i > 0) {
    const prev = input[i - 1]
    if (prev !== ' ' && prev !== '\n' && prev !== '\t') return null
  }
  return { start: i, prefix: input.slice(i + 1) }
}

export function useCompletion({
  value,
  setValue,
  textareaRef,
  ws,
  cwd,
}: UseCompletionOptions): CompletionState & {
  handleKeyDown: (e: React.KeyboardEvent<HTMLTextAreaElement>) => boolean
} {
  const [commandList, setCommandList] = useState<CommandInfo[]>(WEB_LOCAL_COMMANDS)
  const [candidates, setCandidates] = useState<CompletionCandidate[]>([])
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [triggerType, setTriggerType] = useState<'command' | 'file' | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const fileReqSeqRef = useRef(0)

  // Fetch command list once (cached for the session).
  useEffect(() => {
    let cancelled = false
    fetchCommands<CommandInfo>()
      .then((cmds) => {
        if (!cancelled) setCommandList(mergeCommandList(cmds ?? []))
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [ws])

  // Detect trigger and compute candidates whenever value changes.
  useEffect(() => {
    const el = textareaRef.current
    const cursorPos = el?.selectionStart ?? value.length
    const word = currentWord(value, cursorPos)
    const clearCompletion = () => {
      fileReqSeqRef.current++
      if (debounceRef.current) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
      setCandidates([])
      setTriggerType(null)
    }

    if (!word || (word.text.length === 0)) {
      clearCompletion()
      return
    }

    // Command completion: TUI treats slash commands as whole-input commands.
    // Only complete when the current word starts at the first non-space char.
    const commandStart = value.search(/\S/)
    if (word.text.startsWith('/') && word.text.length >= 1 && word.start === commandStart) {
      const commands = commandList.filter((cmd) => {
        if (!cmd.name) return false
        return true
      })
      const seen = new Set<string>()
      const filtered = commands
        .flatMap((cmd) => [cmd.name, ...(cmd.aliases || [])].map((name) => ({ ...cmd, name })))
        .filter((cmd) => {
          if (!cmd.name || seen.has(cmd.name) || !cmd.name.startsWith(word.text)) return false
        seen.add(cmd.name)
        return true
      })
        .slice(0, MAX_CANDIDATES)
        .map((c) => ({
          label: c.name,
          insertText: c.name,
          description: c.description,
        }))
      setCandidates(filtered)
      setTriggerType('command')
      setSelectedIndex(0)
      return
    }

    // File completion: TUI only treats @ as a file trigger at a word boundary.
    const at = detectAtPrefix(value, cursorPos)
    if (at) {
      const textAfterAt = at.prefix
      // Split on last "/" to get directory and filter
      const lastSlash = textAfterAt.lastIndexOf('/')
      let dirPath = cwd ?? '/'
      let filterText = textAfterAt
      if (lastSlash >= 0) {
        const subPath = textAfterAt.slice(0, lastSlash)
        dirPath = joinPath(cwd ?? '/', subPath)
        filterText = textAfterAt.slice(lastSlash + 1)
      }

      // Debounce file list fetch
      if (debounceRef.current) clearTimeout(debounceRef.current)
      const reqSeq = ++fileReqSeqRef.current
      debounceRef.current = setTimeout(() => {
        fetchFsList(dirPath)
          .then((entries) => {
            if (reqSeq !== fileReqSeqRef.current) return
            const filtered = entries
              .filter((e) => e.name.toLowerCase().startsWith(filterText.toLowerCase()))
              .slice(0, MAX_CANDIDATES)
              .map((e) => ({
                label: e.name + (e.isDir ? '/' : ''),
                insertText: e.name + (e.isDir ? '/' : ''),
                isDir: e.isDir,
              }))
            setCandidates(filtered)
            setTriggerType('file')
            setSelectedIndex(0)
          })
          .catch(() => {
            if (reqSeq !== fileReqSeqRef.current) return
            setCandidates([])
            setTriggerType(null)
          })
      }, 150)
      return
    }

    clearCompletion()
  }, [value, commandList, cwd, textareaRef])

  // Cleanup debounce on unmount
  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [])

  const visible = candidates.length > 0 && triggerType !== null

  const completeCandidate = useCallback(
    (index: number) => {
      const candidate = candidates[index]
      if (!candidate) return
      const el = textareaRef.current
      const cursorPos = el?.selectionStart ?? value.length
      const at = triggerType === 'file' ? detectAtPrefix(value, cursorPos) : null
      let word = triggerType === 'file'
        ? (at ? { start: at.start, text: `@${at.prefix}` } : null)
        : currentWord(value, cursorPos)
      if (!word) return
      if (triggerType === 'command') {
        const commandStart = value.search(/\S/)
        if (commandStart >= 0 && word.start === commandStart) {
          word = { ...word, start: 0 }
        }
      }

      const before = value.slice(0, word.start)
      const after = value.slice(cursorPos)
      const trigger = word.text[0] // '/' or '@'
      const completed = candidate.insertText.startsWith(trigger)
        ? candidate.insertText
        : `${trigger}${candidate.insertText}`
      const suffix = trigger === '@' && candidate.isDir ? '' : ' '
      const newValue = `${before}${completed}${suffix}${after}`
      setValue(newValue)

      // Move cursor to after the completed text plus optional space.
      const newCursorPos = word.start + completed.length + suffix.length
      requestAnimationFrame(() => {
        el?.focus()
        el?.setSelectionRange(newCursorPos, newCursorPos)
      })

      if (!candidate.isDir) {
        setCandidates([])
        setTriggerType(null)
      }
    },
    [candidates, textareaRef, triggerType, value, setValue],
  )

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>): boolean => {
      if (!visible) return false

      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setSelectedIndex((i) => (i + 1) % candidates.length)
        return true
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setSelectedIndex((i) => (i - 1 + candidates.length) % candidates.length)
        return true
      }
      if (e.key === 'Tab' || (triggerType === 'file' && e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey)) {
        e.preventDefault()
        completeCandidate(selectedIndex)
        return true
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setCandidates([])
        setTriggerType(null)
        return true
      }
      return false
    },
    [visible, candidates.length, selectedIndex, triggerType, completeCandidate],
  )

  return {
    candidates,
    selectedIndex,
    visible,
    triggerType,
    handleKeyDown,
    completeCandidate,
  }
}

function mergeCommandList(remote: CommandInfo[]): CommandInfo[] {
  const byName = new Map<string, CommandInfo>()
  for (const cmd of [...WEB_LOCAL_COMMANDS, ...remote]) {
    if (!cmd.name) continue
    const existing = byName.get(cmd.name)
    byName.set(cmd.name, existing ? { ...existing, ...cmd } : cmd)
  }
  return [...byName.values()]
}

/** Join path segments, handling relative sub-paths within CWD. */
function joinPath(base: string, sub: string): string {
  if (sub.startsWith('/')) return sub
  if (base.endsWith('/')) return base + sub
  return `${base}/${sub}`
}

/** Fetch directory listing from the REST API. */
async function fetchFsList(path: string): Promise<FsEntry[]> {
  const data = await postAPI<{ entries?: FsEntry[] }>('/api/fs/list', { path })
  return data.entries ?? []
}
