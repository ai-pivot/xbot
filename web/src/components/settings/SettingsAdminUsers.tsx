/**
 * SettingsAdminUsers — admin user management panel.
 *
 * Admins can view all users, see their linked identities, and change roles.
 *
 * Backend APIs:
 *   GET  /api/admin/users           → {users: [{id, display_name, role, created_at}]}
 *   POST /api/admin/users/{id}/role → {ok, user_id, role}
 */
import { useState, useEffect, useCallback } from 'react'
import { Shield, ShieldCheck, Loader2, AlertCircle } from 'lucide-react'

import { SettingsSection } from './SettingsSection'
import { Button } from '@/components/ui/button'
import { postAPI } from '@/lib/api'

interface User {
  id: number
  display_name: string
  role: string
  created_at: string
}

export function SettingsAdminUsers() {
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [updatingId, setUpdatingId] = useState<number | null>(null)

  const fetchUsers = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const data = await postAPI<{ users?: User[] }>('/api/admin/users/list')
      setUsers(data.users || [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchUsers()
  }, [fetchUsers])

  const toggleRole = async (user: User) => {
    setUpdatingId(user.id)
    const newRole = user.role === 'admin' ? 'user' : 'admin'
    try {
      await postAPI(`/api/admin/users/${user.id}/set-role`, { role: newRole })
      setUsers((prev) =>
        prev.map((u) => (u.id === user.id ? { ...u, role: newRole } : u)),
      )
    } catch {
      // ignore
    } finally {
      setUpdatingId(null)
    }
  }

  if (loading) {
    return (
      <div className="flex items-center gap-2 p-4 text-sm text-text-muted">
        <Loader2 className="size-4 animate-spin" />
        加载中...
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-4">
        <p className="flex items-center gap-1.5 text-sm text-[var(--status-error,#ef4444)]">
          <AlertCircle className="size-4" />
          {error}
        </p>
        <p className="mt-2 text-xs text-text-muted">
          此面板仅对 admin 用户可见。
        </p>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-2.5 p-4">
      <SettingsSection title="用户管理" description="查看所有用户并管理角色权限。">
        <div className="flex flex-col gap-2.5">
          {users.map((user) => (
            <div
              key={user.id}
              className="flex items-center gap-2.5 rounded-lg border border-border bg-bg-secondary px-3 py-2 transition-colors hover:bg-bg-tertiary"
            >
              <span className="flex-1 truncate text-sm text-text-primary">
                {user.display_name || `(user ${user.id})`}
              </span>
              {user.role === 'admin' ? (
                <span className="flex items-center gap-1 rounded bg-[#22c55e]/12 px-1.5 py-0.5 text-xs font-medium text-[#22c55e]">
                  <ShieldCheck className="size-3" />
                  admin
                </span>
              ) : (
                <span className="flex items-center gap-1 rounded bg-bg-tertiary px-1.5 py-0.5 text-xs font-medium text-text-muted">
                  <Shield className="size-3" />
                  user
                </span>
              )}
              <Button
                onClick={() => toggleRole(user)}
                disabled={updatingId === user.id}
                variant="outline"
                size="sm"
                className="h-7 border-border bg-bg-tertiary px-2 text-xs hover:bg-bg-hover"
              >
                {updatingId === user.id ? (
                  <Loader2 className="size-3 animate-spin" />
                ) : user.role === 'admin' ? (
                  '降级'
                ) : (
                  '提升'
                )}
              </Button>
            </div>
          ))}
        </div>
      </SettingsSection>
    </div>
  )
}
