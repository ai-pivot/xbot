import { useState, useCallback, useRef } from 'react'
import { useTranslation } from '../i18n'
import type { Tab } from '../hooks/useTabManager'

interface TabBarProps {
  tabs: Tab[]
  activeTabId: string
  onTabClick: (chatId: string) => void
  onTabClose: (chatId: string) => void
  onReorder: (fromIndex: number, toIndex: number) => void
}

export default function TabBar({ tabs, activeTabId, onTabClick, onTabClose, onReorder }: TabBarProps) {
  const { t } = useTranslation()
  const [dragIndex, setDragIndex] = useState<number | null>(null)
  const [overIndex, setOverIndex] = useState<number | null>(null)
  const dragCounterRef = useRef(0)

  const handleDragStart = useCallback((e: React.DragEvent, index: number) => {
    setDragIndex(index)
    // Use a minimal drag image for cleaner visual feedback
    const img = new Image()
    img.src = 'data:image/gif;base64,R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs='
    e.dataTransfer.setDragImage(img, 0, 0)
    e.dataTransfer.effectAllowed = 'move'
  }, [])

  const handleDragOver = useCallback((e: React.DragEvent, index: number) => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    setOverIndex(index)
  }, [])

  const handleDragEnter = useCallback(() => {
    dragCounterRef.current++
  }, [])

  const handleDragLeave = useCallback(() => {
    dragCounterRef.current--
    if (dragCounterRef.current === 0) {
      setOverIndex(null)
    }
  }, [])

  const handleDrop = useCallback((e: React.DragEvent, toIndex: number) => {
    e.preventDefault()
    if (dragIndex !== null && dragIndex !== toIndex) {
      onReorder(dragIndex, toIndex)
    }
    setDragIndex(null)
    setOverIndex(null)
    dragCounterRef.current = 0
  }, [dragIndex, onReorder])

  const handleDragEnd = useCallback(() => {
    setDragIndex(null)
    setOverIndex(null)
    dragCounterRef.current = 0
  }, [])

  if (tabs.length === 0) return null

  return (
    <div className="tab-bar" role="tablist" data-testid="tab-bar">
      <div className="tab-bar-scroll">
        {tabs.map((tab, index) => {
          const isActive = tab.chatId === activeTabId
          const isDragging = dragIndex === index
          const isDropTarget = overIndex === index && dragIndex !== index
          return (
            <div
              key={tab.chatId}
              className={`tab-item ${isActive ? 'tab-item-active' : ''} ${isDragging ? 'tab-item-dragging' : ''} ${isDropTarget ? 'tab-item-drop-target' : ''}`}
              role="tab"
              aria-selected={isActive}
              data-testid={`tab-item-${tab.chatId}`}
              draggable
              onDragStart={(e) => handleDragStart(e, index)}
              onDragOver={(e) => handleDragOver(e, index)}
              onDragEnter={handleDragEnter}
              onDragLeave={handleDragLeave}
              onDrop={(e) => handleDrop(e, index)}
              onDragEnd={handleDragEnd}
            >
              <button
                className="tab-item-label"
                onClick={() => onTabClick(tab.chatId)}
                title={tab.label}
              >
                <span className="tab-item-text">{tab.label || t('unnamedSession')}</span>
              </button>
              <button
                className="tab-item-close"
                onClick={(e) => { e.stopPropagation(); onTabClose(tab.chatId) }}
                title={t('closeTab')}
                aria-label={`${t('closeTab')} ${tab.label}`}
              >
                ✕
              </button>
            </div>
          )
        })}
      </div>
    </div>
  )
}
