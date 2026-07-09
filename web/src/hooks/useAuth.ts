/**
 * useAuth — access the AuthContext (must be used within <AuthProvider>).
 */
import { useContext } from 'react'
import { AuthContext, type AuthContextValue } from '@/providers/AuthProvider'

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth must be used within an <AuthProvider>')
  }
  return ctx
}
