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
 * a fresh reload when the echo row lacks it.
 *
 * Matching:
 * - turnID>0 targets: exact turnID+content match. The echo's content equals
 *   the persisted content (the agent loop eager-saves the same expanded
 *   string), so a mismatch means the row isn't this message — return undefined
 *   rather than guessing across turns.
 * - turnID=0 targets (attachment-expansion echoes): no turnID to match, so
 *   fall back to content. `rows` are DB-id-ascending, so scan in REVERSE to
 *   hit the MOST RECENT occurrence — rewind semantically targets the newest
 *   message with that content; a forward scan would match the oldest
 *   same-content row and rewind to the wrong position when content repeats.
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
  if (target.turnID > 0) {
    for (const m of rows) {
      if (m.role !== 'user' || m.dbID == null) continue
      if (m.turnID === target.turnID && m.content === target.content) {
        return m.dbID
      }
    }
    return undefined
  }
  for (let i = rows.length - 1; i >= 0; i--) {
    const m = rows[i]
    if (m.role !== 'user' || m.dbID == null) continue
    if (m.content && m.content === target.content) {
      return m.dbID
    }
  }
  return undefined
}
