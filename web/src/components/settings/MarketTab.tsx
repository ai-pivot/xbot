import { useTranslation } from '../../i18n'
import { useEffect, useState, useCallback } from 'react'
import { IconCopy, IconBot, IconPackage, IconInbox, IconCheck, IconSettings } from '../Icons'

import type { MarketEntry, MyMarketEntry } from './shared'

export default function MarketTab() {
  const [marketType, setMarketType] = useState<'agent' | 'skill'>('agent')
  const [marketSubTab, setMarketSubTab] = useState<'browse' | 'mine'>('browse')
  const [marketEntries, setMarketEntries] = useState<MarketEntry[]>([])
  const [myMarketEntries, setMyMarketEntries] = useState<MyMarketEntry[]>([])
  const [marketLoading, setMarketLoading] = useState(false)
  const { t } = useTranslation()

  const loadMarket = useCallback(async () => {
    setMarketLoading(true)
    try {
      const resp = await fetch(`/api/market?type=${marketType}&limit=50`)
      const data = await resp.json()
      if (data.ok) setMarketEntries(data.entries || [])
    } catch (err) { console.warn('[MarketTab] loadMarket failed:', err) }
    setMarketLoading(false)
  }, [marketType])

  const handleInstall = useCallback(async (entry: MarketEntry) => {
    try {
      const resp = await fetch('/api/market/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: entry.type, id: entry.id }),
      })
      const data = await resp.json()
      if (data.ok) loadMarket()
    } catch (err) { console.warn('[MarketTab] install failed:', err) }
  }, [loadMarket])

  const handleUninstall = useCallback(async (entry: MarketEntry) => {
    try {
      const resp = await fetch('/api/market/uninstall', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: entry.type, name: entry.name }),
      })
      const data = await resp.json()
      if (data.ok) loadMarket()
    } catch (err) { console.warn('[MarketTab] uninstall failed:', err) }
  }, [loadMarket])

  const loadMyMarket = useCallback(async () => {
    setMarketLoading(true)
    try {
      const resp = await fetch(`/api/market/my?type=${marketType}`)
      const data = await resp.json()
      if (data.ok) setMyMarketEntries(data.entries || [])
    } catch (err) { console.warn('[MarketTab] loadMyMarket failed:', err) }
    setMarketLoading(false)
  }, [marketType])

  const handlePublish = useCallback(async (entry: MyMarketEntry) => {
    try {
      const resp = await fetch('/api/market/publish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: entry.type, name: entry.name }),
      })
      const data = await resp.json()
      if (data.ok) {
        setMyMarketEntries(prev => prev.map(e =>
          e.name === entry.name && e.type === entry.type ? { ...e, published: true } : e
        ))
      }
    } catch (err) { console.warn('[MarketTab] publish failed:', err) }
  }, [])

  const handleUnpublish = useCallback(async (entry: MyMarketEntry) => {
    try {
      const resp = await fetch('/api/market/unpublish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: entry.type, name: entry.name }),
      })
      const data = await resp.json()
      if (data.ok) {
        setMyMarketEntries(prev => prev.map(e =>
          e.name === entry.name && e.type === entry.type ? { ...e, published: false } : e
        ))
      }
    } catch (err) { console.warn('[MarketTab] publish failed:', err) }
  }, [])

  // Load market data when sub-tab or type changes
  useEffect(() => {
    if (marketSubTab === 'browse') loadMarket()
    else loadMyMarket()
  }, [marketSubTab, loadMarket, loadMyMarket])

  const sectionClass = 'settings-section'
  const sectionTitleClass = 'settings-section-title'

  return (
    <div className={sectionClass}>
      <div className={sectionTitleClass}>{t("marketTitle")}</div>
      <div className="market-tab-bar">
        <button
          className={`market-tab ${marketType === 'agent' ? 'active' : ''}`}
          onClick={() => { setMarketType('agent'); setMarketSubTab('browse'); }}
        >
          <IconBot className="inline" /> Agent
        </button>
        <button
          className={`market-tab ${marketType === 'skill' ? 'active' : ''}`}
          onClick={() => { setMarketType('skill'); setMarketSubTab('browse'); }}
        >
          <IconSettings className="inline" /> Skill
        </button>
      </div>
      {/* Sub tabs: browse / mine */}
      <div className="market-sub-tab-bar">
        <button
          className={`market-tab market-sub-tab ${marketSubTab === 'browse' ? 'active' : ''}`}
          onClick={() => setMarketSubTab('browse')}
        >
          <IconPackage className="inline" /> 市场
        </button>
        <button
          className={`market-tab market-sub-tab ${marketSubTab === 'mine' ? 'active' : ''}`}
          onClick={() => setMarketSubTab('mine')}
        >
          <IconCopy className="inline" /> 我的
        </button>
      </div>

      {marketSubTab === 'browse' && (
        marketLoading ? (
          <div className="settings-loading">
            <div className="market-spinner" />
            <p className="text-xs mt-2">加载中...</p>
          </div>
        ) : marketEntries.length === 0 ? (
          <div className="settings-loading">
            <p className="text-3xl mb-3"><IconInbox className="inline" style={{width:28,height:28}} /></p>
            <p className="text-sm">暂无可用条目</p>
          </div>
        ) : (
          <div className="market-entry-list">
            {marketEntries.map(entry => (
              <div key={entry.id} className="market-entry">
                <div className="market-entry-header">
                  <div className="market-entry-info">
                    <span className="market-entry-name">{entry.name}</span>
                    <span className="market-entry-author">by {entry.author}</span>
                  </div>
                  {entry.installed ? (
                    <button className="market-uninstall-btn" onClick={() => handleUninstall(entry)}>
                      卸载
                    </button>
                  ) : (
                    <button className="market-install-btn" onClick={() => handleInstall(entry)}>
                      安装
                    </button>
                  )}
                </div>
                {entry.description && (
                  <p className="market-entry-desc">{entry.description}</p>
                )}
              </div>
            ))}
          </div>
        )
      )}

      {marketSubTab === 'mine' && (
        marketLoading ? (
          <div className="settings-loading">
            <div className="market-spinner" />
            <p className="text-xs mt-2">加载中...</p>
          </div>
        ) : myMarketEntries.length === 0 ? (
          <div className="settings-loading">
            <p className="text-3xl mb-3"><IconInbox className="inline" style={{width:28,height:28}} /></p>
            <p className="text-sm">暂无自己的{marketType === 'skill' ? ' Skill' : ' Agent'}</p>
          </div>
        ) : (
          <div className="market-entry-list">
            {myMarketEntries.map(entry => (
              <div key={entry.name} className="market-entry">
                <div className="market-entry-header">
                  <div className="market-entry-info">
                    <span className="market-entry-name">{entry.name}</span>
                    <span className={`market-entry-status ${entry.published ? 'published' : 'unpublished'}`}>
                      {entry.published ? <><IconCheck className="inline" /> 已上架</> : <><span style={{width:10,height:10,borderRadius:'50%',display:'inline-block',border:'1.5px solid currentColor',verticalAlign:'middle'}} /> 未上架</>}
                    </span>
                  </div>
                  {entry.published ? (
                    <button className="market-unpublish-btn" onClick={() => handleUnpublish(entry)}>
                      下架
                    </button>
                  ) : (
                    <button className="market-install-btn" onClick={() => handlePublish(entry)}>
                      上架
                    </button>
                  )}
                </div>
                {entry.description && (
                  <p className="market-entry-desc">{entry.description}</p>
                )}
              </div>
            ))}
          </div>
        )
      )}
    </div>
  )
}
