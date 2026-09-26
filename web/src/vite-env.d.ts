/// <reference types="vite/client" />

// __BUILD_INFO__ — injected at build time by vite.config.ts (define). Carries
// the frontend build identity shown in the About panel: release version (from
// VITE_APP_VERSION in CI, "dev" locally), git commit, and build timestamp.
declare const __BUILD_INFO__: {
  version: string
  channel: string
  commit: string
  buildTime: string
}

