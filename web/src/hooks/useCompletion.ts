/**
 * useCompletion — tab-completion logic for the message input (tiptap edition).
 *
 * Detects `/` (command completion via /api/commands) and `@`
 * (file completion via REST `/api/fs/list`) triggers at the current cursor
 * position, fetches candidates, filters them, and exposes keyboard navigation.
 *
 * Adapted from textarea-based to ProseMirror (tiptap) editor API:
 * - `editor.state.selection.$from` replaces `el.selectionStart`
 * - `editor.chain().deleteRange().insertContentAt().run()` replaces `setValue()` + `setSelectionRange()`
 * - Current paragraph text + offset replaces full-value string slicing
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Editor } from '@tiptap/react'
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

/** Structural type for keyboard events — works with both DOM KeyboardEvent and React.KeyboardEvent */
export type CompletionKeyEvent = {
  key: string
  shiftKey: boolean
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
  isComposing?: boolean
  preventDefault: () => void
}

interface UseCompletionOptions {
  editor: Editor | null
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
 * whitespace or the start of the current text block (paragraph).
 * Returns { start, text } where `start` is the ABSOLUTE ProseMirror position.
 */
function currentWord(editor: Editor): { from: number; text: string } | null {
  const { selection } = editor.state
  if (!selection.empty) return null
  const $from = selection.$from
  // Get text in the current paragraph up to the cursor
  const parentText = $from.parent.textContent
  const offset = $from.parentOffset
  if (offset < 0 || offset > parentText.length) return null
  let start = offset
  while (start > 0) {
    const ch = parentText[start - 1]
    if (ch === ' ' || ch === '\n' || ch === '\t') break
    start--
  }
  return { from: $from.pos - (offset - start), text: parentText.slice(start, offset) }
}

/**
 * Detect `@prefix` at the current cursor — scan backwards in the current
 * paragraph for an `@` preceded by whitespace or paragraph start.
 */
function detectAtPrefix(editor: Editor): { from: number; prefix: string } | null {
  const { selection } = editor.state
  if (!selection.empty) return null
  const $from = selection.$from
  const parentText = $from.parent.textContent
  const offset = $from.parentOffset
  if (offset <= 0) return null
  if (parentText[offset - 1] === ' ' || parentText[offset - 1] === '\n' || parentText[offset - 1] === '\t') return null
  let i = offset - 1
  while (i >= 0 && parentText[i] !== ' ' && parentText[i] !== '\n' && parentText[i] !== '\t' && parentText[i] !== '@') {
    i--
  }
  if (i < 0 || parentText[i] !== '@') return null
  if (i > 0) {
    const prev = parentText[i - 1]
    if (prev !== ' ' && prev !== '\n' && prev !== '\t') return null
  }
  return { from: $from.pos - (offset - i), prefix: parentText.slice(i + 1, offset) }
}

export function useCompletion({
  editor,
  ws,
  cwd,
}: UseCompletionOptions): CompletionState & {
  handleKeyDown: (e: CompletionKeyEvent) => boolean
} {
  const [commandList, setCommandList] = useState<CommandInfo[]>(WEB_LOCAL_COMMANDS)
  const [candidates, setCandidates] = useState<CompletionCandidate[]>([])
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [triggerType, setTriggerType] = useState<'command' | 'file' | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const fileReqSeqRef = useRef(0)
  // Track the text content to trigger re-evaluation on editor updates.
  const [textContent, setTextContent] = useState('')

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

  // Subscribe to editor updates to track text content + cursor position changes.
  useEffect(() => {
    if (!editor) return
    const update = () => {
      setTextContent(editor.getText() + ':' + editor.state.selection.from)
    }
    editor.on('update', update)
    editor.on('selectionUpdate', update)
    // Initial trigger
    update()
    return () => {
      editor.off('update', update)
      editor.off('selectionUpdate', update)
    }
  }, [editor])

  // Detect trigger and compute candidates whenever editor content or cursor changes.
  useEffect(() => {
    if (!editor) {
      setCandidates([])
      setTriggerType(null)
      return
    }

    const word = currentWord(editor)
    const clearCompletion = () => {
      fileReqSeqRef.current++
      if (debounceRef.current) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
      setCandidates([])
      setTriggerType(null)
    }

    if (!word || word.text.length === 0) {
      clearCompletion()
      return
    }

    // Command completion: only when the word starts with '/' and is at the
    // first non-space position of the document's first paragraph.
    if (word.text.startsWith('/') && word.text.length >= 1) {
      const $from = editor.state.selection.$from
      const isFirstBlock = editor.state.doc.firstChild === $from.parent
      if (isFirstBlock) {
        const parentText = $from.parent.textContent
        const offset = $from.parentOffset
        const wordStartInParent = offset - word.text.length
        const beforeWord = parentText.slice(0, Math.max(0, wordStartInParent))
        if (/^\s*$/.test(beforeWord)) {
          const commands = commandList.filter((cmd) => !!cmd.name)
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
      }
    }

    // File completion: `@` at a word boundary
    const at = detectAtPrefix(editor)
    if (at) {
      const textAfterAt = at.prefix
      const lastSlash = textAfterAt.lastIndexOf('/')
      let dirPath = cwd ?? '/'
      let filterText = textAfterAt
      if (lastSlash >= 0) {
        const subPath = textAfterAt.slice(0, lastSlash)
        dirPath = joinPath(cwd ?? '/', subPath)
        filterText = textAfterAt.slice(lastSlash + 1)
      }

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
  }, [editor, textContent, commandList, cwd])

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
      if (!candidate || !editor) return
      const { selection } = editor.state
      if (!selection.empty) return

      // Compute word/at ranges using the SAME logic as detection
      const at = triggerType === 'file' ? detectAtPrefix(editor) : null
      let word = triggerType === 'file'
        ? (at ? { from: at.from, text: `@${at.prefix}` } : null)
        : currentWord(editor)
      if (!word) return

      const cursorPos = selection.from
      const trigger = word.text[0] // '/' or '@'
      const completed = candidate.insertText.startsWith(trigger)
        ? candidate.insertText
        : `${trigger}${candidate.insertText}`
      const suffix = trigger === '@' && candidate.isDir ? '' : ' '

      // Replace the trigger word with the completed text + suffix
      editor
        .chain()
        .focus()
        .deleteRange({ from: word.from, to: cursorPos })
        .insertContentAt(word.from, completed + suffix)
        .run()

      // Set cursor to after the completed text
      const newCursorPos = word.from + completed.length + suffix.length
      editor.commands.setTextSelection(newCursorPos)

      if (!candidate.isDir) {
        setCandidates([])
        setTriggerType(null)
      }
    },
    [candidates, editor, triggerType],
  )

  const handleKeyDown = useCallback(
    (e: CompletionKeyEvent): boolean => {
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
