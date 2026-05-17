import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8082',
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://localhost:8082',
        ws: true,
        changeOrigin: true,
      },
    },
  },
  build: {
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules')) {
            if (id.includes('/react-dom/') || id.includes('/react/')) return 'vendor-react'
            if (id.includes('/@tiptap/') || id.includes('/tiptap-markdown/')) return 'vendor-tiptap'
            if (id.includes('/react-markdown/') || id.includes('/remark-gfm/')) return 'vendor-markdown'
            if (id.includes('/highlight.js/') || id.includes('/lowlight/')) return 'vendor-highlight'
            if (id.includes('/mermaid/')) return 'vendor-mermaid'
          }
        },
      },
    },
  },
})
