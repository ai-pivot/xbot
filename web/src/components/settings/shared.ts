import type { PresetCommand } from '../../types'

// ── Types ──

export type Theme = 'dark' | 'light'
export type FontSize = 'small' | 'medium' | 'large'
export type Language = 'zh-CN' | 'en'
export type TabId = 'appearance' | 'sessions' | 'presets' | 'llm' | 'runner' | 'market'

export interface UserSettings {
  theme: Theme
  font_size: FontSize
  nickname: string
  language: Language
  preset_commands?: string
}

export interface MarketEntry {
  id: number
  type: string
  name: string
  description: string
  author: string
  created_at: string
  installed: boolean
}

export interface MyMarketEntry {
  name: string
  type: string
  description: string
  published: boolean
}

export interface SessionInfo {
  id: string
  type: string
  label: string
  role?: string
  instance?: string
  running?: boolean
  preview?: string
  members?: string
}

export interface SessionMessage {
  role: string
  content: string
}

export interface LLMConfig {
  provider: string
  base_url: string
  model: string
  models: string[]
  is_global: boolean
}


export type ShowToastFn = (message: string, type?: 'info' | 'error' | 'success') => void

export interface TabProps {
  showToast: ShowToastFn
}

// ── Constants ──

export const FONT_SIZE_MAP: Record<FontSize, string> = {
  small: '14px',
  medium: '16px',
  large: '18px',
}

export const DEFAULT_SETTINGS: UserSettings = {
  theme: 'dark',
  font_size: 'medium',
  nickname: '',
  language: 'zh-CN',
}

export const LS_KEYS: Record<string, string> = {
  theme: 'xbot-theme',
  font_size: 'xbot-font-size',
  nickname: 'xbot-nickname',
  language: 'xbot-language',
}

export const TABS: { id: TabId; labelKey: string; icon: string }[] = [
  { id: 'appearance', labelKey: 'tabAppearance' as const, icon: '🎨' },
  { id: 'sessions', labelKey: 'tabSessions' as const, icon: '💬' },
  { id: 'presets', labelKey: 'tabPresets' as const, icon: '⚡' },
  { id: 'llm', labelKey: 'tabLLM' as const, icon: '🧠' },
  { id: 'runner', labelKey: 'tabRunner' as const, icon: '🖥️' },
  { id: 'market', labelKey: 'tabMarket' as const, icon: '🏪' },
]

export const PROVIDER_OPTIONS = [
  { value: 'openai', label: 'OpenAI (GPT / o-series)' },
  { value: 'anthropic', label: 'Anthropic (Claude)' },
]

// Re-export PresetCommand for convenience
export type { PresetCommand }

// ── Utility functions ──

export function lsGet<K extends keyof UserSettings>(key: K, fallback: UserSettings[K]): UserSettings[K] {
  const raw = localStorage.getItem(LS_KEYS[key])
  return (raw as UserSettings[K]) || fallback
}

export function lsSet<K extends keyof UserSettings>(key: K, value: UserSettings[K]) {
  localStorage.setItem(LS_KEYS[key], value as string)
}

export async function fetchSettings(): Promise<UserSettings & Record<string, string>> {
  try {
    const resp = await fetch('/api/settings')
    const data = await resp.json()
    if (data.ok && data.settings) {
      return {
        theme: (data.settings.theme as Theme) || lsGet('theme', DEFAULT_SETTINGS.theme),
        font_size: (data.settings.font_size as FontSize) || lsGet('font_size', DEFAULT_SETTINGS.font_size),
        nickname: data.settings.nickname || lsGet('nickname', DEFAULT_SETTINGS.nickname),
        language: (data.settings.language as Language) || lsGet('language', DEFAULT_SETTINGS.language),
        preset_commands: data.settings.preset_commands,
        // Agent settings
        context_mode: data.settings.context_mode || 'auto',
        max_iterations: data.settings.max_iterations || '2000',
        max_concurrency: data.settings.max_concurrency || '3',
        max_context_tokens: data.settings.max_context_tokens || '200000',
        max_output_tokens: data.settings.max_output_tokens || '8192',
        thinking_mode: data.settings.thinking_mode ?? '',
        enable_auto_compress: data.settings.enable_auto_compress ?? 'true',
        enable_stream: data.settings.enable_stream ?? 'true',
        enable_masking: data.settings.enable_masking ?? 'true',
      }
    }
  } catch {
    // Server unreachable — use localStorage fallback
  }
  return {
    theme: lsGet('theme', DEFAULT_SETTINGS.theme),
    font_size: lsGet('font_size', DEFAULT_SETTINGS.font_size),
    nickname: lsGet('nickname', DEFAULT_SETTINGS.nickname),
    language: lsGet('language', DEFAULT_SETTINGS.language),
  }
}

export async function saveSettings(settings: Partial<UserSettings>): Promise<boolean> {
  try {
    const resp = await fetch('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ settings }),
    })
    const data = await resp.json()
    return data.ok === true
  } catch {
    return false
  }
}
