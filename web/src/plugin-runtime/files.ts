/**
 * PluginFileService — 通用文件存储实现（所有插件共用）。
 *
 * 通过 /api/plugin-files/ REST API 与后端交互（上传/列出/删除/下载）。
 * 插件声明 'files' permission 后通过 ctx.files 使用。
 */
import type { FileUploadOptions, UploadedFile } from '@/plugin-api/files'

export class PluginFileService {
  private pluginId: string

  constructor(pluginId: string) {
    this.pluginId = pluginId
  }

  async upload(file: File | Blob, opts?: FileUploadOptions): Promise<UploadedFile> {
    const form = new FormData()
    form.append('plugin_id', this.pluginId)
    form.append('file', file, opts?.filename || (file instanceof File ? file.name : 'upload.bin'))

    const res = await fetch('/api/plugin-files/upload', { method: 'POST', body: form })
    if (!res.ok) {
      const err = await res.text().catch(() => res.statusText)
      throw new Error(`upload failed: ${err}`)
    }
    const json = (await res.json()) as { ok: boolean; data?: UploadedFile; error?: string }
    if (!json.ok || !json.data) throw new Error(json.error || 'upload failed')
    return json.data
  }

  async list(): Promise<UploadedFile[]> {
    const res = await fetch(`/api/plugin-files/${this.pluginId}`)
    if (!res.ok) return []
    const json = (await res.json()) as { ok: boolean; data?: UploadedFile[] }
    return json.data ?? []
  }

  async delete(filename: string): Promise<void> {
    const res = await fetch(`/api/plugin-files/${this.pluginId}/${filename}`, { method: 'DELETE' })
    if (!res.ok) throw new Error(`delete failed: ${res.statusText}`)
  }

  getUrl(filename: string): string {
    return `/api/plugin-files/${this.pluginId}/${filename}`
  }
}
