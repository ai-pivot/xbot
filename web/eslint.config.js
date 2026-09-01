import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    rules: {
      // Allow underscore-prefixed unused variables (common for useState destructuring)
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      // These rules flag common valid React patterns (fetch-on-mount, reconnect refs, etc.)
      // Disable for now — they are noisy and block CI without adding real value
      'react-hooks/set-state-in-effect': 'off',
      'react-hooks/immutability': 'off',
      'react-hooks/refs': 'off',
      'react-refresh/only-export-components': 'off',
      'react-hooks/exhaustive-deps': 'off',
    },
  },
  // ── Session-panel global-state ban (user-mandated compile-time guard) ──
  // Per-session components (panels + their hooks) MUST NOT touch global state
  // directly (window.dispatchEvent / addEventListener / removeEventListener).
  // Root cause: useProgressStream dispatched identity-less agent-idle events
  // (PhaseDone's inner payload carries no chat_id), and useSessionStore's
  // listener fell back to "clear the ACTIVE session" — cancelling session A
  // idled busy session B (the active one) via a fresh idle intent that beat
  // HTTP running for 15s ("cancel one session breaks ALL busy sessions").
  // All cross-session signals from per-session code MUST go through
  // src/lib/sessionEvents.ts (enforces the session identity at the type
  // level). Violations = eslint ERROR = pre-commit failure = compile error.
  {
    files: [
      'src/hooks/useProgressStream.ts',
      'src/hooks/useChatMessages.ts',
      'src/components/agent/**/*.{ts,tsx}',
    ],
    ignores: ['**/*.test.*', '**/*.spec.*'],
    rules: {
      'no-restricted-properties': ['error',
        {
          object: 'window',
          property: 'dispatchEvent',
          message: 'Per-session code must NOT dispatch global window events (a background session event idled the ACTIVE session via useSessionStore\'s fallback). Route cross-session signals through src/lib/sessionEvents.ts.',
        },
        {
          object: 'window',
          property: 'addEventListener',
          message: 'Per-session code must NOT listen to global window events (cross-session state pollution). Global listeners live in useSessionStore / sseConnection; per-session rendering reads via React state.',
        },
        {
          object: 'window',
          property: 'removeEventListener',
          message: 'Per-session code must NOT manage global window listeners (cross-session state pollution). Global listeners live in useSessionStore / sseConnection.',
        },
      ],
    },
  },
])
