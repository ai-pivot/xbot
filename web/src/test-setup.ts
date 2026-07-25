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
