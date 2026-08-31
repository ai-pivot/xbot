/**
 * SettingsWebUsers — Web account management panel (admin-only by design:
 * every web login IS the operator after the multi-user removal).
 *
 * Registration is invite-only by default (config web.invite_only, v63+);
 * this panel is THE account-issuance flow: create accounts for family /
 * trusted people, each getting a strong one-time password. The delete
 * action removes access (the DELETEd user's sessions are not force-logged
 * out — existing cookies stay valid until they expire; removing access =
 * deleting the account).
 *
 * RPCs: list_web_users / create_web_user / delete_web_user (all require
 * admin — every web login qualifies).
 */
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { Loader2, ShieldAlert, UserPlus, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { SettingsSection } from './SettingsSection'
import { useI18n } from '@/providers/i18n'
import { postAPI } from '@/lib/api'

interface WebUserInfo {
  id: number
  username: string
  created_at: string
}

export function SettingsWebUsers() {
  const { t } = useI18n()
  const [users, setUsers] = useState<WebUserInfo[] | null>(null)
  const [error, setError] = useState('')
  const [newUsername, setNewUsername] = useState('')
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState('')
  /** One-time password of the last created account — shown ONCE, never stored. */
  const [oneTimePassword, setOneTimePassword] = useState<{ username: string; password: string } | null>(null)

  const refresh = useCallback(async () => {
    setError('')
    try {
      const res = await postAPI<{ users?: WebUserInfo[] }>('/api/rpc', {
        method: 'list_web_users',
        params: {},
      })
      setUsers(res.users ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setUsers([])
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault()
    const name = newUsername.trim()
    if (!name) return
    setCreating(true)
    setError('')
    try {
      const res = await postAPI<{ password: string }>('/api/rpc', {
        method: 'create_web_user',
        params: { username: name },
      })
      setOneTimePassword({ username: name, password: res.password })
      setNewUsername('')
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setCreating(false)
    }
  }

  const handleDelete = async (username: string) => {
    if (!window.confirm(t('settings.webUsers.deleteConfirm', { username }))) return
    setDeleting(username)
    setError('')
    try {
      await postAPI('/api/rpc', {
        method: 'delete_web_user',
        params: { username },
      })
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setDeleting('')
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <SettingsSection
        title={t('settings.nav.webUsers')}
        description={t('settings.webUsers.desc')}
      >
        {/* One-time password banner — shown immediately after a successful create */}
        {oneTimePassword ? (
          <div className="rounded-md border border-border bg-surface-bg p-4" data-testid="one-time-password">
            <p className="text-sm font-medium text-text-primary">
              {t('settings.webUsers.oneTimePasswordTitle', { username: oneTimePassword.username })}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">{t('settings.webUsers.oneTimePasswordHint')}</p>
            <div className="mt-3 flex items-center gap-2">
              <code className="flex-1 select-all break-all rounded bg-app-bg px-3 py-2 font-mono text-sm">
                {oneTimePassword.password}
              </code>
              <Button variant="ghost" size="sm" onClick={() => setOneTimePassword(null)} aria-label="close">
                <X className="size-4" />
              </Button>
            </div>
          </div>
        ) : null}

        {/* Create form */}
        <form onSubmit={handleCreate} className="flex items-end gap-2">
          <div className="flex min-w-0 flex-1 flex-col gap-1.5">
            <Label htmlFor="web-user-name">{t('settings.webUsers.usernameLabel')}</Label>
            <Input
              id="web-user-name"
              value={newUsername}
              onChange={(e) => setNewUsername(e.target.value)}
              placeholder={t('settings.webUsers.usernamePlaceholder')}
              disabled={creating}
              autoComplete="off"
            />
          </div>
          <Button type="submit" disabled={creating || !newUsername.trim()}>
            {creating ? <Loader2 className="size-4 animate-spin" /> : <UserPlus className="size-4" />}
            {t('settings.webUsers.createButton')}
          </Button>
        </form>

        {/* Error */}
        {error ? (
          <p className="text-sm text-destructive" role="alert">{error}</p>
        ) : null}

        {/* User list */}
        {users === null ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {t('common.loading')}
          </p>
        ) : users.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('settings.webUsers.empty')}</p>
        ) : (
          <ul className="flex flex-col gap-1" data-testid="web-user-list">
            {users.map((u) => (
              <li
                key={u.username}
                className="flex items-center justify-between rounded-md border border-border px-3 py-2"
              >
                <div className="flex min-w-0 flex-col">
                  <span className="truncate text-sm text-text-primary">{u.username}</span>
                  <span className="text-xs text-muted-foreground">{u.created_at}</span>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={deleting === u.username}
                  onClick={() => void handleDelete(u.username)}
                  aria-label={t('settings.webUsers.deleteButton', { username: u.username })}
                >
                  {deleting === u.username ? (
                    <Loader2 className="size-4 animate-spin" />
                  ) : (
                    <X className="size-4" />
                  )}
                </Button>
              </li>
            ))}
          </ul>
        )}

        {/* Invite-only note */}
        <p className="flex items-start gap-2 text-xs text-muted-foreground">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" />
          {t('settings.webUsers.inviteOnlyNote')}
        </p>
      </SettingsSection>
    </div>
  )
}
