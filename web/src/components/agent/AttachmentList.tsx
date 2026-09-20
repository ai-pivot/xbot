/**
 * AttachmentList — **附件渲染的唯一组件**（消息里的一等公民）。
 *
 * 输入是 `attachmentParse.parseAttachments()` 从正文里摘出的结构化附件；
 * 输出因 kind 分成两块：
 *   - 图片 → 缩略图网格（1 张大图 / 多张宫格 / >4 显示 `+N` 遮罩），点击开 lightbox；
 *   - 文件 → 卡片（类型图标 + 文件名 + 人类可读大小 + 动作：下载 / 新标签打开 / 复制链接）。
 *
 * 三个视觉变体（同一组件的 `variant` prop；默认 `mixed`）：设计对比用，
 * 也供决定后收敛到单一形态。
 *   - `rows`  紧凑行式：64px 小缩略图 + 单行文件 chip，占高最小；
 *   - `cards` 卡片式：大图宫格 + 独立文件卡片（图标色块 + 两行文本）；
 *   - `mixed` 混合式：单图 hero（带文件名 chip）/ 三图马赛克 + 文件「托盘」（分组容器）。
 *
 * 视觉语言与仓库一致（AGENTS.md「Tool pill 视觉语言」）：
 *   - 分类色只出现在图标 / 图标底（不使用大面积色块）；
 *   - 成功安静、失败吵闹（失败 = 红图标 + 红文案 + 红描边）；
 *   - 触屏把动作收进 `⋯` 菜单，桌面 hover 才显示，命中区均 ≥32px；
 *   - 图片/文件名一律 rune 安全截断 + `min-w-0` 收缩链（手机不越界）；
 *   - 所有文案走 i18n（禁止硬编码中文）。
 */
import { memo, useCallback, useState } from 'react'
import {
  Check,
  Copy,
  Download,
  ExternalLink,
  File as FileIcon,
  FileArchive,
  FileAudio,
  FileCode,
  FileImage,
  FileSpreadsheet,
  FileText,
  FileVideo,
  ImageOff,
  MoreHorizontal,
  RotateCw,
  TriangleAlert,
  type LucideIcon,
} from 'lucide-react'

import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'
import { cn } from '@/lib/utils'

import { openLightbox } from './Lightbox'
import {
  formatBytes,
  type AttachmentCategory,
  type ParsedAttachment,
} from './attachmentParse'

export type AttachmentVariant = 'rows' | 'cards' | 'mixed'

/** 图片/文件块的统一目标宽度（vw 基准，避免 fit-content 容器里的百分比失效）。 */
const BLOCK_W = 'w-[min(320px,calc(100vw-110px))]'

/** 分类色（与 toolVisuals 的分类型色板同族，低饱和使用）。 */
const CATEGORY_COLOR: Record<AttachmentCategory, string> = {
  archive: '#fbbf24',
  code: '#818cf8',
  doc: '#38bdf8',
  sheet: '#34d399',
  media: '#c084fc',
  image: '#fb923c',
  unknown: '#94a3b8',
}

const AUDIO_EXTS = new Set(['mp3', 'wav', 'flac', 'aac', 'ogg', 'm4a', 'opus', 'wma', 'aiff'])

function extensionOf(name: string): string {
  const base = name.split(/[?#]/)[0]
  const dot = base.lastIndexOf('.')
  return dot > 0 && dot < base.length - 1 ? base.slice(dot + 1).toLowerCase() : ''
}

function fileIcon(att: ParsedAttachment): LucideIcon {
  const cat = att.category ?? 'unknown'
  if (cat === 'media') return AUDIO_EXTS.has(extensionOf(att.name)) ? FileAudio : FileVideo
  const map: Record<AttachmentCategory, LucideIcon> = {
    archive: FileArchive,
    code: FileCode,
    doc: FileText,
    sheet: FileSpreadsheet,
    media: FileVideo,
    image: FileImage,
    unknown: FileIcon,
  }
  return map[cat]
}

/** 图片重试：加一个无害的 cache-buster 参数（服务端忽略未知 query）。 */
function withRetryParam(url: string, nonce: number): string {
  if (!url) return url
  const sep = url.includes('?') ? '&' : '?'
  return `${url}${sep}xbot_retry=${nonce}`
}

interface AttachmentListProps {
  attachments: ParsedAttachment[]
  /** 视觉变体（默认 `mixed`）。 */
  variant?: AttachmentVariant
  className?: string
}

/**
 * 附件列表 —— 图片网格 + 文件卡片的唯一渲染入口。
 * 调用方（UserMessage）已把正文里的引用剥离，这里只负责展示。
 */
export const AttachmentList = memo(function AttachmentList({
  attachments,
  variant = 'mixed',
  className,
}: AttachmentListProps) {
  const { t } = useI18n()
  const images = attachments.filter((a) => a.kind === 'image')
  const files = attachments.filter((a) => a.kind === 'file')
  if (attachments.length === 0) return null

  return (
    <div
      role="list"
      aria-label={t('agent.attachment.listLabel')}
      data-testid="attachment-list"
      data-variant={variant}
      className={cn('flex w-full min-w-0 flex-col gap-1.5', className)}
    >
      {images.length > 0 && (
        <div aria-label={t('agent.attachment.imageCount', { count: images.length })}>
          <ImageBlock images={images} variant={variant} />
        </div>
      )}
      {files.length > 0 && <FileBlock files={files} variant={variant} />}
    </div>
  )
})

// ---------------------------------------------------------------------------
// 图片
// ---------------------------------------------------------------------------

interface ImageCellProps {
  att: ParsedAttachment
  variant: AttachmentVariant
  className?: string
  /** >0 时本格显示 `+N` 遮罩（被折叠的图片数）。 */
  hiddenCount?: number
}

function ImageCell({ att, variant, className, hiddenCount = 0 }: ImageCellProps) {
  const { t } = useI18n()
  const [nonce, setNonce] = useState(0)
  const [failed, setFailed] = useState(att.status === 'failed')
  const uploading = att.status === 'uploading'
  const name = att.name || t('agent.attachment.untitled')
  const src = att.url ? withRetryParam(att.url, nonce) : ''
  const percent = Math.round(Math.min(1, Math.max(0, att.progress ?? 0)) * 100)

  const retry = () => {
    if (!att.url) return
    setFailed(false)
    setNonce((n) => n + 1)
  }
  const open = () => {
    if (!att.url || failed || uploading) return
    openLightbox(src, name)
  }

  const rounded = variant === 'rows' ? 'rounded-lg' : 'rounded-xl'
  const label = failed
    ? `${name} — ${t('agent.attachment.retry')}`
    : `${name} — ${t('agent.attachment.openImage')}`

  return (
    <div role="listitem" className={cn('min-w-0', className)}>
      <button
        type="button"
        aria-label={label}
        title={name}
        data-testid="attach-image"
        data-status={failed ? 'failed' : uploading ? 'uploading' : 'ready'}
        onClick={failed ? retry : open}
        className={cn(
          'group/img relative block h-full w-full overflow-hidden border-0 p-0',
          rounded,
          'bg-bg-tertiary/70 ring-1 ring-border/50 transition-[box-shadow,filter] duration-150',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
          failed ? 'cursor-pointer' : uploading ? 'cursor-default' : 'cursor-zoom-in',
          !failed && !uploading && 'hover:brightness-[1.06] hover:ring-border',
        )}
      >
        {!failed && !uploading && src ? (
          <img
            src={src}
            alt={name}
            loading="lazy"
            draggable={false}
            onError={() => setFailed(true)}
            className="absolute inset-0 h-full w-full object-cover"
          />
        ) : null}

        {uploading && (
          <span data-testid="attach-uploading" className="absolute inset-0 flex flex-col items-center justify-center gap-1.5 bg-bg-tertiary/80">
            <span className="text-[11px] font-medium tabular-nums text-text-secondary">{percent}%</span>
            <span className="h-1 w-3/4 overflow-hidden rounded-full bg-border/70">
              <span
                className="block h-full rounded-full bg-accent transition-[width] duration-200"
                style={{ width: `${percent}%` }}
              />
            </span>
          </span>
        )}

        {failed && (
          <span className="absolute inset-0 flex flex-col items-center justify-center gap-1 px-1.5 text-center">
            <ImageOff className="size-4 shrink-0 text-text-muted" aria-hidden />
            <span className="w-full truncate text-[10px] leading-tight text-text-muted">{name}</span>
            <span
              data-testid="attach-image-retry"
              className="flex items-center gap-0.5 text-[10px] font-medium text-accent"
            >
              <RotateCw className="size-2.5" aria-hidden />
              {t('agent.attachment.retry')}
            </span>
          </span>
        )}

        {hiddenCount > 0 && (
          <span
            data-testid="attach-plus-n"
            className="absolute inset-0 flex items-center justify-center bg-black/55 text-sm font-semibold text-white/95 backdrop-blur-[1px]"
          >
            +{hiddenCount}
          </span>
        )}
      </button>
    </div>
  )
}

function ImageBlock({ images, variant }: { images: ParsedAttachment[]; variant: AttachmentVariant }) {
  if (variant === 'rows') {
    const visible = images.slice(0, 4)
    const hidden = images.length - visible.length
    return (
      <div className={cn(BLOCK_W, 'grid grid-cols-4 gap-1.5')}>
        {visible.map((a, i) => (
          <ImageCell
            key={a.id}
            att={a}
            variant={variant}
            className="aspect-square"
            hiddenCount={i === visible.length - 1 && hidden > 0 ? hidden : 0}
          />
        ))}
      </div>
    )
  }

  const isCards = variant === 'cards'
  const visible = images.slice(0, 4)
  const hidden = images.length - visible.length

  let gridClass: string
  if (images.length === 1) {
    gridClass = isCards ? 'grid grid-cols-1' : 'grid grid-cols-1'
  } else if (images.length === 2) {
    gridClass = 'grid grid-cols-2 gap-1.5'
  } else {
    gridClass = 'grid grid-cols-2 gap-1.5'
  }

  return (
    <div className={cn(BLOCK_W, gridClass)}>
      {images.length === 1 ? (
        <SingleImage att={images[0]} variant={variant} />
      ) : images.length === 3 && !isCards ? (
        // 混合式 mosaik：2×2 网格，首图占满左列（经典三图马赛克）。
        <div className="grid aspect-[3/2] grid-cols-2 grid-rows-2 gap-1.5">
          <ImageCell att={images[0]} variant={variant} className="row-span-2" />
          <ImageCell att={images[1]} variant={variant} />
          <ImageCell att={images[2]} variant={variant} />
        </div>
      ) : (
        visible.map((a, i) => (
          <ImageCell
            key={a.id}
            att={a}
            variant={variant}
            className={images.length === 2 ? 'aspect-[4/3]' : 'aspect-square'}
            hiddenCount={i === visible.length - 1 && hidden > 0 ? hidden : 0}
          />
        ))
      )}
    </div>
  )
}

/** 单图：cards = 16/10 大图；mixed = 16/9 hero（带文件名 chip）。 */
function SingleImage({ att, variant }: { att: ParsedAttachment; variant: AttachmentVariant }) {
  const hero = variant === 'mixed'
  return (
    <div className={cn('relative', hero ? 'aspect-[16/9]' : 'aspect-[16/10]')}>
      <ImageCell att={att} variant={variant} className="absolute inset-0" />
      {hero && att.name && att.status !== 'uploading' && (
        <span className="pointer-events-none absolute inset-x-0 bottom-0 truncate rounded-b-xl bg-gradient-to-t from-black/55 to-transparent px-2.5 pb-1.5 pt-5 text-[11px] text-white/90">
          {att.name}
        </span>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 文件
// ---------------------------------------------------------------------------

interface FileCardProps {
  att: ParsedAttachment
  variant: AttachmentVariant
}

function FileCard({ att, variant }: FileCardProps) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  const name = att.name || t('agent.attachment.untitled')
  const failed = att.status === 'failed'
  const uploading = att.status === 'uploading'
  const percent = Math.round(Math.min(1, Math.max(0, att.progress ?? 0)) * 100)
  const Icon = failed ? TriangleAlert : fileIcon(att)
  const color = failed ? 'var(--destructive)' : CATEGORY_COLOR[att.category ?? 'unknown']
  const sizeText = att.size !== undefined ? formatBytes(att.size, t) : ''

  const meta = failed
    ? t('agent.attachment.linkUnavailable')
    : uploading
      ? `${t('agent.attachment.uploading')} ${percent}%`
      : sizeText

  const copyLink = useCallback(() => {
    if (!att.url || !navigator.clipboard?.writeText) return
    void navigator.clipboard.writeText(att.url).then(
      () => {
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1200)
      },
      () => {
        /* 无剪贴板权限（非安全上下文）时静默 */
      },
    )
  }, [att.url])

  const iconTile = (
    <span
      aria-hidden
      className={cn(
        'flex shrink-0 items-center justify-center rounded-lg',
        variant === 'rows' ? 'size-5 rounded-md' : variant === 'mixed' ? 'size-9' : 'size-10',
      )}
      style={{ backgroundColor: `color-mix(in srgb, ${color} 15%, transparent)`, color }}
    >
      {/* eslint-disable-next-line react-hooks/static-components -- Icon is a stable LucideIcon reference */}
      <Icon className={variant === 'rows' ? 'size-3.5' : 'size-[18px]'} />
    </span>
  )

  const body = (
    <span className="flex min-w-0 flex-1 flex-col">
      <span
        className={cn(
          'w-full truncate font-medium leading-tight',
          variant === 'rows' ? 'text-xs' : 'text-[13px]',
          failed && 'text-destructive',
        )}
        title={name}
      >
        {name}
      </span>
      {meta && (
        <span
          className={cn(
            'w-full truncate text-[11px] leading-tight text-text-muted',
            variant === 'rows' ? 'mt-0' : 'mt-0.5',
            failed && 'text-destructive/80',
          )}
        >
          {meta}
        </span>
      )}
      {uploading && (
        <span className="mt-1 block h-1 w-full overflow-hidden rounded-full bg-border/70">
          <span
            className="block h-full rounded-full bg-accent transition-[width] duration-200"
            style={{ width: `${percent}%` }}
          />
        </span>
      )}
    </span>
  )

  const actions = !failed && !uploading && att.url ? (
    <FileActions att={att} copied={copied} onCopy={copyLink} />
  ) : null

  if (variant === 'rows') {
    return (
      <div
        role="listitem"
        data-testid="attach-file"
        data-status={failed ? 'failed' : uploading ? 'uploading' : 'ready'}
        className={cn(
          'group/att flex h-10 w-fit max-w-full min-w-0 items-center gap-2 rounded-lg border bg-bg-primary/40 pl-2.5 pr-1',
          failed ? 'border-destructive/45 bg-destructive/[0.06]' : 'border-border/60',
        )}
      >
        {iconTile}
        {body}
        {actions}
      </div>
    )
  }

  return (
    <div
      role="listitem"
      data-testid="attach-file"
      data-status={failed ? 'failed' : uploading ? 'uploading' : 'ready'}
      className={cn(
        'group/att flex w-full min-w-0 items-center gap-3 bg-bg-primary/50 transition-colors',
        variant === 'cards' ? 'rounded-xl border p-2.5 pr-2' : 'px-3 py-2.5',
        failed
          ? 'border-destructive/45 bg-destructive/[0.06]'
          : variant === 'cards'
            ? 'border-border/60 hover:border-border hover:bg-bg-primary/80'
            : 'hover:bg-bg-primary/75',
      )}
    >
      {iconTile}
      {body}
      {actions}
    </div>
  )
}

function FileBlock({ files, variant }: { files: ParsedAttachment[]; variant: AttachmentVariant }) {
  if (variant === 'rows') {
    return (
      <div className="flex flex-wrap gap-1.5">
        {files.map((f) => (
          <FileCard key={f.id} att={f} variant={variant} />
        ))}
      </div>
    )
  }
  return (
    <div
      className={cn(
        BLOCK_W,
        variant === 'mixed'
          ? 'divide-y divide-border/50 overflow-hidden rounded-xl border border-border/60 bg-bg-primary/45'
          : 'flex flex-col gap-1.5',
      )}
    >
      {files.map((f) => (
        <FileCard key={f.id} att={f} variant={variant} />
      ))}
    </div>
  )
}

/** 文件动作：桌面 hover 显形（图标 ≥32px 命中区）；触屏收进 `⋯` 菜单。 */
function FileActions({
  att,
  copied,
  onCopy,
}: {
  att: ParsedAttachment
  copied: boolean
  onCopy: () => void
}) {
  const { t } = useI18n()
  const isTouch = useIsTouch()

  const btnClass =
    'flex size-8 shrink-0 items-center justify-center rounded-md text-text-muted transition-colors hover:bg-bg-tertiary hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent'

  if (isTouch) {
    return (
      <Popover>
        <PopoverTrigger asChild>
          <button
            type="button"
            data-testid="attach-menu"
            aria-label={t('agent.attachment.moreActions')}
            className={btnClass}
          >
            <MoreHorizontal className="size-4" />
          </button>
        </PopoverTrigger>
        <PopoverContent align="end" side="top" className="w-44 p-1">
          <a
            href={att.url}
            target="_blank"
            rel="noopener noreferrer"
            role="menuitem"
            className="flex h-10 items-center gap-2 rounded-md px-2 text-sm text-text-primary hover:bg-bg-tertiary"
          >
            <ExternalLink className="size-4 text-text-muted" />
            {t('agent.attachment.openNewTab')}
          </a>
          <a
            href={att.url}
            download
            role="menuitem"
            className="flex h-10 items-center gap-2 rounded-md px-2 text-sm text-text-primary hover:bg-bg-tertiary"
          >
            <Download className="size-4 text-text-muted" />
            {t('agent.attachment.download')}
          </a>
          <button
            type="button"
            role="menuitem"
            onClick={onCopy}
            className="flex h-10 w-full items-center gap-2 rounded-md px-2 text-left text-sm text-text-primary hover:bg-bg-tertiary"
          >
            {copied ? <Check className="size-4 text-success" /> : <Copy className="size-4 text-text-muted" />}
            {copied ? t('agent.attachment.copied') : t('agent.attachment.copyLink')}
          </button>
        </PopoverContent>
      </Popover>
    )
  }

  return (
    <span className="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover/att:opacity-100 focus-within:opacity-100">
      <a
        href={att.url}
        target="_blank"
        rel="noopener noreferrer"
        aria-label={t('agent.attachment.openNewTab')}
        title={t('agent.attachment.openNewTab')}
        className={btnClass}
      >
        <ExternalLink className="size-4" />
      </a>
      <a
        href={att.url}
        download
        aria-label={t('agent.attachment.download')}
        title={t('agent.attachment.download')}
        className={btnClass}
      >
        <Download className="size-4" />
      </a>
      <button
        type="button"
        onClick={onCopy}
        aria-label={copied ? t('agent.attachment.copied') : t('agent.attachment.copyLink')}
        title={copied ? t('agent.attachment.copied') : t('agent.attachment.copyLink')}
        className={btnClass}
      >
        {copied ? <Check className="size-4 text-success" /> : <Copy className="size-4" />}
      </button>
    </span>
  )
}

export default AttachmentList
