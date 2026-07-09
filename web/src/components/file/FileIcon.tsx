/**
 * FileIcon — Lucide icon per file extension (Spec 5 §3.7).
 *
 * Resolves the file name to a Lucide icon component; falls back to a generic
 * `File` icon. The icon is rendered by the caller (toolbar/tab) so this stays
 * a pure lookup — pass the returned component `<Icon className=... />`.
 */
import {
  File,
  FileCode,
  FileImage,
  FileJson,
  FileText,
  Settings,
  type LucideIcon,
} from 'lucide-react'

import { fileExt } from './fileTypes'

const ICONS: Record<string, LucideIcon> = {
  // code
  '.ts': FileCode,
  '.tsx': FileCode,
  '.js': FileCode,
  '.jsx': FileCode,
  '.mjs': FileCode,
  '.cjs': FileCode,
  '.go': FileCode,
  '.py': FileCode,
  '.rs': FileCode,
  '.java': FileCode,
  '.kt': FileCode,
  '.c': FileCode,
  '.h': FileCode,
  '.cpp': FileCode,
  '.hpp': FileCode,
  '.cc': FileCode,
  '.cs': FileCode,
  '.rb': FileCode,
  '.php': FileCode,
  '.sh': FileCode,
  '.bash': FileCode,
  '.zsh': FileCode,
  // data / config
  '.json': FileJson,
  '.yaml': Settings,
  '.yml': Settings,
  '.toml': Settings,
  '.ini': Settings,
  '.xml': FileCode,
  '.sql': FileCode,
  // docs
  '.md': FileText,
  '.markdown': FileText,
  '.txt': FileText,
  '.pdf': FileText,
  '.csv': FileText,
  // images
  '.png': FileImage,
  '.jpg': FileImage,
  '.jpeg': FileImage,
  '.gif': FileImage,
  '.webp': FileImage,
  '.svg': FileImage,
}

export function fileIcon(fileName: string): LucideIcon {
  return ICONS[fileExt(fileName)] ?? File
}
