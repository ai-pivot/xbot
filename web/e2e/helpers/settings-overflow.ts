/** Runs inside the page. Check visible text/control geometry, not scrollWidth polluted by closed drawers. */
export function collectSettingsOverflow(): string[] {
  const root = document.querySelector('[data-slot="dialog-content"]')
  if (!root) return ['Settings dialog is missing']
  const problems: string[] = []
  const scrollable = (value: string) => value === 'auto' || value === 'scroll'
  const clips = (value: string) => value === 'hidden' || value === 'clip'
  for (const el of root.querySelectorAll<HTMLElement>('*')) {
    if (!el.getClientRects().length || el.closest('.sr-only, [hidden], [inert], [aria-hidden="true"]')) continue
    const ancestors: HTMLElement[] = []
    for (let node: HTMLElement | null = el; node && node !== document.body; node = node.parentElement) ancestors.push(node)
    if (ancestors.some(node => {
      const style = getComputedStyle(node)
      if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return true
      // A translated drawer entirely outside its clipping parent is an intentional inactive view.
      if (style.transform !== 'none') {
        const rect = node.getBoundingClientRect()
        for (let parent = node.parentElement; parent; parent = parent.parentElement) {
          const ps = getComputedStyle(parent), pr = parent.getBoundingClientRect()
          if ((clips(ps.overflowX) || scrollable(ps.overflowX)) && (rect.left >= pr.right - 1 || rect.right <= pr.left + 1)) return true
        }
      }
      return false
    })) continue
    if (ancestors.some(node => getComputedStyle(node).textOverflow === 'ellipsis')) continue
    const style = getComputedStyle(el)
    if (Number.parseInt(style.webkitLineClamp, 10) > 0) continue
    const boxes: DOMRect[] = []
    if (el.matches('input:not([type="hidden"]), textarea, select, button')) boxes.push(el.getBoundingClientRect())
    for (const node of el.childNodes) {
      if (node.nodeType !== Node.TEXT_NODE || !node.textContent?.trim()) continue
      const range = document.createRange(); range.selectNodeContents(node)
      boxes.push(...Array.from(range.getClientRects()))
    }
    for (const box of boxes) {
      if (!box.width || !box.height) continue
      const outsideViewportX = box.left < -1 || box.right > innerWidth + 1
      let clippedX = false, clippedY = false
      let accessibleX = false, accessibleY = false
      for (const ancestor of ancestors) {
        const st = getComputedStyle(ancestor), rect = ancestor.getBoundingClientRect()
        if (scrollable(st.overflowX)) accessibleX = true
        if (scrollable(st.overflowY)) accessibleY = true
        // An outer scroller cannot make content clipped by an inner element reachable.
        if (!accessibleX && clips(st.overflowX) && (box.left < rect.left - 2 || box.right > rect.right + 2)) clippedX = true
        if (!accessibleY && clips(st.overflowY) && (box.top < rect.top - 2 || box.bottom > rect.bottom + 2)) clippedY = true
      }
      if (clippedX || clippedY || (outsideViewportX && !accessibleX)) {
        problems.push(`${el.tagName.toLowerCase()} ${(el.textContent ?? el.getAttribute('aria-label') ?? '').trim().slice(0, 40)}: ${clippedX || (outsideViewportX && !accessibleX) ? 'horizontal' : 'vertical'} clipping`)
        break
      }
    }
  }
  return problems
}
