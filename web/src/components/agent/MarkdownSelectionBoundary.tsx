import { Component, type ReactNode, type RefObject } from 'react'

interface Props {
  children: ReactNode
  rootRef: RefObject<HTMLDivElement | null>
  className: string
  streaming: boolean
  content: string
}

interface SelectionSnapshot {
  anchor: number
  focus: number
  prefix: string
}

const MAX_SELECTION_CHARS = 16384
const MAX_SELECTION_NODES = 256

/** Bound additional selection work independently of the reply's total size. */
function boundedTextNodes(root: HTMLElement): Text[] | null {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  let chars = 0
  let node: Node | null
  while ((node = walker.nextNode())) {
    chars += (node as Text).length
    if (chars > MAX_SELECTION_CHARS || nodes.length === MAX_SELECTION_NODES) return null
    nodes.push(node as Text)
  }
  return nodes
}

/** Capture before React replaces Markdown nodes, not after a collapsed range. */
export class MarkdownSelectionBoundary extends Component<Props, object, SelectionSnapshot | null> {
  getSnapshotBeforeUpdate(previous: Props): SelectionSnapshot | null {
    if (!previous.streaming && !this.props.streaming) return null
    if (previous.content === this.props.content && previous.streaming === this.props.streaming) return null
    if (previous.content.length > MAX_SELECTION_CHARS || this.props.content.length > MAX_SELECTION_CHARS) return null
    if (!this.props.content.startsWith(previous.content)) return null
    const root = this.props.rootRef.current
    const selection = document.getSelection()
    if (!root || !selection || selection.isCollapsed || selection.rangeCount !== 1 ||
      !root.contains(selection.anchorNode) || !root.contains(selection.focusNode)) return null
    if (!boundedTextNodes(root)) return null
    const offset = (node: Node, position: number) => {
      const range = document.createRange()
      range.selectNodeContents(root)
      range.setEnd(node, position)
      return range.toString().length
    }
    const anchor = offset(selection.anchorNode!, selection.anchorOffset)
    const focus = offset(selection.focusNode!, selection.focusOffset)
    return { anchor, focus, prefix: root.textContent!.slice(0, Math.max(anchor, focus)) }
  }

  componentDidUpdate(_previous: Props, _state: object, snapshot: SelectionSnapshot | null) {
    if (!snapshot) return
    const root = this.props.rootRef.current!
    const nodes = boundedTextNodes(root)
    if (!nodes) return
    // A new iteration or changed Markdown can change the quoted prefix. Never
    // reattach an old range to different text just because its offsets fit.
    if (!root.textContent!.startsWith(snapshot.prefix)) return
    const point = (offset: number): [Node, number] => {
      let index = 0
      let node = nodes[index]
      while (offset > node.textContent!.length) {
        offset -= node.textContent!.length
        node = nodes[++index]
      }
      return [node, offset]
    }
    const [anchor, anchorOffset] = point(snapshot.anchor)
    const [focus, focusOffset] = point(snapshot.focus)
    const selection = document.getSelection()!
    if (selection.anchorNode === anchor && selection.anchorOffset === anchorOffset &&
      selection.focusNode === focus && selection.focusOffset === focusOffset) return
    selection.setBaseAndExtent(anchor, anchorOffset, focus, focusOffset)
  }

  render() {
    return <div ref={this.props.rootRef} className={this.props.className}>{this.props.children}</div>
  }
}
