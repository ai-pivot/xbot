import type { ChatMessage } from '@/types/shared'

/**
 * Resolve the DB id of a persisted user message from a fresh history snapshot.
 *
 * The web frontend renders user messages deterministically from the `user_echo`
 * SSE message, which is emitted at queue-admission time — BEFORE the agent loop
 * eagerly persists the row. The echo therefore carries the authoritative turnID
 * but NOT the DB id (`session_messages` auto-increment id), which is only
 * assigned at persistence. `dbID` arrives exclusively via a history reload
 * (parseHistoryMessages → `dbID: m.id`).
 *
 * Rewind (RewindToHistoryID) requires that DB id, so rewindTo resolves it from
 * a fresh reload when the echo row lacks it. Matching is turnID+content first
 * (echo rows always carry the authoritative turnID); rows without a turnID
 * (attachment-expansion echoes) fall back to content-only matching.
 *
 * @param rows   Fresh history rows (must carry dbID).
 * @param target The echo row the user is trying to rewind.
 * @returns The DB id of the matching persisted row, or undefined when the
 *          message is not (yet) in the snapshot — i.e. genuinely not persisted.
 */
export function resolveUserMessageDBID(
  rows: ChatMessage[],
  target: Pick<ChatMessage, 'role' | 'turnID' | 'content'>,
): number | undefined {
  for (const m of rows) {
    if (m.role !== 'user' || m.dbID == null) continue
    if (m.turnID === target.turnID && m.turnID > 0 && m.content === target.content) {
      return m.dbID
    }
  }
  for (const m of rows) {
    if (m.role !== 'user' || m.dbID == null) continue
    if (m.content && m.content === target.content) {
      return m.dbID
    }
  }
  return undefined
}
