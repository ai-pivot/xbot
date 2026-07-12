# xbot Web UI

> React 19 + TypeScript + Vite single-page application for the xbot AI agent framework.

## Architecture

```
src/
├── i18n/                    # Internationalization (zh-CN, en)
│   ├── index.tsx            # I18nProvider + useTranslation hook
│   ├── zh-CN.ts             # Chinese locale (source of truth)
│   └── en.ts                # English locale
├── contexts/
│   ├── ToastContext.tsx      # Global toast notification provider
│   ├── MediaPlayerContext.tsx # Shared media player state (mutex)
│   └── NotificationContext.tsx # Desktop notification provider
├── hooks/
│   ├── useWebSocket.ts      # WebSocket with exponential backoff + jitter
│   ├── useChatMessageHandler.ts  # WS message → state mutations
│   ├── useKeyboardShortcuts.ts   # Global keyboard shortcuts
│   ├── useNetworkStatus.ts  # Network status monitoring
│   ├── useNotification.ts   # Desktop notification permissions
│   ├── useVimNavigation.ts  # Vim-style j/k navigation
│   └── useBookmarks.ts      # Bookmark persistence (localStorage)
├── components/
│   ├── ErrorBoundary.tsx    # Class component error boundary
│   ├── ChatSidebar.tsx      # Session list with search, rename, delete
│   ├── TiptapEditor.tsx     # Rich text editor with Markdown
│   ├── SearchPanel.tsx      # Message history search with highlighting
│   ├── FileUpload.tsx       # Multi-file upload with drag-drop + paste
│   ├── UploadQueue.tsx      # Upload progress queue display
│   ├── AskUserPanel.tsx     # Agent question UI (multi-step)
│   ├── ConfirmDialog.tsx    # Reusable confirmation dialog
│   ├── CodeBlock.tsx        # Syntax-highlighted code with collapse + line numbers
│   ├── MermaidBlock.tsx     # Mermaid diagram renderer with DOMPurify
│   ├── ProgressPanel.tsx    # Tool execution progress with drag support
│   ├── AssistantTurn.tsx    # AI response rendering (thinking/iterations/collapsible)
│   ├── RunnerPanel.tsx      # Remote runner management
│   ├── SettingsPanel.tsx    # Settings drawer with tabs
│   ├── CommandPalette.tsx   # Ctrl+K command palette
│   ├── SearchPanel.tsx      # Global search with filters
│   ├── NotificationPanel.tsx # Notification center
│   ├── BookmarkPanel.tsx    # Bookmark management panel
│   ├── MediaPlayer.tsx      # Audio/video player with fullscreen + PiP
│   ├── Lightbox.tsx         # Image lightbox viewer
│   ├── ThemeEditor.tsx      # Custom theme editor
│   ├── ThreadPanel.tsx      # Thread/conversation panel
│   ├── ContextMenu.tsx      # Right-click context menu
│   ├── MessageReactions.tsx # Emoji reactions
│   ├── MessageActions.tsx   # Message action buttons
│   ├── CollapsibleContent.tsx # Collapsible content wrapper
│   ├── TabBar.tsx           # Draggable tab bar
│   ├── OnboardingTip.tsx    # First-time user tips
│   ├── SnapshotShare.tsx    # Chat snapshot sharing
│   ├── SwipeableMessage.tsx # Swipeable message cards
│   ├── ReplyPreview.tsx     # Reply/quote preview
│   └── settings/
│       ├── shared.ts        # Types, constants, helpers
│       ├── AppearanceTab.tsx # Theme, language, font settings
│       ├── SessionsTab.tsx  # Session management
│       ├── PresetsTab.tsx   # Quick command presets
│       ├── LLMTab.tsx       # Personal LLM configuration
│       └── RunnerTab.tsx    # Runner monitoring
├── styles/                  # CSS organized by feature
│   ├── base.css             # CSS variables + reset
│   ├── index.css            # Main stylesheet imports
│   ├── codeblock.css        # Code block styling
│   ├── markdown.css         # Markdown content styling
│   ├── mobile.css           # Mobile responsive overrides
│   ├── settings.css         # Settings panel styles
│   ├── theme-editor.css     # Theme editor styles
│   ├── sidebar.css          # Sidebar styles
│   ├── search.css           # Search panel styles
│   ├── login.css            # Login page styles
│   ├── progress.css         # Progress panel styles
│   ├── runner.css           # Runner panel styles
│   ├── bookmark.css         # Bookmark panel styles
│   ├── lightbox.css         # Lightbox viewer styles
│   ├── confirm.css          # Confirm dialog styles
│   ├── askuser.css          # Ask user panel styles
│   ├── tiptap.css           # Tiptap editor styles
│   └── chat/                # Chat-specific styles
│       ├── assistant-turn.css
│       ├── animations.css
│       ├── command-palette.css
│       ├── context-menu.css
│       ├── file-upload.css
│       ├── media-player.css
│       ├── notification-center.css
│       ├── onboarding.css
│       ├── preset.css
│       ├── reactions.css
│       ├── reply.css
│       ├── scroll-btn.css
│       ├── snapshot.css
│       ├── swipeable-message.css
│       ├── tabbar.css
│       ├── thread.css
│       ├── toast.css
│       ├── token-usage.css
│       └── user-message.css
├── App.tsx                  # Root: auth gate + ErrorBoundary + ToastProvider
├── ChatPage.tsx             # Main chat view with virtual scrolling
├── LoginPage.tsx            # Multi-method login (password + Feishu)
├── main.tsx                 # Entry: I18nProvider → App
├── types.ts                 # Shared TypeScript types
├── constants.ts             # Shared constants (truncation limits)
├── utils.ts                 # Utility functions
├── highlight.ts             # hljs lazy language loader
└── webVitals.ts             # Web Vitals collection
```

## Key Patterns

### i18n
All user-facing strings are centralized in `src/i18n/zh-CN.ts`. Components use the `useTranslation()` hook to get `t(key, params?)`. For class components (ErrorBoundary), use `getTranslation()`.

Language switching works end-to-end: AppearanceTab language dropdown → `I18nProvider.setLocale()` → all components re-render with new locale.

### Virtual Scrolling
Chat messages use `@tanstack/react-virtual` for efficient rendering of large histories. Messages are grouped into "turns" (user/assistant pairs) via `groupMessagesIntoTurns()`.

### WebSocket
Single WebSocket connection managed by `useWebSocket` hook with:
- Exponential backoff + jitter reconnection
- Sequence-based message deduplication
- Intentional close detection (no reconnect on explicit close)

### CSS Architecture
Styles organized by feature under `src/styles/`. Custom properties (`--xbot-*`) drive theming. Two built-in themes: dark (default) and light, plus custom theme editor. Toggle via `data-theme` attribute on `<html>`.

### State Management
- **React Context** for global state (Toast, MediaPlayer mutex, Notifications)
- **localStorage** for persistence (bookmarks, theme preferences, onboarding state)
- **Custom hooks** for encapsulated stateful logic (WebSocket, keyboard, navigation)

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
| vendor-mermaid | 764 KB | Lazy-loaded diagram renderer |
| vendor-react | 145 KB | React + React DOM |
| vendor-tiptap | 82 KB | Editor + extensions |
| vendor-katex | 77 KB | Math rendering (lazy) |
| vendor-highlight | 50 KB | hljs core + lazy languages |
| vendor-markdown | 49 KB | react-markdown + remark |
| index | 54 KB | App code |
| SettingsPanel | 10 KB | Settings (lazy) |
| CommandPalette | 2 KB | Command palette (lazy) |

## Testing

- **Framework**: Vitest + @testing-library/react + jsdom
- **Coverage**: 307 tests across 15 test files
- **Run**: `npx vitest run`
- **Watch**: `npx vitest`

## Optimization History

- **R1**: Component split, CSS modules, type unification, ARIA
- **R2**: Virtual list (tanstack/react-virtual), DOMPurify sanitization, WS handler split, Vite manualChunks optimization
- **R3**: Toast system, hljs lazy-load, tablet adapt, global shortcuts, WS reconnect cap
- **R4**: CSS variables (dark/light themes), prefers-reduced-motion, drag-drop, image preview, ConfirmDialog, sidebar search
- **R5**: ErrorBoundary, useNetworkStatus, i18n constants, PWA manifest, Web Vitals, focus-visible, skip-to-content
- **R6**: Full i18n (useTranslation + en.ts), bug fixes (render-time setState, array bounds), lint cleanup, type safety
- **R7**: Security fixes (shell injection), vitest infrastructure + 27 unit tests, CSS cleanup, memo optimizations, race protection
- **R8**: Testing library setup, 86 unit tests, message action menu, search highlighting, chat export (MD/JSON), editor preview, build optimization
- **R9**: Message delete/regenerate, reply/quote, typing effect, tab bar, notification system, test expansion 91→137
- **R10**: Shared constants extraction, tab drag-drop, CollapsibleContent + ResizeObserver, code block line numbers + collapse, Playwright E2E setup, consistency audit
- **R11**: WebSocket reconnect UX, KaTeX math rendering, lightbox viewer, settings import/export, 170 tests
- **R12**: Dark mode image brightness, i18n keys, file upload XHR progress, CommandPalette, OnboardingTip, message interactions, media player, 226 tests
- **R13**: Player mutex, progress drag, vim navigation, keyboard help, theme editor, bookmark system, responsive layout deep optimization, 264 tests
- **R14**: Advanced search, hook improvements, final polish features, 307 tests
- **R15**: Final round — full project consistency scan, dead CSS cleanup (15 unused rules), lint fix, npm audit fix (2 vulnerabilities resolved), README update, 307 tests passing

## CHANGELOG

### v15 (2026-05-18) — Final Polish
- Full project consistency scan: 78 hardcoded Chinese strings identified (settings panels, deferred to future i18n migration)
- Dead CSS cleanup: 15 unused rule blocks removed
- Lint fix: 2 unused eslint-disable directives removed
- Security: npm audit fix resolved 2 vulnerabilities (lodash-es, uuid via mermaid chain)
- README updated with complete R1-R15 history
- Final verification: build ✅, lint ✅ (1 known warning), 307 tests ✅, 0 vulnerabilities ✅

### v14 (2026-05-18) — Advanced Features
- Advanced search with filters and highlighting
- Hook improvements for better performance
- Final polish features

### v13 (2026-05-18) — Deep Optimization
- Media player mutex for single-playback
- Progress bar drag interaction
- Vim-style j/k message navigation
- Keyboard help panel
- Custom theme editor with live preview
- Bookmark system with localStorage persistence
- Responsive layout deep optimization
- Test suite expanded to 264 tests

### v12 (2026-05-18) — Feature Complete
- Dark mode image brightness control
- Complete i18n key coverage
- File upload with XHR progress tracking
- Command palette (Ctrl+K)
- Onboarding tips for first-time users
- Message interactions (reactions, context menu, swipe)
- Audio/video media player with fullscreen + PiP
- Test suite expanded to 226 tests

### v11 (2026-05-17) — Quality & Features
- WebSocket reconnect UX improvements
- KaTeX math rendering
- Image lightbox viewer
- Settings import/export
- Test suite: 170 tests

### v10 (2026-05-17) — Consistency & Tooling
- Shared constants extraction
- Tab drag-drop reordering
- CollapsibleContent with ResizeObserver
- Code block line numbers + collapse
- Playwright E2E framework setup
- Consistency audit

### v9 (2026-05-17) — Interactions
- Message delete/regenerate
- Reply/quote system
- Typing effect for assistant messages
- Tab bar with session management
- Notification system with desktop alerts
- Tests: 91→137

### v8 (2026-05-17) — Testing & Export
- @testing-library/react setup
- 86 unit tests (hooks, components, search, types)
- Message action menu
- Search result highlighting
- Chat export (Markdown/JSON)
- Editor preview mode
- Build optimization

### v7 (2026-05-17) — Security & Testing
- Shell injection security fix
- Vitest infrastructure + 27 unit tests
- CSS cleanup + memo optimizations
- Race protection for chat switching
