/**
 * File type helpers (Spec 5 §3.2/§3.7).
 *
 * Pure functions shared by FileIcon, useFileContent and FilePanel to decide
 * how a file is rendered (editor vs preview vs image) and which Monaco
 * language its extension maps to. Kept framework-free so it is trivially
 * testable and has no React import cost.
 */

export type FileViewMode = 'editor' | 'preview'

/** Lowercased extension *with* the dot, e.g. `.tsx`. `''` when none. */
export function fileExt(fileName: string): string {
  const i = fileName.lastIndexOf('.')
  if (i <= 0 || i === fileName.length - 1) return ''
  return fileName.slice(i).toLowerCase()
}

const IMAGE_EXTS = new Set(['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg'])

export function isImageFile(fileName: string): boolean {
  return IMAGE_EXTS.has(fileExt(fileName))
}

const HTML_EXTS = new Set(['.html', '.htm'])

export function isHtmlFile(fileName: string): boolean {
  return HTML_EXTS.has(fileExt(fileName))
}

const MARKDOWN_EXTS = new Set(['.md', '.markdown'])

export function isMarkdownFile(fileName: string): boolean {
  return MARKDOWN_EXTS.has(fileExt(fileName))
}

/**
 * Map a file extension to a Monaco language id.
 *
 * Falls back to `'plaintext'` so unknown files still open as editable text
 * rather than erroring. Monaco ships built-in grammars for the languages
 * listed below; adding a new one only needs a key here.
 */
const EXT_TO_LANGUAGE: Record<string, string> = {
  '.ts': 'typescript',
  '.tsx': 'typescript',
  '.js': 'javascript',
  '.jsx': 'javascript',
  '.mjs': 'javascript',
  '.cjs': 'javascript',
  '.json': 'json',
  '.go': 'go',
  '.py': 'python',
  '.rs': 'rust',
  '.java': 'java',
  '.kt': 'kotlin',
  '.swift': 'swift',
  '.c': 'c',
  '.h': 'c',
  '.cpp': 'cpp',
  '.hpp': 'cpp',
  '.cc': 'cpp',
  '.cs': 'csharp',
  '.rb': 'ruby',
  '.php': 'php',
  '.sh': 'shell',
  '.bash': 'shell',
  '.zsh': 'shell',
  '.yaml': 'yaml',
  '.yml': 'yaml',
  '.toml': 'ini',
  '.ini': 'ini',
  '.xml': 'xml',
  '.html': 'html',
  '.htm': 'html',
  '.css': 'css',
  '.scss': 'scss',
  '.less': 'less',
  '.sql': 'sql',
  '.md': 'markdown',
  '.markdown': 'markdown',
}

export function languageOf(fileName: string): string {
  const ext = fileExt(fileName)
  if (ext === '' && /dockerfile/i.test(fileName)) return 'dockerfile'
  return EXT_TO_LANGUAGE[ext] ?? 'plaintext'
}

/**
 * Decide the default view mode for a file (Spec 5 §3.2).
 *
 *   - Markdown → preview (can switch to editor)
 *   - Image    → preview (no toggle)
 *   - Other    → editor  (can switch to preview only for markdown-like)
 *
 * The toolbar hides the toggle for images; the panel uses this only to seed
 * the initial mode.
 */
export function defaultViewMode(fileName: string): FileViewMode {
  if (isMarkdownFile(fileName) || isImageFile(fileName) || isHtmlFile(fileName)) return 'preview'
  return 'editor'
}

/** Whether the editor↔preview toggle should be offered for this file. */
export function canTogglePreview(fileName: string): boolean {
  return isMarkdownFile(fileName) || isHtmlFile(fileName)
}
