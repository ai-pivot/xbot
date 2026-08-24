/**
 * editorRegistry —— 插件编辑器控制注册表测试。
 *
 * 覆盖：确定性 editorId 派生、attach/detach 生命周期、handle 方法路由、
 * tab 未挂载时 no-op（返回 false / isVisible=false）、onClose 广播、
 * 新实例覆盖旧实例（dockview 复用 panel 时的 detach 不误删新实例）。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  attachEditor,
  createDiffHandle,
  createEditorHandle,
  editorIdForDiff,
  editorIdForFile,
  type EditorController,
} from './editorRegistry'

afterEach(() => {
  // 清空注册表（每个用例独立）
  for (const id of ['ed-file:/repo/a.ts', 'ed-diff:git-diff:abc']) {
    const h = createEditorHandle(id)
    h.close()
  }
})

describe('editorRegistry', () => {
  it('editorId 确定性派生：同 path/diffKey 恒定', () => {
    expect(editorIdForFile('/repo/src/a.ts')).toBe('ed-file:/repo/src/a.ts')
    expect(editorIdForFile('/repo/src/a.ts')).toBe(editorIdForFile('/repo/src/a.ts'))
    expect(editorIdForDiff('git-diff:abc:src/a.ts')).toBe('ed-diff:git-diff:abc:src/a.ts')
    // openFileTab 的显式 key 覆盖派生
    expect(editorIdForFile('my-key')).toBe('ed-file:my-key')
  })

  it('未挂载时 handle 全部 no-op：isVisible=false、方法返回 false、getContent=null', () => {
    const h = createEditorHandle('ed-file:/repo/none.ts')
    expect(h.isVisible()).toBe(false)
    expect(h.revealLine(10)).toBe(false)
    expect(h.highlightLines(1, 5)).toBe(false)
    expect(h.setContent('x')).toBe(false)
    expect(h.getContent()).toBeNull()
    expect(h.close()).toBe(false)
    const dh = createDiffHandle('ed-diff:none')
    expect(dh.nextDiff()).toBe(false)
    expect(dh.isVisible()).toBe(false)
  })

  it('attach 后 handle 方法路由到 controller；detach 后回到 no-op', () => {
    const calls: string[] = []
    const real: EditorController = {
      revealLine: (l) => calls.push(`revealLine:${l}`),
      revealRange: (s, e) => calls.push(`revealRange:${s}-${e}`),
      setSelection: (...a) => calls.push(`setSelection:${a.join(',')}`),
      setCursorPosition: (l, c) => calls.push(`cursor:${l}:${c}`),
      highlightLines: (s, e) => calls.push(`hl:${s}-${e}`),
      clearHighlights: () => calls.push('clearHl'),
      getContent: () => 'file-content',
      setContent: (t) => calls.push(`setContent:${t.length}`),
      setLanguage: (l) => calls.push(`lang:${l}`),
      setTitle: (t) => calls.push(`title:${t}`),
      setViewMode: (m) => calls.push(`mode:${m}`),
      close: () => calls.push('close'),
    }
    const id = 'ed-file:/repo/a.ts'
    const detach = attachEditor(id, real)
    const h = createEditorHandle(id)
    expect(h.isVisible()).toBe(true)

    expect(h.revealLine(42)).toBe(true)
    expect(h.revealLine(43, { center: false })).toBe(true)
    expect(h.highlightLines(10, 20)).toBe(true)
    expect(h.getContent()).toBe('file-content')
    expect(h.setContent('hello')).toBe(true)
    expect(h.setLanguage('go')).toBe(true)
    expect(h.close()).toBe(true)
    expect(calls).toEqual([
      'revealLine:42', 'revealLine:43', 'hl:10-20',
      'setContent:5', 'lang:go', 'close',
    ])

    detach()
    expect(h.isVisible()).toBe(false)
    expect(h.revealLine(1)).toBe(false)
  })

  it('onClose 在 detach 时广播（tab 关闭通知插件）', () => {
    const id = 'ed-file:/repo/a.ts'
    const cb = vi.fn()
    const real = makeNoopController()
    const detach = attachEditor(id, real)
    createEditorHandle(id).onClose(cb)
    detach()
    expect(cb).toHaveBeenCalledTimes(1)
  })

  it('新实例覆盖：detach 旧的不误删新注册（dockview panel params 更换实例）', () => {
    const id = 'ed-file:/repo/a.ts'
    const c1 = makeNoopController()
    const c2 = makeNoopController()
    const detach1 = attachEditor(id, c1)
    const detach2 = attachEditor(id, c2)
    // 旧实例卸载——controller 不同，不得删除新注册
    detach1()
    expect(createEditorHandle(id).isVisible()).toBe(true)
    detach2()
    expect(createEditorHandle(id).isVisible()).toBe(false)
  })
})

function makeNoopController(): EditorController {
  return {
    revealLine: () => {},
    revealRange: () => {},
    setSelection: () => {},
    setCursorPosition: () => {},
    highlightLines: () => {},
    clearHighlights: () => {},
    getContent: () => null,
    setContent: () => {},
    setLanguage: () => {},
    setTitle: () => {},
    setViewMode: () => {},
    close: () => {},
  }
}
