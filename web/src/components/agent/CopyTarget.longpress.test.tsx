import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import i18n from '@/i18n'
import { I18nProvider } from '@/providers/i18n'
import { CopyTarget } from './MessageActions'

// 菜单标签走 i18n（agent.copyMenu.*）⇒ ① 组件必须在 I18nProvider 内渲染（useI18n 无 Provider 会 throw）；
// ② 断言语言两侧钉死（jsdom 的 navigator.language 是 en-US，不钉就断言到英文标签）。
beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

// 2026-09-16 用户报告：
//   ② 「你没给手机复制 user msg 的交互」
//   ③ 「手机长按老是变出那个蓝色选中判定，位置还根本不对」
// ② 根因：长按判定 onPointerMove **任何位移都取消计时**（无容差），触屏手指必然抖动
//    一两像素 ⇒ 480ms 计时几乎永远被清掉 ⇒ 手机上长按不出复制菜单。
// ③ 根因：消息内容是可选文本，长按触发**浏览器原生**选择（蓝色高亮 + 原生气泡），
//    其位置在虚拟滚动 + transform 容器里不受我们控制；触屏上必须抑制，把长按让给复制菜单。
const device = { touch: true }
vi.mock('@/hooks/useIsMobile', () => ({
  useIsTouch: () => device.touch,
  useIsMobile: () => false,
}))

const child = <span>target</span>

function menuOpen() {
  return document.querySelector('[data-testid="copy-sheet"], [data-testid="copy-menu"]')
}

function renderTarget() {
  const { container } = render(
    <I18nProvider>
      <CopyTarget kind="message" message={{ role: 'user', content: 'hi' } as never}>
        {child}
      </CopyTarget>
    </I18nProvider>,
  )
  return container.querySelector('[data-copy-target="message"]') as HTMLElement
}

/** 假定时器下必须用 act() 包住时间推进，否则 React 状态更新不会被 flush。 */
function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms)
  })
}

describe('CopyTarget 长按（触屏抖动容差）', () => {
  beforeEach(() => {
    device.touch = true
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('手指轻微抖动（4px）后长按仍能弹出复制菜单', () => {
    const node = renderTarget()
    fireEvent.pointerDown(node, { pointerType: 'touch', clientX: 100, clientY: 100 })
    // 抖动：旧实现在这里就 clear() 了，菜单永远不会出现
    fireEvent.pointerMove(node, { pointerType: 'touch', clientX: 104, clientY: 103 })
    advance(520)
    expect(menuOpen()).not.toBeNull()
  })

  it('真正划动（>10px）时取消长按，不弹菜单', () => {
    const node = renderTarget()
    fireEvent.pointerDown(node, { pointerType: 'touch', clientX: 100, clientY: 100 })
    fireEvent.pointerMove(node, { pointerType: 'touch', clientX: 140, clientY: 100 })
    advance(520)
    expect(menuOpen()).toBeNull()
  })

  it('鼠标按下不触发长按，右键才弹菜单', () => {
    const node = renderTarget()
    fireEvent.pointerDown(node, { pointerType: 'mouse', clientX: 10, clientY: 10 })
    advance(600)
    expect(menuOpen()).toBeNull()

    // 右键是桌面入口（菜单项里必然有"复制…"，且不止一项）
    fireEvent.contextMenu(node, { clientX: 10, clientY: 10 })
    expect(menuOpen()).not.toBeNull()
    expect(screen.getAllByText(/复制/).length).toBeGreaterThan(0)
  })

  it('③ 触屏：复制面禁用原生选择与 callout（长按不再弹蓝色选中控件）', () => {
    const node = renderTarget()
    expect(node.className).toContain('select-none')
    expect(node.className).toContain('-webkit-touch-callout')
    // min-w-0 不能被丢掉（2026-09-15 的教训）
    expect(node.className).toContain('min-w-0')
  })

  it('③ 桌面：不抑制原生选择（拖选文本仍可用）', () => {
    device.touch = false
    const node = renderTarget()
    expect(node.className).not.toContain('select-none')
  })

  // 触屏长按的**落点**同样要给链接入口（PR #398）：桌面右键路径已有单测 + E2E，
  // 但长按路径此前零守护 —— 把长按回调的 target 置空（等价于"手机长按链接再也出不来
  // 『打开链接』"）时，全量单测与 E2E 会**全绿**（CR 实测），因此这条必须有。
  it('触屏长按落在链接上 → 菜单含「打开链接 / 复制链接地址」', () => {
    render(
      <I18nProvider>
        <CopyTarget
          kind="message"
          message={
            { id: 'm1', role: 'assistant', content: '', iterations: [], isPartial: false, turnID: 1, timestamp: '' } as never
          }
        >
          <p>
            参考 <a href="https://example.com/bench">新的基准评测</a>
          </p>
        </CopyTarget>
      </I18nProvider>,
    )
    fireEvent.pointerDown(screen.getByText('新的基准评测'), {
      pointerType: 'touch',
      clientX: 120,
      clientY: 300,
    })
    advance(520)
    const menu = menuOpen()
    expect(menu).not.toBeNull()
    expect(menu?.textContent ?? '').toContain('打开链接')
    expect(menu?.textContent ?? '').toContain('复制链接地址')
  })

  // ── 触屏泄漏守护（2026-09-30 用户报告「有些情况手机无法划动虚拟视图的历史，
  //    打开侧边栏后恢复正常」的根因）────────────────────────────────────────
  //
  // 根因：copy-backdrop（fixed inset-0 z-40 透明遮罩）的关闭路径只有 mousedown /
  // wheel / contextmenu —— 触屏一个都不触发（触摸兼容模型：滑动不合成 mousedown，
  // Android 长按后的抬手也不合成）。遮罩一旦泄漏：新手势命中遮罩（z-40 盖全屏），
  // 遮罩 portal 到 body ⇒ 无滚动祖先 ⇒ 历史划不动；点侧边栏按钮的 tap 合成
  // mousedown ⇒ 命中遮罩把它点掉 ⇒ click 落到按钮上 ⇒ 「打开侧边栏后恢复正常」。
  //
  // 修复两层（下面两条各自守护一层）：
  //   a) 长按已开菜单后**继续拖动** >10px ⇒ 菜单自行关闭（长按 = 误触发）；
  //   b) 遮罩补 onTouchStart ⇒ 任何新手势（tap/划）一触即关。
  it('长按已开菜单后继续拖动 >10px ⇒ 菜单与遮罩必须关闭（长按 = 误触发，拖走 = 用户想滚动）', () => {
    const node = renderTarget()
    fireEvent.pointerDown(node, { pointerType: 'touch', clientX: 100, clientY: 100 })
    advance(520)
    expect(menuOpen(), '长按 480ms 后菜单应打开（前置条件）').not.toBeNull()

    // 同一手势继续拖动 40px（超容差）：用户其实是在滚动 —— 菜单必须自行关闭，
    // 否则遮罩泄漏、历史划不动（正是用户报告的症状）。
    fireEvent.pointerMove(node, { pointerType: 'touch', clientX: 100, clientY: 140 })
    expect(menuOpen(), '拖走后菜单必须关闭 —— 否则透明遮罩泄漏并冻结历史滚动').toBeNull()
  })

  it('菜单打开时新触摸落在遮罩上 ⇒ 遮罩关闭（划动/点击的第一下 touchstart 即关）', () => {
    const node = renderTarget()
    fireEvent.pointerDown(node, { pointerType: 'touch', clientX: 100, clientY: 100 })
    advance(520)
    const backdrop = document.querySelector('[data-testid="copy-backdrop"]')
    expect(backdrop, '前置条件：菜单打开时透明遮罩应存在').not.toBeNull()

    // 新手势（抬手后重新触摸历史区域）：touchstart 命中遮罩 —— 必须立即关闭。
    // 这正是用户「划历史没反应」的那一下：遮罩不关，本次手势的目标被锁在遮罩上（划不动）。
    fireEvent.touchStart(backdrop as HTMLElement)
    expect(menuOpen(), '遮罩收到触摸必须关闭（划动的第一下就是 touchstart）').toBeNull()
    expect(document.querySelector('[data-testid="copy-backdrop"]')).toBeNull()
  })
})
