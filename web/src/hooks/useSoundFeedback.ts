import { useState, useCallback, useRef, useEffect } from 'react'
import type { SoundConfig } from '../types'

const DEFAULT_CONFIG: SoundConfig = {
  enabled: false,
  volume: 0.5,
  sentSound: 'beep',
  receiveSound: 'chime',
  notifySound: 'pop',
}

const LS_KEY = 'xbot-sound-config'

function loadConfig(): SoundConfig {
  try {
    const raw = localStorage.getItem(LS_KEY)
    if (raw) return { ...DEFAULT_CONFIG, ...JSON.parse(raw) }
  } catch { /* ignore corrupt data */ }
  return { ...DEFAULT_CONFIG }
}

function saveConfig(config: SoundConfig) {
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(config))
  } catch { /* ignore quota errors */ }
}

let _audioCtx: AudioContext | null = null

function getAudioContext(): AudioContext {
  if (!_audioCtx || _audioCtx.state === 'closed') {
    _audioCtx = new AudioContext()
  }
  return _audioCtx
}

/**
 * Synthesize a short tone using Web Audio API.
 * No external audio files needed.
 */
function playTone(frequency: number, duration: number, volume: number, type: OscillatorType = 'sine') {
  try {
    const ctx = getAudioContext()
    if (ctx.state === 'suspended') ctx.resume()
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.type = type
    osc.frequency.setValueAtTime(frequency, ctx.currentTime)
    gain.gain.setValueAtTime(volume * 0.3, ctx.currentTime)
    gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + duration)
    osc.connect(gain)
    gain.connect(ctx.destination)
    osc.start()
    osc.stop(ctx.currentTime + duration)
    osc.onended = () => { osc.disconnect(); gain.disconnect() }
  } catch {
    // AudioContext may not be available
  }
}

const SOUND_PROFILES: Record<string, { freq: number; dur: number; type: OscillatorType }> = {
  beep:  { freq: 800, dur: 0.1, type: 'square' },
  chime: { freq: 1200, dur: 0.2, type: 'sine' },
  pop:   { freq: 600, dur: 0.08, type: 'triangle' },
  none:  { freq: 0, dur: 0, type: 'sine' },
}

export interface UseSoundFeedbackReturn {
  config: SoundConfig
  /** Play a specific sound type */
  play: (event: 'sent' | 'receive' | 'notify') => void
  /** Update config (persists to localStorage) */
  updateConfig: (partial: Partial<SoundConfig>) => void
  /** Toggle sound on/off */
  toggleEnabled: () => void
}

export function useSoundFeedback(): UseSoundFeedbackReturn {
  const [config, setConfig] = useState<SoundConfig>(loadConfig)
  const configRef = useRef(config)

  useEffect(() => {
    configRef.current = config
  }, [config])

  const play = useCallback((event: 'sent' | 'receive' | 'notify') => {
    const cfg = configRef.current
    if (!cfg.enabled) return
    const soundName = event === 'sent' ? cfg.sentSound : event === 'receive' ? cfg.receiveSound : cfg.notifySound
    const profile = SOUND_PROFILES[soundName]
    if (profile && profile.freq > 0) {
      playTone(profile.freq, profile.dur, cfg.volume, profile.type)
    }
  }, [])

  const updateConfig = useCallback((partial: Partial<SoundConfig>) => {
    setConfig(prev => {
      const next = { ...prev, ...partial }
      saveConfig(next)
      return next
    })
  }, [])

  const toggleEnabled = useCallback(() => {
    setConfig(prev => {
      const next = { ...prev, enabled: !prev.enabled }
      saveConfig(next)
      return next
    })
  }, [])

  return { config, play, updateConfig, toggleEnabled }
}
