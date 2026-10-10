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

/** Capture before React replaces Markdown nodes, not after a collapsed range. */
export class MarkdownSelectionBoundary extends Component<Props, object, SelectionSnapshot | null> {
  getSnapshotBeforeUpdate(previous: Props): SelectionSnapshot | null {
    if (!previous.streaming && !this.props.streaming) return null
    if (previous.content === this.props.content && previous.streaming === this.props.streaming) return null
    if (!this.props.content.startsWith(previous.content)) return null
    const root = this.props.rootRef.current
    const selection = document.getSelection()
    if (!root || !selection || selection.isCollapsed || selection.rangeCount !== 1 ||
      !root.contains(selection.anchorNode) || !root.contains(selection.focusNode)) return null
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
    // A new iteration or changed Markdown can change the quoted prefix. Never
    // reattach an old range to different text just because its offsets fit.
    if (!root.textContent!.startsWith(snapshot.prefix)) return
    const point = (offset: number): [Node, number] => {
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
      let node = walker.nextNode()!
      while (offset > node.textContent!.length) {
        offset -= node.textContent!.length
        node = walker.nextNode()!
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
