/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
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
