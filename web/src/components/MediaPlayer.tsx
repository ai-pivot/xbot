import { useRef, useState, useCallback, useEffect, memo } from 'react'
import { useTranslation } from '../i18n'

/* -------------------------------------------------------------------------- */
/*  Fullscreen helpers (vendor-prefixed compat)                                */
/* -------------------------------------------------------------------------- */

function requestFS(el: HTMLElement): Promise<void> {
  const fn = el.requestFullscreen
    ?? (el as unknown as { webkitRequestFullscreen?: () => Promise<void> }).webkitRequestFullscreen
  if (fn) return fn.call(el)
  return Promise.reject(new Error('Fullscreen not supported'))
}

function exitFS(): Promise<void> {
  const fn = document.exitFullscreen
    ?? (document as unknown as { webkitExitFullscreen?: () => Promise<void> }).webkitExitFullscreen
  if (fn) return fn.call(document)
  return Promise.reject(new Error('Fullscreen not supported'))
}

function checkFullscreen(): boolean {
  return !!(document.fullscreenElement ?? (document as unknown as { webkitFullscreenElement?: Element }).webkitFullscreenElement)
}

/* -------------------------------------------------------------------------- */
/*  useMediaPlayer hook                                                       */
/* -------------------------------------------------------------------------- */

interface UseMediaPlayerOptions {
  src: string
}

interface UseMediaPlayerReturn {
  mediaRef: React.RefObject<HTMLAudioElement | HTMLVideoElement | null>
  playing: boolean
  currentTime: number
  duration: number
  buffered: number
  volume: number
  muted: boolean
  playbackRate: number
  error: boolean
  togglePlay: () => void
  seek: (time: number) => void
  setVolume: (v: number) => void
  toggleMute: () => void
  setPlaybackRate: (rate: number) => void
  formatTime: (seconds: number) => string
}

function useMediaPlayer({ src }: UseMediaPlayerOptions): UseMediaPlayerReturn {
  const mediaRef = useRef<HTMLAudioElement | HTMLVideoElement>(null)
  const [playing, setPlaying] = useState(false)
  const [currentTime, setCurrentTime] = useState(0)
  const [duration, setDuration] = useState(0)
  const [buffered, setBuffered] = useState(0)
  const [volume, setVolumeState] = useState(1)
  const [muted, setMuted] = useState(false)
  const [playbackRate, setPlaybackRateState] = useState(1)
  const [error, setError] = useState(false)

  const togglePlay = useCallback(() => {
    const el = mediaRef.current
    if (!el) return
    if (el.paused) {
      el.play().catch(() => { /* user gesture required */ })
    } else {
      el.pause()
    }
  }, [])

  const seek = useCallback((time: number) => {
    const el = mediaRef.current
    if (!el || !isFinite(time) || time < 0) return
    el.currentTime = time
  }, [])

  const setVolume = useCallback((v: number) => {
    const el = mediaRef.current
    if (!el) return
    const clamped = Math.max(0, Math.min(1, v))
    el.volume = clamped
    setVolumeState(clamped)
  }, [])

  const toggleMute = useCallback(() => {
    const el = mediaRef.current
    if (!el) return
    el.muted = !el.muted
    setMuted(el.muted)
  }, [])

  const setPlaybackRate = useCallback((rate: number) => {
    const el = mediaRef.current
    if (!el) return
    el.playbackRate = rate
    setPlaybackRateState(rate)
  }, [])

  const formatTime = useCallback((seconds: number): string => {
    if (!isFinite(seconds) || seconds < 0) return '0:00'
    const h = Math.floor(seconds / 3600)
    const m = Math.floor((seconds % 3600) / 60)
    const s = Math.floor(seconds % 60)
    if (h > 0) {
      return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
    }
    return `${m}:${String(s).padStart(2, '0')}`
  }, [])

  // Sync DOM events → state
  useEffect(() => {
    const el = mediaRef.current
    if (!el) return

    setError(false)
    setCurrentTime(0)
    setDuration(0)
    setBuffered(0)

    const onPlay = () => setPlaying(true)
    const onPause = () => setPlaying(false)
    const onTimeUpdate = () => setCurrentTime(el.currentTime)
    const onLoadedMetadata = () => setDuration(el.duration)
    const onDurationChange = () => setDuration(el.duration)
    const onEnded = () => setPlaying(false)
    const onError = () => setError(true)

    const onProgress = () => {
      if (el.buffered.length > 0) {
        setBuffered(el.buffered.end(el.buffered.length - 1))
      }
    }

    el.addEventListener('play', onPlay)
    el.addEventListener('pause', onPause)
    el.addEventListener('timeupdate', onTimeUpdate)
    el.addEventListener('loadedmetadata', onLoadedMetadata)
    el.addEventListener('durationchange', onDurationChange)
    el.addEventListener('ended', onEnded)
    el.addEventListener('progress', onProgress)
    el.addEventListener('error', onError)

    return () => {
      el.removeEventListener('play', onPlay)
      el.removeEventListener('pause', onPause)
      el.removeEventListener('timeupdate', onTimeUpdate)
      el.removeEventListener('loadedmetadata', onLoadedMetadata)
      el.removeEventListener('durationchange', onDurationChange)
      el.removeEventListener('ended', onEnded)
      el.removeEventListener('progress', onProgress)
      el.removeEventListener('error', onError)
    }
  }, [src])

  return {
    mediaRef,
    playing,
    currentTime,
    duration,
    buffered,
    volume,
    muted,
    playbackRate,
    error,
    togglePlay,
    seek,
    setVolume,
    toggleMute,
    setPlaybackRate,
    formatTime,
  }
}

/* -------------------------------------------------------------------------- */
/*  Progress bar (shared)                                                     */
/* -------------------------------------------------------------------------- */

function ProgressBar({
  currentTime,
  duration,
  buffered,
  onSeek,
  testId,
}: {
  currentTime: number
  duration: number
  buffered: number
  onSeek: (time: number) => void
  testId: string
}) {
  const barRef = useRef<HTMLDivElement>(null)

  const seekFromEvent = useCallback(
    (clientX: number) => {
      const bar = barRef.current
      if (!bar || !duration) return
      const rect = bar.getBoundingClientRect()
      const ratio = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width))
      onSeek(ratio * duration)
    },
    [duration, onSeek],
  )

  const handleClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      seekFromEvent(e.clientX)
    },
    [seekFromEvent],
  )

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (!duration) return
      const step = duration * 0.05
      if (e.key === 'ArrowRight') {
        e.preventDefault()
        onSeek(Math.min(duration, currentTime + step))
      } else if (e.key === 'ArrowLeft') {
        e.preventDefault()
        onSeek(Math.max(0, currentTime - step))
      }
    },
    [currentTime, duration, onSeek],
  )

  const pct = duration > 0 ? (currentTime / duration) * 100 : 0
  const bufPct = duration > 0 ? (buffered / duration) * 100 : 0

  return (
    <div
      ref={barRef}
      className="media-player-progress"
      onClick={handleClick}
      onKeyDown={handleKeyDown}
      tabIndex={0}
      data-testid={testId}
      role="slider"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(pct)}
      aria-label="Progress"
    >
      <div className="media-player-progress-bar">
        {bufPct > 0 && <div className="media-player-progress-buffered" style={{ width: `${bufPct}%` }} />}
        <div className="media-player-progress-filled" style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

/* -------------------------------------------------------------------------- */
/*  AudioPlayer                                                               */
/* -------------------------------------------------------------------------- */

interface AudioPlayerProps {
  src: string
  fileName?: string
}

export const AudioPlayer = memo(function AudioPlayer({ src, fileName }: AudioPlayerProps) {
  const { t } = useTranslation()
  const {
    mediaRef,
    playing,
    currentTime,
    duration,
    buffered,
    volume,
    muted,
    error,
    togglePlay,
    seek,
    setVolume,
    toggleMute,
    formatTime,
  } = useMediaPlayer({ src })

  const handleVolumeChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      setVolume(parseFloat(e.target.value))
    },
    [setVolume],
  )

  if (error) {
    return (
      <div className="media-player-audio media-player-error" data-testid="audio-player">
        <span className="media-player-error-text">{t('mediaNotSupported')}</span>
      </div>
    )
  }

  return (
    <div className="media-player-audio" data-testid="audio-player" aria-label={t('audioPlayer')}>
      <audio ref={mediaRef as React.RefObject<HTMLAudioElement>} src={src} preload="metadata" />

      <button
        className="media-player-audio-play-btn"
        onClick={togglePlay}
        data-testid="audio-play-btn"
        aria-label={playing ? t('pause') : t('play')}
      >
        <span aria-hidden="true">{playing ? '⏸' : '▶'}</span>
      </button>

      <div className="media-player-audio-body">
        {fileName && <div className="media-player-audio-filename">{fileName}</div>}

        <div className="media-player-audio-controls">
          <ProgressBar currentTime={currentTime} duration={duration} buffered={buffered} onSeek={seek} testId="audio-progress-bar" />
          <div className="media-player-time">
            <span>{formatTime(currentTime)}</span>
            <span>{formatTime(duration)}</span>
          </div>
        </div>
      </div>

      <div className="media-player-audio-volume">
        <button
          className="media-player-audio-volume-btn"
          onClick={toggleMute}
          data-testid="audio-mute-btn"
          aria-label={muted ? t('unmute') : t('mute')}
        >
          <span aria-hidden="true">{muted || volume === 0 ? '🔇' : volume < 0.5 ? '🔉' : '🔊'}</span>
        </button>
        <input
          type="range"
          min={0}
          max={1}
          step={0.05}
          value={muted ? 0 : volume}
          onChange={handleVolumeChange}
          className="media-player-volume-slider"
          aria-label={t('volume')}
          data-testid="audio-volume-slider"
        />
      </div>
    </div>
  )
})

/* -------------------------------------------------------------------------- */
/*  VideoPlayer                                                               */
/* -------------------------------------------------------------------------- */

interface VideoPlayerProps {
  src: string
  fileName?: string
}

export const VideoPlayer = memo(function VideoPlayer({ src, fileName }: VideoPlayerProps) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [controlsVisible, setControlsVisible] = useState(true)
  const [isFullscreen, setIsFullscreen] = useState(false)

  const {
    mediaRef,
    playing,
    currentTime,
    duration,
    buffered,
    muted,
    error,
    togglePlay,
    seek,
    toggleMute,
    formatTime,
  } = useMediaPlayer({ src })

  // Track fullscreen state via event
  useEffect(() => {
    const handler = () => setIsFullscreen(checkFullscreen())
    document.addEventListener('fullscreenchange', handler)
    document.addEventListener('webkitfullscreenchange', handler)
    return () => {
      document.removeEventListener('fullscreenchange', handler)
      document.removeEventListener('webkitfullscreenchange', handler)
    }
  }, [])

  // Auto-hide controls after 3 s while playing
  const showControls = useCallback(() => {
    setControlsVisible(true)
    if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    if (playing) {
      hideTimerRef.current = setTimeout(() => setControlsVisible(false), 3000)
    }
  }, [playing])

  const handleMouseMove = useCallback(() => {
    showControls()
  }, [showControls])

  const handleMouseLeave = useCallback(() => {
    if (playing) {
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
      hideTimerRef.current = setTimeout(() => setControlsVisible(false), 1000)
    }
  }, [playing])

  // Keep controls visible when paused
  useEffect(() => {
    if (!playing) {
      setControlsVisible(true)
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    }
  }, [playing])

  // Cleanup timer on unmount
  useEffect(() => {
    return () => {
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    }
  }, [])

  const toggleFullscreen = useCallback(() => {
    const el = containerRef.current
    if (!el) return
    if (checkFullscreen()) {
      exitFS()
    } else {
      requestFS(el)
    }
  }, [])

  const handleVideoClick = useCallback(() => {
    togglePlay()
    showControls()
  }, [togglePlay, showControls])

  if (error) {
    return (
      <div className="media-player-video media-player-error" data-testid="video-player">
        <div className="media-player-video-error-overlay">
          <span className="media-player-error-text">{t('mediaNotSupported')}</span>
        </div>
      </div>
    )
  }

  return (
    <div
      ref={containerRef}
      className={`media-player-video${controlsVisible ? ' media-player-video-controls-visible' : ''}`}
      data-testid="video-player"
      aria-label={t('videoPlayer')}
      onMouseMove={handleMouseMove}
      onMouseLeave={handleMouseLeave}
    >
      <video
        ref={mediaRef as React.RefObject<HTMLVideoElement>}
        src={src}
        preload="metadata"
        onClick={handleVideoClick}
      />

      <div className="media-player-video-overlay" onClick={handleVideoClick} />

      <div className="media-player-video-controls">
        <div className="media-player-video-progress-row">
          <ProgressBar currentTime={currentTime} duration={duration} buffered={buffered} onSeek={seek} testId="video-progress-bar" />
        </div>

        <div className="media-player-video-controls-row">
          <button
            className="media-player-video-btn"
            onClick={(e) => { e.stopPropagation(); togglePlay() }}
            data-testid="video-play-btn"
            aria-label={playing ? t('pause') : t('play')}
          >
            <span aria-hidden="true">{playing ? '⏸' : '▶'}</span>
          </button>

          <span className="media-player-time">
            {formatTime(currentTime)} / {formatTime(duration)}
          </span>

          {fileName && <span className="media-player-video-filename">{fileName}</span>}

          <div className="media-player-video-controls-spacer" />

          <button
            className="media-player-video-btn"
            onClick={(e) => { e.stopPropagation(); toggleMute() }}
            data-testid="video-mute-btn"
            aria-label={muted ? t('unmute') : t('mute')}
          >
            <span aria-hidden="true">{muted ? '🔇' : '🔊'}</span>
          </button>

          <button
            className="media-player-video-fullscreen-btn"
            onClick={(e) => { e.stopPropagation(); toggleFullscreen() }}
            data-testid="video-fullscreen-btn"
            aria-label={isFullscreen ? t('exitFullscreen') : t('fullscreen')}
          >
            <span aria-hidden="true">{isFullscreen ? '⊡' : '⛶'}</span>
          </button>
        </div>
      </div>
    </div>
  )
})
