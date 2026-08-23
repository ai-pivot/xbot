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

  it('openFileTab 复用宿主文件系统 tab（type=file）', () => {
    const opener = vi.fn(() => 'tab-2')
    registerEditorTabOpener(opener)

    openEditorFileTab('/repo/src/main.go')

    expect(opener).toHaveBeenCalledWith({
      type: 'file',
      title: 'main.go',
      icon: 'file',
      closable: true,
      data: { filePath: '/repo/src/main.go' },
    })
  })

  it('opener 未注册时安全降级：返回空串不抛异常', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(openEditorViewTab({ viewId: 'v', title: 't' })).toBe('')
    expect(openEditorFileTab('/x')).toBe('')
    expect(warn).toHaveBeenCalledTimes(2)
    warn.mockRestore()
  })
})
