/**
 * index.html viewport 契约 —— 安卓软键盘适配（2026-09-27）。
 *
 * `interactive-widget=resizes-content`：Chrome 108+ / Firefox Android 132+ 在
 * 软键盘弹出时缩小【布局视口】（默认 resizes-visual 只缩 visual viewport，
 * fixed inset-0 的输入框仍锚定被键盘盖住的布局视口底部 —— 用户报告
 * 「安卓浏览器键盘盖住输入框」的根因之一）。不认识该参数的浏览器会忽略它
 * （AppShell/MobileAppShell 的 useKeyboardInset 补偿兜底，两机制天然互斥）。
 *
 * 其余既有字段（viewport-fit=cover 的 iOS 安全区、禁缩放）一并守护，防止
 * 未来有人"清理"时把关键字段删掉。
 */
import { describe, expect, it } from 'vitest'
// Vite `?raw` 导入：构建/测试管线统一（build 的 tsc 无 @types/node，不能用 node:fs）。
import html from '../index.html?raw'

function viewportMeta(): string {
  const m = html.match(/<meta\s+name="viewport"\s+content="([^"]*)"\s*\/?/)
  if (!m) throw new Error('viewport meta tag not found in index.html')
  return m[1]
}

describe('index.html viewport — 移动端键盘/安全区契约', () => {
  it('含 interactive-widget=resizes-content（安卓键盘弹出时缩小布局视口）', () => {
    expect(viewportMeta()).toContain('interactive-widget=resizes-content')
  })

  it('保留 viewport-fit=cover（iOS 安全区 + fixed inset-0 全屏出血的前提）', () => {
    expect(viewportMeta()).toContain('viewport-fit=cover')
  })

  it('保留禁缩放字段（输入体验：聚焦输入框不触发页面捏合缩放）', () => {
    expect(viewportMeta()).toContain('user-scalable=no')
    expect(viewportMeta()).toContain('maximum-scale=1.0')
  })
})
