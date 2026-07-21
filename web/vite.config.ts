/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { VitePWA } from 'vite-plugin-pwa'
import path from 'node:path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    VitePWA({
      registerType: 'autoUpdate',
      injectRegister: false,
      includeAssets: ['favicon.svg', 'icons.svg', 'apple-touch-icon.png', 'pwa-192.png', 'pwa-512.png'],
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
        globPatterns: ['**/*.{js,css,html,svg,png,ico,woff2}'],
        // Precache up to 8MB (monaco/katex/highlight are large but cacheable)
        maximumFileSizeToCacheInBytes: 8 * 1024 * 1024,
        runtimeCaching: [
          // API requests: network-first, fall back to cache for offline reads
          {
            urlPattern: ({ url }) => url.pathname.startsWith('/api/'),
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
    }),
  ],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
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
    // contributions land together in vendor-monaco); mermaid is similarly
    // large. Both load lazily behind their panels, so this is acceptable.
    chunkSizeWarningLimit: 5000,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules')) {
            if (id.includes('/react-dom/') || id.includes('/react/')) return 'vendor-react'
            if (id.includes('/@tiptap/') || id.includes('/tiptap-markdown/')) return 'vendor-tiptap'
            if (id.includes('/monaco-editor/')) return 'vendor-monaco'
            if (id.includes('/react-markdown/') || id.includes('/remark-gfm/')) return 'vendor-markdown'
            if (id.includes('/highlight.js/') || id.includes('/lowlight/')) return 'vendor-highlight'
            if (id.includes('/mermaid/')) return 'vendor-mermaid'
            if (id.includes('/katex/')) return 'vendor-katex'
          }
        },
      },
    },
  },
})
