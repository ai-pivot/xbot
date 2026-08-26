/** Vitest global setup — runs before all tests. */

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
