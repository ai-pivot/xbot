import { useState, memo } from 'react'
import { IconCopy, IconCheck } from './Icons'

export interface TodoItem {
  id: number
  text: string
  done: boolean
}

export interface TodoBarProps {
  todos: TodoItem[]
}

/**
 * Persistent TodoBar — displayed above the message list.
 * Shows TODO progress across turns, cleared only on new user message.
 * Inspired by TUI's renderTodoBar (channel/cli_view.go).
 */
export const TodoBar = memo(function TodoBar({ todos }: TodoBarProps) {
  const [collapsed, setCollapsed] = useState(false)

  if (!todos || todos.length === 0) return null

  const done = todos.filter(t => t.done).length
  const total = todos.length
  const pct = total > 0 ? Math.round((done / total) * 100) : 0
  const allDone = done === total

  return (
    <div className={`
      mx-4 my-1 rounded-lg border overflow-hidden transition-all duration-300
      ${allDone
        ? 'bg-green-900/20 border-green-800/30'
        : 'bg-slate-800/60 border-slate-700/40'}
    `}>
      {/* Header: always visible, clickable to toggle collapse */}
      <button
        className="w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-slate-700/20 transition-colors"
        onClick={() => setCollapsed(!collapsed)}
        aria-expanded={!collapsed}
        aria-label={`TODO ${done}/${total}`}
      >
        <span className="text-xs flex-shrink-0"><IconCopy /></span>
        <span className={`text-xs font-semibold flex-shrink-0 ${allDone ? 'text-green-400' : 'text-slate-300'}`}>
          TODO
        </span>
        <span className="text-[11px] text-slate-500 font-mono flex-shrink-0">
          {done}/{total}
        </span>
        {/* Progress bar */}
        <div className="flex-1 max-w-[120px] min-w-[60px] bg-slate-700/50 rounded-full h-1.5 mx-1">
          <div
            className={`h-1.5 rounded-full transition-all duration-500 ease-out ${
              allDone
                ? 'bg-green-400'
                : 'bg-gradient-to-r from-blue-500 to-cyan-400'
            }`}
            style={{ width: `${pct}%` }}
          />
        </div>
        <span className="text-[10px] text-slate-500 font-mono w-8 text-right flex-shrink-0">{pct}%</span>
        <span className={`text-slate-500 text-[10px] transition-transform duration-200 flex-shrink-0 ${collapsed ? '' : 'rotate-90'}`}>
          ▸
        </span>
      </button>

      {/* Item list (collapsible) */}
      {!collapsed && (
        <div className="px-3 pb-2 space-y-px border-t border-slate-700/20 pt-1 max-h-40 overflow-y-auto">
          {todos.map(todo => (
            <div key={todo.id} className="flex items-center gap-2 text-xs leading-relaxed">
              <span className={`flex-shrink-0 ${todo.done ? 'text-green-400' : 'text-slate-500'}`}>
                {todo.done ? <IconCheck className="inline" /> : <span style={{width:10,height:10,borderRadius:'50%',display:'inline-block',border:'1.5px solid currentColor',verticalAlign:'middle'}} />}
              </span>
              <span className={`truncate ${
                todo.done
                  ? 'text-slate-500 line-through'
                  : 'text-slate-300'
              }`}>
                {todo.text}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
})
