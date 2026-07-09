/**
 * FileNodeIcon — renders the Lucide file-type icon for a FileNode.
 *
 * Each icon is rendered explicitly per language (no `const Icon = fileIcon(...)`
 * assignment) so the react-hooks/static-components rule stays happy — resolved
 * icon components shouldn't be assigned to a `const` during render. Directories
 * are handled by the caller (FolderOpen); this only handles file leaves.
 */
import { File, FileCode, FileJson, FileText, Hash } from 'lucide-react'
import type { FileNode } from '@/types/file'

export function FileNodeIcon({
  node,
  className = 'size-4 shrink-0 text-text-secondary',
}: {
  node: FileNode
  className?: string
}) {
  switch (node.language) {
    case 'typescript':
    case 'javascript':
      return <FileCode className={className} />
    case 'json':
      return <FileJson className={className} />
    case 'markdown':
      return <FileText className={className} />
    case 'css':
      return <Hash className={className} />
    default:
      return <File className={className} />
  }
}
