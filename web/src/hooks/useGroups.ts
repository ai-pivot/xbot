/**
 * useGroups — agent groups (peer groups): the durable "these sessions are a
 * team" set.
 *
 * The store is the authority: every mutation returns the whole list, so the UI
 * never patches its own copy (an edit made by an agent through its own tools
 * shows up here too). Loading happens on mount — the panel only mounts when the
 * group tab is selected.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import {
  createPeerGroup,
  deletePeerGroup,
  fetchPeerGroups,
  joinPeerGroup,
  leavePeerGroup,
} from '@/components/agent/api'
import type { PeerGroup } from '@/types/shared'

export interface GroupsState {
  groups: PeerGroup[]
  loading: boolean
  error: string | null
  refresh: () => void
  createGroup: (id: string) => Promise<void>
  deleteGroup: (id: string) => Promise<void>
  addMember: (groupId: string, sessionKey: string, name: string) => Promise<void>
  removeMember: (groupId: string, sessionKey: string) => Promise<void>
}

/** Group ids the store accepts (mirrors tools.ValidatePeerGroupID). */
export const PEER_GROUP_ID_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/

export function useGroups(): GroupsState {
  const [groups, setGroups] = useState<PeerGroup[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seqRef = useRef(0)

  const load = useCallback(async () => {
    const seq = ++seqRef.current
    setLoading(true)
    try {
      const next = await fetchPeerGroups()
      if (seq !== seqRef.current) return
      setGroups(next)
      setError(null)
    } catch (err) {
      if (seq !== seqRef.current) return
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      if (seq === seqRef.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /** Run a mutation, adopt the authoritative list, and surface failures. */
  const mutate = useCallback(
    async (fn: () => Promise<PeerGroup[]>) => {
      try {
        const next = await fn()
        seqRef.current++ // a mutation's response supersedes any in-flight read
        setGroups(next)
        setError(null)
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
        await load() // show the truth after a rejected edit
      }
    },
    [load],
  )

  const createGroup = useCallback((id: string) => mutate(() => createPeerGroup(id)), [mutate])
  const deleteGroup = useCallback((id: string) => mutate(() => deletePeerGroup(id)), [mutate])
  const addMember = useCallback(
    (groupId: string, sessionKey: string, name: string) => mutate(() => joinPeerGroup(groupId, sessionKey, name)),
    [mutate],
  )
  const removeMember = useCallback(
    (groupId: string, sessionKey: string) => mutate(() => leavePeerGroup(groupId, sessionKey)),
    [mutate],
  )

  return useMemo(
    () => ({ groups, loading, error, refresh: load, createGroup, deleteGroup, addMember, removeMember }),
    [groups, loading, error, load, createGroup, deleteGroup, addMember, removeMember],
  )
}
