/**
 * DiffPanel —— 宿主原生 diff 编辑器 tab（VSCode DiffEditor 语义）。
 *
 * 插件经 ctx.ui.openDiffTab({title, path, original, modified, key?}) 打开：
 * 宿主负责语言推断（fileTypes.languageOf）、Monaco DiffEditor 渲染（并排
 * + 语法高亮 + 行级着色）、tab 去重。插件零渲染代码——只传两侧内容。
 *
 * 与 FilePanel 同层（workspace panel），不依赖任何插件 view。
 */
import { Suspense, lazy, useEffect, useMemo, useRef } from 'react'
import { ChevronDown, ChevronUp, Loader2 } from 'lucide-react'

import { languageOf } from '@/components/file/fileTypes'
import { attachEditor } from '@/plugin-runtime/editorRegistry'
import type { MonacoDiffEditorHandle } from '@/components/file/MonacoEditor'
import type { PanelProps } from '@/workspace/panels/types'

const MonacoDiffEditor = lazy(() =>
  import('@/components/file/MonacoEditor').then(m => ({ default: m.MonacoDiffEditor })))

/** VSCode 式 diff 导航按钮组（header 内——编辑器外 DOM，不被 monaco 捕获点击）。 */
function DiffNavButtons({ editorRef }: { editorRef: React.RefObject<MonacoDiffEditorHandle | null> }) {
  const btn =
    'flex h-6 w-6 items-center justify-center rounded text-text-secondary transition-colors hover:bg-bg-hover hover:text-text-primary'
  // stopPropagation + preventDefault：阻止点击冒泡/默认行为触达编辑器区域
  //（移动端任何到达 monaco 的事件都可能 focus → 弹软键盘）。
  const nav = (e: React.MouseEvent, dir: 1 | -1) => {
    e.stopPropagation()
    e.preventDefault()
    editorRef.current?.goDiff(dir)
  }
  return (
    <div className="ml-auto flex items-center gap-0.5 rounded-md border border-border bg-bg-secondary/90 p-0.5">
      <button type="button" onClick={(e) => nav(e, -1)} title="上一个差异 (Shift+F7)" className={btn}>
        <ChevronUp className="size-3.5" />
      </button>
      <div className="h-4 w-px bg-border" />
      <button type="button" onClick={(e) => nav(e, 1)} title="下一个差异 (F7)" className={btn}>
        <ChevronDown className="size-3.5" />
      </button>
    </div>
  )
}

export function DiffPanel({ params, api }: PanelProps) {
  const editorRef = useRef<MonacoDiffEditorHandle | null>(null)
  const language = useMemo(
    () => languageOf(params.diffPath || params.title || ''),
    [params.diffPath, params.title],
  )

  // 插件控制：params.editorId 存在时挂载 DiffController（nextDiff/prevDiff/
  // setRenderSideBySide/setTitle/close——plugin-runtime/editorRegistry.ts）。
  const editorId = params.editorId
  useEffect(() => {
    if (!editorId) return
    return attachEditor(editorId, {
      nextDiff: () => editorRef.current?.goDiff(1),
      prevDiff: () => editorRef.current?.goDiff(-1),
      setRenderSideBySide: v => editorRef.current?.setRenderSideBySide(v),
      setTitle: t => api?.setTitle?.(t),
      close: () => api?.close?.(),
    })
  }, [editorId, api])

  // 空内容守卫：original/modified 均为空说明 params 没有到达（持久化布局
  // 恢复丢失 / 链路断裂）——渲染空 DiffEditor 只会得到空白窗口 + dispose
  // 竞态报错，明确提示用户重新打开。
  if (!params.original && !params.modified) {
    return (
      <div className="flex h-full flex-col bg-bg-primary">
        <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1.5">
          <span className="min-w-0 truncate font-mono text-xs text-text-primary">{params.title || params.diffPath}</span>
        </div>
        <div className="flex flex-1 flex-col items-center justify-center gap-2 p-4 text-center text-sm text-text-muted">
          <div>Diff 内容为空</div>
          <div className="text-xs">该 tab 可能来自刷新后的持久化布局（diff 内容不持久化）。请从 Git 面板重新点击文件打开 diff。</div>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg-primary">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1.5">
        <span className="min-w-0 truncate font-mono text-xs text-text-primary">{params.title || params.diffPath}</span>
        {params.diffScope && (
          <span className="shrink-0 rounded bg-bg-tertiary px-1.5 py-0.5 text-[10px] text-text-muted">
            {params.diffScope}
          </span>
        )}
        <DiffNavButtons editorRef={editorRef} />
      </div>
      <div className="min-h-0 flex-1">
        <Suspense
          fallback={
            <div className="flex h-full items-center justify-center">
              <Loader2 className="size-5 animate-spin text-text-muted" />
            </div>
          }
        >
          <MonacoDiffEditor
            ref={editorRef}
            className="h-full w-full"
            original={params.original ?? ''}
            modified={params.modified ?? ''}
            language={language}
            height="100%"
            key={params.diffKey}
          />
        </Suspense>
      </div>
    </div>
  )
}
