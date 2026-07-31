import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  loadRecentWorkDirs,
  recentWorkDirsStorageKey,
  rememberRecentWorkDir,
  removeRecentWorkDir,
} from './recent-workdirs'

describe('recent work directories', () => {
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => store.set(key, value),
    })
  })

  it('uses the versioned key, deduplicates, and keeps the five newest paths', () => {
    expect(recentWorkDirsStorageKey).toBe('xbot:recent-workdirs:v1')
    for (const path of ['/a', '/b', '/c', '/d', '/e', '/f', '/c']) {
      rememberRecentWorkDir(path)
    }
    expect(loadRecentWorkDirs()).toEqual(['/c', '/f', '/e', '/d', '/b'])
  })

  it('removes a remembered path', () => {
    rememberRecentWorkDir('/repo/one')
    rememberRecentWorkDir('/repo/two')
    expect(removeRecentWorkDir('/repo/one')).toEqual(['/repo/two'])
  })
})
