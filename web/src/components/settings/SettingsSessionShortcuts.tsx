import { useEffect, useId, useRef, useState } from 'react'
import { Ellipsis, Keyboard, RotateCcw, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import {
  SESSION_SHORTCUT_ACTIONS, SESSION_SHORTCUT_TITLES,
  formatShortcutBinding, isWindowsPlatform, parseShortcutBinding, shortcutBindingFromEvent, shortcutBindingsOverlap,
  type SessionShortcutAction, type SessionShortcutBindings,
} from '@/components/session/session-shortcuts'
import { useSessionShortcuts } from '@/hooks/useSessionShortcuts'
import { commands } from '@/lib/commandRouter'
import { useI18n } from '@/providers/i18n'
import { SettingsSection } from './SettingsSection'

function ShortcutRow({ action, bindings, defaultBinding, setBinding }: {
  action: SessionShortcutAction
  bindings: SessionShortcutBindings
  defaultBinding: string
  setBinding: (action: SessionShortcutAction, binding: string | null) => void
}) {
  const { t } = useI18n()
  const inputRef = useRef<HTMLInputElement>(null)
  const id = useId()
  const binding = bindings[action]
  const formatted = formatShortcutBinding(binding)
  const [draft, setDraft] = useState(formatted)
  const [recording, setRecording] = useState(false)
  const [tooltipOpen, setTooltipOpen] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    setDraft(formatted)
    setRecording(false)
    setError(null)
  }, [formatted])

  const save = (value: string) => {
    if (value === formatted) {
      setRecording(false)
      setError(null)
      return
    }
    const next = parseShortcutBinding(value)
    if (next === undefined) {
      setError(t('settings.shortcuts.invalid'))
      return
    }
    if (next) {
      const conflict = SESSION_SHORTCUT_ACTIONS.find(other => other !== action && bindings[other] && shortcutBindingsOverlap(next, bindings[other]!))
      const commandConflict = commands.list().find(command => {
        if (!command.keybinding) return false
        if (shortcutBindingsOverlap(next, command.keybinding.replace(/^ctrl\+/, 'mod+'))) return true
        // CommandRouter folds Meta into Ctrl even when session Mod is Windows-only.
        return isWindowsPlatform() && command.keybinding.startsWith('ctrl+')
          && shortcutBindingsOverlap(next, command.keybinding.replace(/^ctrl\+/, 'meta+'))
      })
      if (conflict || commandConflict) {
        const name = conflict ? t(SESSION_SHORTCUT_TITLES[conflict]) : commandConflict!.titleKey ? t(commandConflict!.titleKey) : commandConflict!.title ?? commandConflict!.id
        setError(t('settings.shortcuts.conflict', { name }))
        return
      }
    }
    try {
      setBinding(action, next)
    } catch {
      setError(t('settings.saveFailed'))
      return
    }
    setDraft(formatShortcutBinding(next))
    setRecording(false)
    setError(null)
  }

  const cancel = () => {
    setDraft(formatted)
    setRecording(false)
    setError(null)
  }

  return (
    <div data-shortcut-action={action} className="grid min-w-0 grid-cols-1 gap-2 border-b border-border py-3 last:border-0 sm:grid-cols-[minmax(0,1fr)_minmax(0,17rem)] sm:items-center">
      <label htmlFor={id} className="min-w-0 text-sm text-text-primary wrap-anywhere">{t(SESSION_SHORTCUT_TITLES[action])}</label>
      <div className="flex min-w-0 items-center gap-1">
        <Input
          ref={inputRef}
          id={id}
          data-testid={`shortcut-${action}`}
          data-shortcut-editing={recording || draft !== formatted}
          value={draft}
          placeholder={recording ? t('settings.shortcuts.recording') : t('settings.shortcuts.unassigned')}
          readOnly={recording}
          aria-invalid={!!error}
          aria-describedby={error ? `${id}-error` : undefined}
          autoComplete="off"
          spellCheck={false}
          className={`h-8 min-w-0 flex-1 font-mono text-xs ${recording ? 'ring-2 ring-accent' : ''}`}
          onChange={event => { setDraft(event.target.value); setError(null) }}
          onBlur={() => {
            if (recording) cancel()
            else if (draft !== formatted) save(draft)
          }}
          onKeyDown={event => {
            if (event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229 || event.repeat) return
            if (event.key === 'Escape') {
              event.preventDefault()
              event.stopPropagation()
              cancel()
            } else if (recording) {
              if (event.key === 'Tab' && !event.ctrlKey && !event.metaKey && !event.altKey) {
                cancel()
                return
              }
              event.preventDefault()
              event.stopPropagation()
              const next = shortcutBindingFromEvent(event.nativeEvent)
              if (next) save(next)
              else if (!['Control', 'Meta', 'Alt', 'Shift'].includes(event.key)) setError(t('settings.shortcuts.invalid'))
            } else if (event.key === 'Enter') {
              event.preventDefault()
              event.stopPropagation()
              save(draft)
            }
          }}
        />
        <Tooltip open={!recording && tooltipOpen} onOpenChange={setTooltipOpen}>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-sm" aria-label={t('settings.shortcuts.record')} aria-pressed={recording} onMouseDown={event => event.preventDefault()} onClick={() => {
              setRecording(!recording)
              setDraft(recording ? formatted : '')
              setError(null)
              setTooltipOpen(false)
              inputRef.current?.focus()
            }}><Keyboard /></Button>
          </TooltipTrigger>
          <TooltipContent>{t('settings.shortcuts.record')}</TooltipContent>
        </Tooltip>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" aria-label={t('settings.shortcuts.more')} title={t('settings.shortcuts.more')}><Ellipsis /></Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem className="min-h-10" disabled={binding === null} onSelect={() => save('')}><X />{t('settings.shortcuts.clear')}</DropdownMenuItem>
            <DropdownMenuItem className="min-h-10" disabled={binding === defaultBinding} onSelect={() => save(defaultBinding)}><RotateCcw />{t('settings.shortcuts.reset')}</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {error ? <p role="alert" id={`${id}-error`} className="text-xs text-destructive wrap-anywhere sm:col-span-2">{error}</p> : null}
    </div>
  )
}

export function SettingsSessionShortcuts() {
  const { t } = useI18n()
  const { bindings, defaults, setBinding, resetAll } = useSessionShortcuts()
  const [resetVersion, setResetVersion] = useState(0)
  const [resetError, setResetError] = useState(false)
  return (
    <SettingsSection
      title={t('settings.shortcuts.title')}
      actions={
        <Button variant="ghost" size="sm" className="text-xs" onClick={() => {
          try {
            resetAll()
            setResetVersion(version => version + 1)
            setResetError(false)
          } catch { setResetError(true) }
        }}><RotateCcw />{t('settings.shortcuts.resetAll')}</Button>
      }
    >
      {resetError ? <p role="alert" className="text-xs text-destructive">{t('settings.saveFailed')}</p> : null}
      <div className="min-w-0">
        {SESSION_SHORTCUT_ACTIONS.map(action => <ShortcutRow key={`${action}:${resetVersion}`} action={action} bindings={bindings} defaultBinding={defaults[action]} setBinding={setBinding} />)}
      </div>
    </SettingsSection>
  )
}
