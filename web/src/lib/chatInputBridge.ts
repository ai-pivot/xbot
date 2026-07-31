/**
 * chatInputBridge — cross-tree bridge so the file explorer (right sidebar,
 * normal React tree) can inject text into the MessageInput textarea (which
 * lives inside a dockview panel with an isolated React root and therefore
 * cannot share React Context with the sidebar).
 *
 * The MessageInput registers an insert handler on mount; the FileExplorer
 * calls `insertIntoChat` from its context menu. The handler is module-scoped,
 * so it works regardless of React tree boundaries.
 *
 * Only the main (non-SubAgent) Agent panel renders a MessageInput, so at most
 * one handler is registered at a time.
 */
type InsertHandler = (text: string) => void

let currentHandler: InsertHandler | null = null

/** Register the handler that owns the chat input textarea. Pass null to clear. */
export function setChatInsertHandler(fn: InsertHandler | null): void {
  currentHandler = fn
}

/** Insert `text` into the chat input (appended to current content). */
export function insertIntoChat(text: string): void {
  currentHandler?.(text)
}
