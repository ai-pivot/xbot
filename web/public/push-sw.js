/*
 * WebPush handler, imported by the generated Workbox service worker
 * (vite.config.ts -> workbox.importScripts).
 *
 * The server sends a JSON payload:
 *   { kind, chat_id, title, body, request_id, ts }
 */
self.addEventListener('push', (event) => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch (e) {
    data = { body: event.data ? event.data.text() : '' }
  }
  const title = data.title || 'xbot'
  const chatId = data.chat_id || ''
  const tag = data.request_id || (chatId ? 'chat:' + chatId : 'xbot')
  const options = {
    body: data.body || '',
    icon: '/pwa-192.png',
    badge: '/pwa-192.png',
    tag,
    renotify: true,
    data: { chat_id: chatId },
  }
  event.waitUntil(self.registration.showNotification(title, options))
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const chatId = (event.notification.data && event.notification.data.chat_id) || ''
  const target = chatId ? '/?chat=' + encodeURIComponent(chatId) : '/'
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
    for (const client of windows) {
      if ('focus' in client) {
        await client.focus()
        client.postMessage({ type: 'xbot:open-chat', chatId })
        return
      }
    }
    await self.clients.openWindow(target)
  })())
})
