# xbot Web UI

> React 19 + TypeScript + Vite single-page application for the xbot AI agent framework.

## Architecture

```
src/
├── i18n/                    # Internationalization
│   ├── index.tsx            # I18nProvider + useTranslation hook + getTranslation helper
│   ├── zh-CN.ts             # Chinese locale (source of truth for I18nKey type)
│   └── en.ts                # English locale
├── contexts/
│   └── ToastContext.tsx      # Global toast notification provider
├── hooks/
│   ├── useWebSocket.ts      # WebSocket with exponential backoff + jitter reconnect
│   ├── useChatMessageHandler.ts  # WS message → state mutations
│   ├── useKeyboardShortcuts.ts   # Global keyboard shortcut handler
│   └── useNetworkStatus.ts  # Network status + toast notifications
├── components/
│   ├── ErrorBoundary.tsx    # Class component error boundary (uses getTranslation)
│   ├── ChatSidebar.tsx      # Session list with search, rename, delete
│   ├── TiptapEditor.tsx     # Rich text editor with Markdown support
│   ├── SearchPanel.tsx      # Message history search with jump-to-result
│   ├── FileUpload.tsx       # Multi-file upload with paste support
│   ├── AskUserPanel.tsx     # Agent question UI (multi-step)
│   ├── ConfirmDialog.tsx    # Reusable confirmation dialog
│   ├── CodeBlock.tsx        # Syntax-highlighted code with lazy language loading
│   ├── MermaidBlock.tsx     # Mermaid diagram renderer with DOMPurify
│   ├── ProgressPanel.tsx    # Tool execution progress display
│   ├── AssistantTurn.tsx    # AI response rendering with thinking/iterations
│   ├── RunnerPanel.tsx      # Remote runner management
│   ├── SettingsPanel.tsx    # Settings drawer with tabs
│   └── settings/
│       ├── shared.ts        # Types, constants, helpers for settings tabs
│       ├── AppearanceTab.tsx
│       ├── SessionsTab.tsx
│       ├── PresetsTab.tsx
│       ├── LLMTab.tsx
│       ├── RunnerTab.tsx
│       └── MarketTab.tsx
├── App.tsx                  # Root: auth gate + ErrorBoundary + ToastProvider
├── ChatPage.tsx             # Main chat view with virtual scrolling
├── LoginPage.tsx            # Username/password + Feishu login
├── main.tsx                 # Entry: I18nProvider → App
├── types.ts                 # Shared TypeScript types
├── utils.ts                 # Utility functions (message grouping, progress)
├── highlight.ts             # hljs lazy language loader
├── webVitals.ts             # Web Vitals collection (dev-only)
└── index.css                # CSS variables + global styles
```

## Key Patterns

### i18n
All user-facing strings are centralized in `src/i18n/zh-CN.ts` (190+ keys). Components use the `useTranslation()` hook to get `t(key, params?)`. For class components (ErrorBoundary), use `getTranslation()`.

Language switching works end-to-end: AppearanceTab language dropdown → `I18nProvider.setLocale()` → all components re-render with new locale.

### Virtual Scrolling
Chat messages use `@tanstack/react-virtual` for efficient rendering of large histories. Messages are grouped into "turns" (user/assistant pairs) via `groupMessagesIntoTurns()`.

### WebSocket
Single WebSocket connection managed by `useWebSocket` hook with:
- Exponential backoff + jitter reconnection
- Sequence-based message deduplication
- Intentional close detection (no reconnect on explicit close)

### CSS
Custom properties (`--xbot-*`) drive theming. Two themes: dark (default) and light. Toggle via `data-theme` attribute on `<html>`.

### Security
- DOMPurify on all user HTML (Mermaid diagrams, markdown)
- `securityLevel: 'strict'` for Mermaid
- hljs output trusted (industry standard)

## Development

```bash
npm install          # Install dependencies
npm run dev          # Start dev server (port 5173)
npm run build        # Production build
npm run lint         # ESLint check
```

## Build Output

| Chunk | Size (gzip) | Notes |
|-------|------------|-------|
| vendor-react | 145 KB | React + React DOM |
| vendor-tiptap | 82 KB | Editor + extensions |
| vendor-highlight | 50 KB | hljs core + lazy languages |
| vendor-markdown | 48 KB | react-markdown + remark |
| vendor-mermaid | 688 KB | Lazy-loaded diagram renderer |
| katex | 77 KB | Math rendering (lazy) |
| index | 26 KB | App code |
| SettingsPanel | 9 KB | Settings (lazy) |

## Optimization History

- **R1**: Component split, CSS modules, type unification, ARIA
- **R2**: Virtual list, DOMPurify, WS handler split, Vite manualChunks
- **R3**: Toast system, hljs lazy-load, tablet adapt, global shortcuts, WS reconnect cap
- **R4**: CSS variables, prefers-reduced-motion, drag-drop, image preview, ConfirmDialog, sidebar search
- **R5**: ErrorBoundary, useNetworkStatus, i18n constants, PWA manifest, Web Vitals, focus-visible, skip-to-content
- **R6**: Full i18n implementation (useTranslation + en.ts), bug fixes (render-time setState, array bounds, error handling), lint cleanup, type safety
