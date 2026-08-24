/**
 * editorTabs —— 插件 editor-view 打开器注册器测试。
 *
 * 覆盖：注册器桥接（openViewTab/openFileTab 走 opener 且参数正确）、
 * 未注册时的安全降级（warn + 空串返回，不抛异常）。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'

import { openEditorFileTab, openEditorViewTab, registerEditorTabOpener } from './editorTabs'

afterEach(() => {
  registerEditorTabOpener(null)
})

describe('editorTabs 注册器', () => {
  it('openViewTab 走已注册的 opener，携带 viewId/viewKey/viewParams', () => {
    const opener = vi.fn(() => 'tab-1')
    registerEditorTabOpener(opener)

    const id = openEditorViewTab({
      viewId: 'xbot.git-fancy.diff',
      title: 'src/a.go',
      icon: 'file-diff',
      key: 'git-diff:worktree:src/a.go',
      params: { path: 'src/a.go', commit: '' },
    })

    expect(id).toBe('tab-1')
    expect(opener).toHaveBeenCalledWith({
      type: 'plugin',
      title: 'src/a.go',
      icon: 'file-diff',
      closable: true,
      data: {
        viewId: 'xbot.git-fancy.diff',
        viewKey: 'git-diff:worktree:src/a.go',
        viewParams: { path: 'src/a.go', commit: '' },
      },
    })
  })

  it('openFileTab 复用宿主文件系统 tab（type=file），data 带 editorId 与初始定位字段，返回 EditorHandle', () => {
    const opener = vi.fn(() => 'tab-2')
    registerEditorTabOpener(opener)

    const h = openEditorFileTab('/repo/src/main.go', { line: 42, highlight: { startLine: 40, endLine: 44 } })

    expect(opener).toHaveBeenCalledWith({
      type: 'file',
      title: 'main.go',
      icon: 'file',
      closable: true,
      data: {
        filePath: '/repo/src/main.go',
        editorId: 'ed-file:/repo/src/main.go',
        initialLine: 42,
        initialHighlight: { startLine: 40, endLine: 44 },
        fileLanguage: undefined,
        fileViewMode: undefined,
      },
    })
    // handle 与 editorId 派生一致（同 path 恒定）
    expect(h.editorId).toBe('ed-file:/repo/src/main.go')
    expect(h.isVisible()).toBe(false) // 测试环境无 panel attach
  })

  it('opener 未注册时安全降级：viewTab 返回空串、fileTab 返回 no-op handle，不抛异常', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(openEditorViewTab({ viewId: 'v', title: 't' })).toBe('')
    const h = openEditorFileTab('/x')
    expect(h.editorId).toBe('ed-file:/x')
    expect(h.isVisible()).toBe(false)
    expect(warn).toHaveBeenCalledTimes(2)
    warn.mockRestore()
  })
})
