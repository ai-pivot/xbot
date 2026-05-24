import { useState, useCallback } from 'react'
import type { Message } from '../types'

export interface UseSnapshotReturn {
  /** Whether a snapshot is being generated */
  snapshotting: boolean
  /** Error message if snapshot failed */
  snapshotError: string | null
  /** Generate a text snapshot of a message and copy to clipboard */
  takeSnapshot: (message: Message) => Promise<boolean>
}

/**
 * Hook for generating message snapshots using Canvas.
 * Draws a styled text card and copies the canvas as a PNG image to clipboard.
 * Uses pure text rendering (no foreignObject dependency).
 */
export function useSnapshot(): UseSnapshotReturn {
  const [snapshotting, setSnapshotting] = useState(false)
  const [snapshotError, setSnapshotError] = useState<string | null>(null)

  const takeSnapshot = useCallback(async (message: Message): Promise<boolean> => {
    setSnapshotting(true)
    setSnapshotError(null)

    try {
      const canvas = document.createElement('canvas')
      const ctx = canvas.getContext('2d')
      if (!ctx) {
        setSnapshotError('Canvas not supported')
        return false
      }

      // Layout configuration
      const padding = 24
      const lineHeight = 24
      const maxLineWidth = 520
      const headerHeight = 48
      const footerHeight = 36
      const timestamp = message.ts ? new Date(message.ts).toLocaleString() : new Date().toLocaleString()
      const roleLabel = message.type === 'user' ? 'User' : 'Assistant'

      // Wrap text into lines
      ctx.font = '14px "SF Mono", "Cascadia Code", "Fira Code", monospace'
      const words = message.content.split('')
      const lines: string[] = []
      let currentLine = ''

      for (const char of words) {
        const testLine = currentLine + char
        const metrics = ctx.measureText(testLine)
        if (metrics.width > maxLineWidth - padding * 2 && currentLine) {
          lines.push(currentLine)
          currentLine = char
        } else {
          currentLine = testLine
        }
      }
      if (currentLine) lines.push(currentLine)

      // If no content, add a placeholder
      if (lines.length === 0) lines.push('(empty message)')

      // Calculate canvas dimensions
      const contentHeight = lines.length * lineHeight
      canvas.width = maxLineWidth
      canvas.height = headerHeight + contentHeight + footerHeight + padding

      // Draw background
      ctx.fillStyle = '#1e293b'
      ctx.fillRect(0, 0, canvas.width, canvas.height)

      // Draw border
      ctx.strokeStyle = '#334155'
      ctx.lineWidth = 1
      ctx.strokeRect(0.5, 0.5, canvas.width - 1, canvas.height - 1)

      // Draw header
      ctx.fillStyle = '#334155'
      ctx.fillRect(0, 0, canvas.width, headerHeight)
      ctx.font = 'bold 14px -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif'
      ctx.fillStyle = '#e2e8f0'
      ctx.fillText(`${roleLabel}  •  xbot`, padding, 30)

      // Draw content
      ctx.font = '14px "SF Mono", "Cascadia Code", "Fira Code", monospace'
      ctx.fillStyle = '#cbd5e1'
      for (let i = 0; i < lines.length; i++) {
        // Truncate extremely long lines
        const line = lines[i].length > 200 ? lines[i].slice(0, 200) + '…' : lines[i]
        ctx.fillText(line, padding, headerHeight + padding + i * lineHeight + lineHeight / 2)
      }

      // Draw footer
      const footerY = canvas.height - footerHeight
      ctx.fillStyle = '#334155'
      ctx.fillRect(0, footerY, canvas.width, footerHeight)
      ctx.font = '11px -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif'
      ctx.fillStyle = '#64748b'
      ctx.fillText(`📅 ${timestamp}`, padding, footerY + 22)

      // Copy canvas as PNG to clipboard
      const blob = await new Promise<Blob | null>(resolve => canvas.toBlob(resolve, 'image/png'))
      if (!blob) {
        setSnapshotError('Failed to create image')
        return false
      }

      await navigator.clipboard.write([
        new ClipboardItem({ 'image/png': blob })
      ])

      return true
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Unknown error'
      setSnapshotError(msg)
      return false
    } finally {
      setSnapshotting(false)
    }
  }, [])

  return { snapshotting, snapshotError, takeSnapshot }
}
