/**
 * useWSConnection — React hook returning the app's WSConnection.
 *
 * Spec 2 §3.4 names this hook; the implementation lives in
 * `@/providers/WSProvider` (which owns the connection instance + context).
 * This thin re-export keeps the documented import path stable
 * (`@/hooks/useWSConnection`) while avoiding a second source of truth.
 */
export { useWSConnection } from '@/providers/WSProvider'
export type { WSConnection } from '@/types/ws'
