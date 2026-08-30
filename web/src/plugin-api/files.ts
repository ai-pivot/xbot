/**
 * Plugin Files API — 通用文件存储（所有插件共用，无特化）。
 *
 * 每插件独立目录（{XBOT_HOME}/plugin-files/{plugin_id}/），通过
 * /api/plugin-files/ REST API 上传/列出/删除/下载（鉴权 + 路径安全）。
 * 插件声明 'files' permission 后通过 ctx.files 使用。
 */

export interface UploadedFile {
  filename: string
  url: string
  size: number
  contentType: string
  modTime: string
}

export interface FileUploadOptions {
  /** 可选：指定文件名（缺省用原始名，不安全字符替换为 UUID）。 */
  filename?: string
  /** 可选：子目录（插件目录下，缺省根目录）。 */
  subDir?: string
}

export interface FilesAPI {
  /** 上传文件（multipart POST /api/plugin-files/upload）。 */
  upload(file: File | Blob, opts?: FileUploadOptions): Promise<UploadedFile>
  /** 列出插件的所有文件。 */
  list(): Promise<UploadedFile[]>
  /** 删除文件。 */
  delete(filename: string): Promise<void>
  /** 获取文件的完整 serve URL。 */
  getUrl(filename: string): string
}
