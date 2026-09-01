/**
 * Session-scoped event dispatch — the ONLY sanctioned way for per-session
 * panels/hooks to signal cross-cutting state (sidebar busy/idle etc.).
 *
 * ARCHITECTURAL CONTRACT (user-mandated: "session 的面板禁止加全局状态"):
 * per-session components (useProgressStream, AgentPanel, useChatMessages, …)
 * MUST NOT touch global state directly — window.dispatchEvent /
 * window.addEventListener are BANNED in those files by ESLint
 * (`no-restricted-properties` on window, see eslint.config.js). Route every
 * global signal through this module, which enforces the session identity
 * at the type level:
 *
 *   - dispatchAgentIdle(chatID, channel) — chatID is REQUIRED. Identity-less
 *     dispatches are a bug: useSessionStore's listener used to fall back to
 *     "clear the ACTIVE session", so a background session's PhaseDone/text-end
 *     (whose inner progress payload carries no chat_id) corrupted whatever the
 *     user was viewing — cancelling session A idled busy session B (the
 *     active one) for 15s (fresh idle intent beat HTTP running in
 *     mergeStatus). The fallback is deleted; identity is enforced here.
 *
 * The global listener (useSessionStore) ignores identity-less events —
 * routing them to "the active session" was the cross-session pollution.
 */

export interface AgentIdleDetail {
  chatID: string
  channel?: string
}

/** Dispatch agent-idle for a SPECIFIC session (its turn ended / PhaseDone).
 * chatID is required — an empty/missing chatID is a caller bug and is
 * dropped with a console.error (never routed to "the active session"). */
export function dispatchAgentIdle(chatID: string, channel?: string): void {
  if (!chatID) {
    console.error('[sessionEvents] dispatchAgentIdle called without chatID — dropping (identity-less agent-idle must never resolve to the active session)')
    return
  }
  window.dispatchEvent(new CustomEvent('agent-idle', { detail: { chatID, channel } }))
}

/** Dispatch sessions-resync (HTTP reconcile request). This one is
 * connection-level (SSE layer / reconnect / resync_required), NOT per-panel —
 * panels never call it. Exported for the SSE provider; banned in panel files. */
export function dispatchSessionsResync(): void {
  window.dispatchEvent(new CustomEvent('sessions-resync'))
}
