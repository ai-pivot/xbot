/**
 * useCwd — React hook returning the app's current working directory.
 *
 * The implementation lives in `@/providers/CwdProvider` (which owns the WS
 * connection interaction + context). This thin re-export keeps the documented
 * import path stable (`@/hooks/useCwd`) while avoiding a second source of truth.
 */
export { useCwd, type CwdContextValue } from '@/providers/CwdProvider'
