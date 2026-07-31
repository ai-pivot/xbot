/**
 * useWSConnection — compatibility import for the app's REST + SSE connection.
 *
 * The compatibility name avoids a broad UI-only rename while the provider now
 * owns an EventSource and sends every client operation through REST POST.
 */
export { useWSConnection } from '@/providers/WSProvider'
export type { WSConnection } from '@/types/ws'
