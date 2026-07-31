/**
 * ImagePreview — centered image preview with click-to-zoom (Spec 5 §3.5).
 *
 * Supports PNG/JPG/JPEG/GIF/WebP/SVG. The image is centered in the panel; a
 * caption row shows the file name. Clicking the image (or pressing Enter on
 * the button) opens a fullscreen-ish Dialog with the image scaled up.
 *
 * Source is the image URL from `useFileContent.imageUrl`; a real backend
 * file-serving endpoint would supply a fetchable URL.
 */
import { useState } from 'react'
import { ImageOff } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from '@/components/ui/dialog'
import { useI18n } from '@/providers/i18n'

export interface ImagePreviewProps {
  /** Resolved image URL (or null when not available). */
  src: string
  /** File name shown as a caption. */
  fileName: string
  className?: string
}

export function ImagePreview({ src, fileName, className }: ImagePreviewProps) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [errored, setErrored] = useState(false)

  return (
    <div className={`flex h-full flex-col items-center justify-center gap-4 p-6 ${className ?? ''}`}>
      {errored ? (
        <div className="flex flex-col items-center gap-2 text-text-secondary">
          <ImageOff className="size-10 opacity-50" />
          <span className="text-sm">{t('file.imageLoadFailed')}</span>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-label={t('file.zoom')}
          className="group/img flex max-h-full max-w-full flex-col items-center gap-3 rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
        >
          <img
            src={src}
            alt={fileName}
            loading="lazy"
            onError={() => setErrored(true)}
            className="max-h-[calc(100%-2rem)] max-w-full rounded-md border object-contain shadow-sm transition-transform group-hover/img:scale-[1.02]"
          />
          <span className="text-xs text-text-secondary">{fileName}</span>
        </button>
      )}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent
          className="max-w-[90vw] border-bg-tertiary bg-bg-primary/95 p-2 sm:max-w-[80vw]"
          showCloseButton
        >
          <DialogTitle className="sr-only">{fileName}</DialogTitle>
          {open && !errored && (
            <img
              src={src}
              alt={fileName}
              className="mx-auto max-h-[85vh] max-w-full rounded-md object-contain"
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
