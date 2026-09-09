/** Vitest global setup — runs before all tests. */

import { Fragment, createElement } from 'react'
import { vi } from 'vitest'

// Components now consume translations via useI18n() (providers/i18n), which
// throws outside an <I18nProvider>. Tests render components directly without
// the app shell, so provide a global stand-in backed by the real i18n
// singleton (same t() semantics, no React context required). Tests that DO
// wrap with <I18nProvider> keep working — the provider becomes a passthrough.
vi.mock('@/providers/i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/providers/i18n')>()
  const i18n = (await import('@/i18n')).default
  return {
    // 保留真实导出（I18nContext 等）——测试可能直接消费它们。
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => i18n.t(key, params) as string,
      locale: i18n.language || 'zh-CN',
      setLocale: (l: string) => { void i18n.changeLanguage(l) },
    }),
    // Fragment (not a wrapper div): tests assert on container.firstChild, so
    // the provider must not introduce an extra DOM node.
    I18nProvider: ({ children }: { children?: unknown }) => createElement(Fragment, null, children as never),
  }
})

// jsdom does not implement ResizeObserver. Radix primitives using
// @radix-ui/react-use-size (Slider etc.) construct one at mount — without
// this the whole component tree unmounts ("ResizeObserver is not defined").
if (!window.ResizeObserver) {
  window.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

// jsdom does not implement matchMedia. Mock it so hooks using it
// (useIsMobile, useIsTouch) work in tests.
if (!window.matchMedia) {
  window.matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })
}

// jsdom does not implement getClientRects/getBoundingClientRect on Text nodes
// and Range objects (only on Element). ProseMirror (tiptap) calls these for
// scrollIntoView during editor transactions (coordsAtPos → singleRect).
// Polyfill with zero-value rects so editor operations don't throw in tests.
const fakeRect: DOMRect = { top: 0, left: 0, bottom: 0, right: 0, width: 0, height: 0, x: 0, y: 0, toJSON: () => ({}) } as DOMRect
const fakeRectList: DOMRectList = [fakeRect] as unknown as DOMRectList

// Use Object.getOwnPropertyDescriptor to avoid TS errors (Text.prototype
// doesn't declare getClientRects in the DOM type definitions).
for (const Ctor of [Text, Range]) {
  const proto = Ctor.prototype
  if (!Object.getOwnPropertyDescriptor(proto, 'getClientRects')) {
    Object.defineProperty(proto, 'getClientRects', {
      value: function () { return fakeRectList },
      configurable: true,
      writable: true,
    })
  }
  if (!Object.getOwnPropertyDescriptor(proto, 'getBoundingClientRect')) {
    Object.defineProperty(proto, 'getBoundingClientRect', {
      value: function () { return fakeRect },
      configurable: true,
      writable: true,
    })
  }
}
