/**
 * Touch devices (mobile web): fold/collapse height animations must be DISABLED.
 *
 * grid-template-rows (0fr→1fr) and the collapsible-motion height keyframes are
 * LAYOUT animations — every frame reflows the composer region. TodoPullOut
 * (the todo panel above MessageInput) and the folded tool/thinking groups sit
 * inside/around the message list layout, so each animation frame also resizes
 * the TanStack Virtual viewport (remeasure + scroll adjust) — on phones the
 * todo panel expand visibly janks (reported case).
 *
 * Fix (web/src/index.css): @media (hover: none) drops the height animation on
 * touch devices (height snaps in ONE reflow) and keeps only the opacity fade
 * (compositor-only). Desktop (hover: hover) keeps the full animation.
 *
 * CSS-only probe: index.css is bundled into every route (the login page
 * included), so we inject probe elements and read computed styles — no
 * backend data flow needed.
 */
import { test, expect } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

async function setupAuthMock(page: import('@playwright/test').Page) {
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
}

// Inject probe elements and read their computed animation/transition styles.
const probe = () => {
  const mk = (cls: string, attrs: Record<string, string> = {}) => {
    const el = document.createElement('div')
    el.className = cls
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v)
    document.body.appendChild(el)
    const st = getComputedStyle(el)
    const r = { transition: st.transitionProperty, animation: st.animationName }
    el.remove()
    return r
  }
  return {
    // AnimatedCollapse wrapper (TodoPullOut / FoldedLine / ToolGroup / ThinkingLine)
    fold: mk('fold-container'),
    // Radix collapsible height keyframes (data-state=open plays collapsibleOpen)
    collapsible: mk('collapsible-motion', { 'data-state': 'open' }),
    hoverNone: matchMedia('(hover: none)').matches,
  }
}

test('mobile (hover:none): fold-container drops the grid height animation — todo panel expand jank fix', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 375, height: 812 }, hasTouch: true, isMobile: true })
  await setupAuthMock(page)
  await page.goto(`${BASE}/login`)
  const r = await page.evaluate(probe)
  console.log('MOBILE FOLD CSS:', JSON.stringify(r))

  // Confirm the media-query context actually matched (mobile emulation).
  expect(r.hoverNone).toBe(true)
  // The grid-template-rows LAYOUT transition must be gone on touch —
  // expanding the todo panel must snap in one reflow, not animate N frames
  // of composer-layout + virtualizer resize.
  expect(r.fold.transition).not.toContain('grid-template-rows')
  // The opacity fade (compositor-only) is kept for a gentle reveal.
  expect(r.fold.transition).toContain('opacity')
  // Radix collapsible height keyframes are layout animations too — off on touch.
  expect(r.collapsible.animation).toBe('none')
  await page.close()
})

test('desktop (hover:hover): fold height animation retained (no regression)', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } })
  await setupAuthMock(page)
  await page.goto(`${BASE}/login`)
  const r = await page.evaluate(probe)
  console.log('DESKTOP FOLD CSS:', JSON.stringify(r))

  expect(r.hoverNone).toBe(false)
  // Desktop keeps the smooth grid disclosure animation.
  expect(r.fold.transition).toContain('grid-template-rows')
  expect(r.collapsible.animation).toContain('collapsibleOpen')
  await page.close()
})
