import { useEffect, useRef, useState, type ReactNode } from 'react'
import i18n from '@/i18n'
import {
  activatePreviewConnection, backendUrl, clearPreviewBackend, clearPreviewConnection, normalizeBackendUrl,
  PreviewConnectionError, readPreviewBackend, revokePreviewConnection, savePreviewBackend, testPreviewConnection, verifyPreviewToken,
} from '@/lib/previewConnection'
import { previewCopy, previewLocale } from './copy'
import './preview.css'

/** Backend providers must remain children, never ancestors, of this boundary. */
export function PreviewGate({ children }: { children: ReactNode }) {
  const copy = previewCopy[previewLocale(i18n.resolvedLanguage || i18n.language || navigator.language)]
  const [address, setAddress] = useState(readPreviewBackend)
  const [status, setStatus] = useState<string>(copy.idle)
  const [saved, setSaved] = useState('')
  const [busy, setBusy] = useState(false)
  const attempt = useRef<{ id: number; controller?: AbortController }>({ id: 0 })
  const [token, setToken] = useState('')
  const [consent, setConsent] = useState(false)
  const expiryTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const [authorized, setAuthorized] = useState(false)

  function cancel() {
    attempt.current.controller?.abort()
    attempt.current = { id: attempt.current.id + 1 }
    setBusy(false)
  }
  useEffect(() => () => { attempt.current.controller?.abort(); attempt.current.id++; clearTimeout(expiryTimer.current); clearPreviewConnection() }, [])

  async function test() {
    cancel()
    let base: string
    try { base = normalizeBackendUrl(address) } catch { setStatus(copy.invalid); return }
    const controller = new AbortController()
    const id = attempt.current.id
    attempt.current.controller = controller
    setBusy(true)
    setStatus(copy.testing)
    try {
      await testPreviewConnection(base, controller.signal)
      if (attempt.current.id === id) setStatus(copy.success)
    } catch (error) {
      if (attempt.current.id !== id) return
      if (error instanceof PreviewConnectionError) {
        setStatus(error.reason === 'http' ? `${copy.http} ${error.status}.` : copy[error.reason])
      } else setStatus(copy.cancelled)
    } finally {
      if (attempt.current.id === id) setBusy(false)
    }
  }

  async function connect() {
    cancel()
    if (!consent || !token.trim()) return
    const controller = new AbortController()
    const id = attempt.current.id
    attempt.current.controller = controller
    setBusy(true)
    setStatus(copy.testing)
    try {
      const base = normalizeBackendUrl(address)
      const expiry = await verifyPreviewToken(base, token, controller.signal)
      if (id !== attempt.current.id) return
      activatePreviewConnection(base, token)
      setToken('')
      setAuthorized(true)
      clearTimeout(expiryTimer.current)
      expiryTimer.current = setTimeout(() => {
        clearPreviewConnection(); window.location.reload()
      }, Math.min(Date.parse(expiry) - Date.now(), 2_147_483_647))
    } catch (error) {
      if (id !== attempt.current.id) return
      setStatus(error instanceof PreviewConnectionError
        ? error.reason === 'http' ? `${copy.http} ${error.status}.` : copy[error.reason]
        : copy.invalid)
    } finally { if (id === attempt.current.id) setBusy(false) }
  }

  let authorizationUrl: string | undefined
  try { authorizationUrl = backendUrl(`/preview/authorize?origin=${encodeURIComponent(window.location.origin)}`, normalizeBackendUrl(address)) } catch { /* Invalid address: no authorization link. */ }

  function save() {
    try {
      const base = normalizeBackendUrl(address)
      setAddress(base)
      setSaved(savePreviewBackend(base) ? copy.saved : copy.storage)
    } catch { setSaved(copy.invalid) }
  }

  return <div className={`preview-shell${authorized ? ' preview-connected' : ''}`}>
    <header className="preview-toolbar">
      <span className="preview-wordmark">xbot <span>{copy.eyebrow}</span></span>
      <button type="button" onClick={() => {
        cancel(); clearTimeout(expiryTimer.current); revokePreviewConnection(); clearPreviewConnection(); window.location.reload()
      }}>{copy.change}</button>
    </header>
    {authorized ? <div className="preview-application">{children}</div> : <main className="preview-main">
      <div className="preview-heading"><p className="preview-eyebrow">{copy.eyebrow}</p>
        <h1>{copy.title}</h1><p>{copy.intro}</p></div>
      <section className="preview-settings" aria-label={copy.title}>
        <form onSubmit={event => { event.preventDefault(); save() }}>
          <label htmlFor="preview-backend">{copy.label}</label>
          <input id="preview-backend" type="text" inputMode="url" spellCheck={false}
            autoCapitalize="none" autoComplete="off" value={address} aria-describedby="preview-backend-hint"
            onChange={event => { cancel(); setAddress(event.target.value); setToken(''); setConsent(false); setStatus(copy.idle); setSaved('') }} />
          <p id="preview-backend-hint" className="preview-hint">{copy.hint}</p>
          <div className="preview-actions">
            <button className="preview-primary" type="submit">{copy.save}</button>
            <button type="button" disabled={busy} onClick={() => void test()}>{busy ? copy.testing : copy.test}</button>
            {busy && <button type="button" onClick={() => { cancel(); setStatus(copy.cancelled) }}>{copy.cancel}</button>}
          </div>
        </form>
        {saved && <p role="status" className="preview-hint">{saved}</p>}
        <p role="status" aria-live="polite" className="preview-result">{status}</p>
      </section>
      <section className="preview-notice"><h2>{copy.blockedTitle}</h2><p>{copy.blocked}</p></section>
      <section className="preview-auth" aria-label={copy.authorize}>
        {authorizationUrl && <a href={authorizationUrl} target="_blank" rel="noopener noreferrer">{copy.authorize} ↗</a>}
        <label htmlFor="preview-token">{copy.token}</label>
        <input id="preview-token" type="password" value={token} autoComplete="off" spellCheck={false}
          onChange={event => { cancel(); setToken(event.target.value); setStatus(copy.idle) }} />
        <label className="preview-consent"><input type="checkbox" checked={consent} onChange={event => { cancel(); setConsent(event.target.checked) }} />{copy.consent}</label>
        <button type="button" className="preview-primary" disabled={busy || !consent || !token.trim()} onClick={() => void connect()}>{copy.enter}</button>
      </section>
      <aside className="preview-warning"><h2>{copy.warningTitle}</h2><p>{copy.warning}</p></aside>
      <button className="preview-reset" type="button" onClick={() => {
        cancel(); clearTimeout(expiryTimer.current); revokePreviewConnection(); clearPreviewConnection(); clearPreviewBackend(); window.location.reload()
      }}>{copy.reset}</button>
    </main>}
  </div>
}
