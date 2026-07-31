/**
 * File tree types shared by the file browser, search, and icon components.
 *
 * The `FileNode` shape mirrors the backend `list_files` RPC response
 * (serverapp/rpc_table.go fileEntry). The tree is fetched via the `useFileTree`
 * hook which calls the RPC on demand and auto-refreshes when the CWD changes.
 */
export interface FileNode {
  name: string
  path: string
  type: 'file' | 'directory'
  children?: FileNode[]
  /** Monaco-compatible language id (typescript, go, json, ...). */
  language?: string
}

/** Flatten a tree into a list of file leaves (search projection). */
export function flattenFiles(nodes: FileNode[]): FileNode[] {
  const out: FileNode[] = []
  const walk = (list: FileNode[]): void => {
    for (const node of list) {
      if (node.type === 'directory') {
        if (node.children) walk(node.children)
      } else {
        out.push(node)
      }
    }
  }
  walk(nodes)
  return out
}
