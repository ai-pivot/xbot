/**
 * wallpapers — 用户上传壁纸（IndexedDB 持久化 + canvas 浏览器端压缩）。
 *
 * 浏览器本地存储（不上服务器）：资产放 IndexedDB（dataURL），配置
 * （选了哪张）走 localStorage 'xbot:ambience'（user_settings 跨设备同步
 * 只同步配置引用——其他设备回落插件预设，资产本机）。
 *
 * 上传管线：File → FileReader dataURL → Image → canvas 压缩（最长边
 * maxDim 内）→ toDataURL('image/webp', 0.85) → IndexedDB put → 注册进
 * ambience store 内存缓存（resolveWallpaper 'user:' 前缀消费）。
 * GIF（动图）不做 canvas 压缩（会丢动画）——原样存（超限拒绝）。
 * tone（亮度基调）采样已随 tone 感知玻璃一并移除（2026-08-29 用户要求：
 * 壁纸不改变 UI 主题色——所有壁纸统一走主题色 alpha 化）。
 */

const DB_NAME = 'xbot-ambience'
const STORE = 'wallpapers'

export interface UserWallpaperMeta {
  id: string
  name: string
  /** 上传时间（ms epoch）——列表排序用。 */
  createdAt: number
}

interface UserWallpaperRecord extends UserWallpaperMeta {
  dataUrl: string
}

let dbPromise: Promise<IDBDatabase> | null = null

function openDB(): Promise<IDBDatabase> {
  if (dbPromise) return dbPromise
  dbPromise = new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1)
    req.onupgradeneeded = () => {
      if (!req.result.objectStoreNames.contains(STORE)) {
        req.result.createObjectStore(STORE, { keyPath: 'id' })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error ?? new Error('indexedDB open failed'))
  })
  return dbPromise
}

async function withStore<T>(mode: IDBTransactionMode, fn: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const db = await openDB()
  return new Promise<T>((resolve, reject) => {
    const tx = db.transaction(STORE, mode)
    const req = fn(tx.objectStore(STORE))
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error ?? new Error('indexedDB request failed'))
  })
}

/** 读文件为 dataURL。 */
function readFileAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as string)
    reader.onerror = () => reject(reader.error ?? new Error('FileReader failed'))
    reader.readAsDataURL(file)
  })
}

/** dataURL → HTMLImageElement（解码）。 */
function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error('图片解码失败（不支持的格式）'))
    img.src = src
  })
}

/** 平均亮度采样已随 tone 感知玻璃一并移除（2026-08-29 用户要求：
 * 壁纸不改变 UI 主题色——所有壁纸统一走主题色 alpha 化）。 */

/** canvas 压缩：最长边 maxDim 内缩放 → webp 0.85（解码失败/超限回退原 dataURL）。 */
async function compress(dataUrl: string, maxDim: number): Promise<string> {
  const img = await loadImage(dataUrl)
  const w = img.naturalWidth
  const h = img.naturalHeight
  if (w <= maxDim && h <= maxDim) return dataUrl
  const scale = Math.min(1, maxDim / Math.max(w, h))
  const c = document.createElement('canvas')
  c.width = Math.max(1, Math.round(w * scale))
  c.height = Math.max(1, Math.round(h * scale))
  const ctx = c.getContext('2d')
  if (!ctx) return dataUrl
  ctx.drawImage(img, 0, 0, c.width, c.height)
  try {
    const webp = c.toDataURL('image/webp', 0.85)
    // webp 编码不支持时 toDataURL 回退 png dataURL（仍以 image/png 开头）。
    return webp.startsWith('data:image/webp') ? webp : c.toDataURL('image/png')
  } catch {
    return dataUrl
  }
}

function newId(): string {
  return `user:${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`
}

/** 上传壁纸（压缩 + IndexedDB 持久化），返回记录。
 * 大小/类型不限制（用户要求取消限制——大 GIF 原样存）。 */
export async function uploadUserWallpaper(file: File, maxDim = 1600): Promise<UserWallpaperRecord> {
  const raw = await readFileAsDataURL(file)
  // GIF 保留原样（canvas 会丢动画）；其余压缩到 maxDim。
  const dataUrl = file.type === 'image/gif' ? raw : await compress(raw, maxDim)
  const record: UserWallpaperRecord = {
    id: newId(),
    name: file.name.replace(/\.[^.]+$/, '') || 'wallpaper',
    createdAt: Date.now(),
    dataUrl,
  }
  await withStore('readwrite', (s) => s.put(record))
  return record
}

/** 全量列出用户壁纸（按上传时间倒序）。 */
export async function listUserWallpapers(): Promise<UserWallpaperRecord[]> {
  try {
    const all = await withStore<UserWallpaperRecord[]>('readonly', (s) => s.getAll() as IDBRequest<UserWallpaperRecord[]>)
    return all.sort((a, b) => b.createdAt - a.createdAt)
  } catch {
    return []
  }
}

/** 删除用户壁纸。 */
export async function removeUserWallpaper(id: string): Promise<boolean> {
  try {
    await withStore('readwrite', (s) => s.delete(id))
    return true
  } catch {
    return false
  }
}
