import { syncSettingToServer } from '@/lib/userSettings'

const STORAGE_KEY = 'xbot:recent-workdirs:v1'
const STORAGE_VERSION = 1
const MAX_RECENT_WORKDIRS = 5

interface StoredRecentWorkDirs {
  version: typeof STORAGE_VERSION
  paths: string[]
}

export function loadRecentWorkDirs(): string[] {
  try {
    const parsed = JSON.parse(localStorage.getItem(STORAGE_KEY) || 'null') as Partial<StoredRecentWorkDirs> | null
    if (parsed?.version !== STORAGE_VERSION || !Array.isArray(parsed.paths)) return []
    return parsed.paths
      .filter((path): path is string => typeof path === 'string' && path.trim().length > 0)
      .map((path) => path.trim())
      .filter((path, index, paths) => paths.indexOf(path) === index)
      .slice(0, MAX_RECENT_WORKDIRS)
  } catch {
    return []
  }
}

export function rememberRecentWorkDir(workDir: string): string[] {
  const normalized = workDir.trim()
  if (!normalized) return loadRecentWorkDirs()
  return persistRecentWorkDirs([
    normalized,
    ...loadRecentWorkDirs().filter((path) => path !== normalized),
  ].slice(0, MAX_RECENT_WORKDIRS))
}

export function removeRecentWorkDir(workDir: string): string[] {
  return persistRecentWorkDirs(loadRecentWorkDirs().filter((path) => path !== workDir))
}

function persistRecentWorkDirs(paths: string[]): string[] {
  try {
    const value = JSON.stringify({ version: STORAGE_VERSION, paths })
    localStorage.setItem(STORAGE_KEY, value)
    syncSettingToServer(STORAGE_KEY, value)
  } catch {
    // Keep the in-memory result usable when storage is unavailable.
  }
  return paths
}

export const recentWorkDirsStorageKey = STORAGE_KEY
