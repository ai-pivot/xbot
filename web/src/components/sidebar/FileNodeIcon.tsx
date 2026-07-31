/**
 * FileNodeIcon — renders file-type icons using Iconify (Material Design Icons).
 *
 * Uses @iconify/react with the "mdi" icon set, bundled locally via
 * @iconify-json/mdi. Each file type gets a brand color via inline style.
 */
import { Icon, addCollection } from '@iconify/react'
import mdiIcons from '@iconify-json/mdi/icons.json'
import type { CSSProperties } from 'react'
import type { FileNode } from '@/types/file'

// Register the full mdi collection locally (no network needed at runtime).
addCollection(mdiIcons)

interface IconDef {
  icon: string
  color: string
}

const SPECIAL_FILES: Record<string, IconDef> = {
  dockerfile: { icon: 'mdi:docker', color: '#2496ed' },
  'package.json': { icon: 'mdi:npm', color: '#cb3837' },
  'readme.md': { icon: 'mdi:file-document', color: '#519aba' },
  license: { icon: 'mdi:file-certificate', color: '#d4ac0d' },
  makefile: { icon: 'mdi:cog', color: '#6d8086' },
  '.gitignore': { icon: 'mdi:git', color: '#f1502f' },
  '.gitattributes': { icon: 'mdi:git', color: '#f1502f' },
  '.env': { icon: 'mdi:file-cog', color: '#ecd53f' },
  'tsconfig.json': { icon: 'mdi:language-typescript', color: '#3178c6' },
}

const EXT_MAP: Record<string, IconDef> = {
  go: { icon: 'mdi:language-go', color: '#00add8' },
  ts: { icon: 'mdi:language-typescript', color: '#3178c6' },
  tsx: { icon: 'mdi:language-typescript', color: '#3178c6' },
  js: { icon: 'mdi:language-javascript', color: '#f7df1e' },
  jsx: { icon: 'mdi:language-javascript', color: '#f7df1e' },
  mjs: { icon: 'mdi:language-javascript', color: '#f7df1e' },
  cjs: { icon: 'mdi:language-javascript', color: '#f7df1e' },
  py: { icon: 'mdi:language-python', color: '#3776ab' },
  rs: { icon: 'mdi:language-rust', color: '#dea584' },
  json: { icon: 'mdi:code-json', color: '#cbcb41' },
  yaml: { icon: 'mdi:file-cog', color: '#6d8086' },
  yml: { icon: 'mdi:file-cog', color: '#6d8086' },
  md: { icon: 'mdi:language-markdown', color: '#519aba' },
  markdown: { icon: 'mdi:language-markdown', color: '#519aba' },
  html: { icon: 'mdi:file-xml', color: '#e34c26' },
  htm: { icon: 'mdi:file-xml', color: '#e34c26' },
  css: { icon: 'mdi:file-code', color: '#563d7c' },
  scss: { icon: 'mdi:file-code', color: '#c6538c' },
  less: { icon: 'mdi:file-code', color: '#1d365d' },
  sql: { icon: 'mdi:database', color: '#e38900' },
  sh: { icon: 'mdi:console', color: '#89e051' },
  bash: { icon: 'mdi:console', color: '#89e051' },
  zsh: { icon: 'mdi:console', color: '#89e051' },
  xml: { icon: 'mdi:file-xml', color: '#e37933' },
  toml: { icon: 'mdi:file-cog', color: '#6d8086' },
  ini: { icon: 'mdi:file-cog', color: '#6d8086' },
  cfg: { icon: 'mdi:file-cog', color: '#6d8086' },
  conf: { icon: 'mdi:file-cog', color: '#6d8086' },
  txt: { icon: 'mdi:file-document', color: '#6d8086' },
  png: { icon: 'mdi:file-image', color: '#a78bfa' },
  jpg: { icon: 'mdi:file-image', color: '#a78bfa' },
  jpeg: { icon: 'mdi:file-image', color: '#a78bfa' },
  gif: { icon: 'mdi:file-image', color: '#a78bfa' },
  webp: { icon: 'mdi:file-image', color: '#a78bfa' },
  svg: { icon: 'mdi:svg', color: '#ffb13b' },
  lock: { icon: 'mdi:lock', color: '#8b8b8b' },
  mod: { icon: 'mdi:language-go', color: '#00add8' },
  sum: { icon: 'mdi:language-go', color: '#00add8' },
  c: { icon: 'mdi:language-c', color: '#555555' },
  h: { icon: 'mdi:language-c', color: '#555555' },
  cpp: { icon: 'mdi:language-cpp', color: '#00599c' },
  hpp: { icon: 'mdi:language-cpp', color: '#00599c' },
  cc: { icon: 'mdi:language-cpp', color: '#00599c' },
  java: { icon: 'mdi:language-java', color: '#ea2d2e' },
  kt: { icon: 'mdi:language-kotlin', color: '#7f52ff' },
  swift: { icon: 'mdi:language-swift', color: '#f05138' },
  rb: { icon: 'mdi:language-ruby', color: '#cc342d' },
  php: { icon: 'mdi:language-php', color: '#777bb4' },
  pdf: { icon: 'mdi:file-pdf-box', color: '#e5252a' },
}

const DEFAULT_ICON: IconDef = { icon: 'mdi:file', color: '#6d8086' }

export function FileNodeIcon({
  node,
  className = 'size-4 shrink-0',
}: {
  node: FileNode
  className?: string
}) {
  const baseName = node.name.toLowerCase()
  const ext = node.name.slice(node.name.lastIndexOf('.') + 1).toLowerCase()

  // Check special filenames first
  let def: IconDef | undefined
  for (const [name, val] of Object.entries(SPECIAL_FILES)) {
    if (baseName === name || (name === 'dockerfile' && baseName.startsWith('dockerfile.')) ||
        (name === 'makefile' && baseName.startsWith('makefile.')) ||
        (name === '.env' && baseName.startsWith('.env.'))) {
      def = val
      break
    }
  }

  // Then by extension
  if (!def) {
    def = EXT_MAP[ext]
  }

  // Fallback
  if (!def) {
    def = DEFAULT_ICON
  }

  const style: CSSProperties = { color: def.color }

  return <Icon icon={def.icon} className={className} style={style} />
}
