/// <reference types="vite/client" />
declare const __PWA_ENABLED__: boolean
/** Vercel preview or local XBOT_WEB_PREVIEW=1; never inferred from hostname. */
declare const __PREVIEW_ENABLED__: boolean

// __BUILD_INFO__ — injected at build time by vite.config.ts (define). Carries
// the frontend build identity shown in the About panel: release version (from
// VITE_APP_VERSION in CI, "dev" locally), git commit, and build timestamp.
declare const __BUILD_INFO__: {
  version: string
  channel: string
  commit: string
  buildTime: string
}
