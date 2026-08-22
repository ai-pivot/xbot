/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { VitePWA } from 'vite-plugin-pwa'
import path from 'node:path'

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  // dev 构建（--mode development）禁用 PWA：SW 会缓存 debug 产物导致
  // 拿旧代码，且未压缩的 ts.worker 超 workbox 大小限制。debug 场景下
  // 前端应走网络，SW 反而干扰。
  const isDev = mode === 'development'
  const plugins = [
    react(),
    tailwindcss(),
  ]
  if (!isDev) {
    plugins.push(VitePWA({
      registerType: 'autoUpdate',
      injectRegister: false,
      // SW 文件名带版本：sw.js 曾被 server 无 Cache-Control地下发，浏览器
      // heuristic 缓存了旧 SW（24h fresh）→ 更新检查永不回源 → 新 SW 永不
      // 安装 → 用户永远跑旧 bundle。换 URL 绕开污染缓存（server 现已 no-store）。
      filename: 'sw2.js',
      includeAssets: ['favicon-48.png', 'icons.svg', 'apple-touch-icon.png', 'pwa-192.png', 'pwa-512.png'],
      manifest: {
        name: 'xbot',
        short_name: 'xbot',
        description: 'AI 智能对话助手',
        start_url: '/',
        scope: '/',
        display: 'standalone',
        background_color: '#1e1e1e',
        theme_color: '#1e1e1e',
        orientation: 'any',
        icons: [
          { src: '/pwa-192.png', sizes: '192x192', type: 'image/png' },
          { src: '/pwa-512.png', sizes: '512x512', type: 'image/png' },
          { src: '/pwa-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
          { src: '/favicon.svg', sizes: 'any', type: 'image/svg+xml' },
        ],
      },
      workbox: {
        // 不 precache HTML —— SW 缓存旧 index.html 会导致版本倒退
        // （旧 HTML 引用旧 JS hash，SW 拦截导航返回旧 HTML → 加载旧 JS）。
        // HTML 始终走网络（NetworkFirst），JS/CSS/assets 走 precache。
        globPatterns: ['**/*.{js,css,svg,png,ico,woff2}'],
        globIgnores: ['**/index.html'],
        // 禁用 Workbox 的 NavigationRoute（createHandlerBoundToURL）——
        // 它用 precache 的 index.html 拦截导航请求，导致版本倒退。
        // 导航请求由 runtimeCaching 的 NetworkFirst 处理。
        navigateFallback: null,
        // Auto-activate new SW without waiting for page message — breaks the
        // chicken-and-egg cycle where old SW caches old HTML that can't send
        // SKIP_WAITING. clientsClaim takes control of existing tabs immediately.
        skipWaiting: true,
        clientsClaim: true,
        // Precache up to 8MB (monaco/katex/highlight are large but cacheable)
        maximumFileSizeToCacheInBytes: 8 * 1024 * 1024,
        runtimeCaching: [
          // HTML (navigation): NetworkFirst —— 始终从服务器获取最新 index.html，
          // 网络失败时才用缓存。这防止 SW 缓存旧 HTML（引用旧 JS hash）导致
          // 版本倒退（用户报告：普通刷新后版本回退到旧 JS）。
          {
            urlPattern: ({ request }) => request.mode === 'navigate',
            handler: 'NetworkFirst',
            options: {
              cacheName: 'html-cache',
              networkTimeoutSeconds: 3,
              cacheableResponse: { statuses: [0, 200] },
            },
          },
          // API requests: network-first, fall back to cache for offline reads.
          // Exclude /api/sse — SSE is a streaming response that never
          // completes normally; caching it throws "Cache.put() network error"
          // when the connection drops (which is normal for long-lived SSE).
          {
            urlPattern: ({ url }) =>
              url.pathname.startsWith('/api/') && !url.pathname.startsWith('/api/sse'),
            handler: 'NetworkFirst',
            options: {
              cacheName: 'api-cache',
              networkTimeoutSeconds: 5,
              expiration: { maxEntries: 100, maxAgeSeconds: 300 },
              cacheableResponse: { statuses: [0, 200] },
            },
          },
          // Static assets (fonts, images): cache-first, long TTL
          {
            urlPattern: ({ request }) =>
              request.destination === 'font' ||
              request.destination === 'image' ||
              request.destination === 'style',
            handler: 'CacheFirst',
            options: {
              cacheName: 'static-assets',
              expiration: { maxEntries: 60, maxAgeSeconds: 30 * 24 * 60 * 60 },
              cacheableResponse: { statuses: [0, 200] },
            },
          },
        ],
      },
      devOptions: {
        enabled: false,
      },
    }))
  }

  return {
    plugins,
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    setupFiles: ['src/test-setup.ts'],
  },
  server: {
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8082',
        changeOrigin: true,
        secure: true,
      },
      '/ws': {
        target: 'ws://127.0.0.1:8082',
        ws: true,
        changeOrigin: true,
        secure: true,
      },
    },
  },
  build: {
    // Raise chunk size warning limit. Monaco is a large single chunk (its
    // language workers are code-split, but the core + bundled language
    // contributions land together in vendor-monaco). It loads lazily behind
    // the FilePanel, so this is acceptable.
    chunkSizeWarningLimit: 5000,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules')) {
            if (id.includes('/react-dom/') || id.includes('/react/')) return 'vendor-react'
            if (id.includes('/monaco-editor/')) return 'vendor-monaco'
            if (id.includes('/react-markdown/') || id.includes('/remark-gfm/')) return 'vendor-markdown'
            if (id.includes('/highlight.js/')) return 'vendor-highlight'
            if (id.includes('/katex/')) return 'vendor-katex'
            // Split medium-sized libraries into their own chunks to reduce
            // the main entry chunk (was 4.4MB). These are loaded on demand
            // or are only needed for specific features.
            if (id.includes('/framer-motion/')) return 'vendor-framer-motion'
            if (id.includes('/dockview/')) return 'vendor-dockview'
            if (id.includes('/@tiptap/') || id.includes('/tiptap-markdown/')) return 'vendor-tiptap'
            if (id.includes('/i18next/') || id.includes('/react-i18next/')) return 'vendor-i18n'
            if (id.includes('/xterm/')) return 'vendor-xterm'
          }
        },
      },
    },
  },
  }
})
