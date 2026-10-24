import {
  registerWebPush,
  unregisterWebPush,
  type WebPushSubscription,
} from '@/lib/api'

/**
 * The browser's PushSubscription (if any). Returns null in environments
 * without a service worker (tests / non-HTTPS / unsupported browsers).
 */
async function currentSubscription(): Promise<PushSubscription | null> {
  if (
    typeof navigator === 'undefined' ||
    !('serviceWorker' in navigator) ||
    typeof window === 'undefined' ||
    !('PushManager' in window)
  ) {
    return null
  }
  const registration = await navigator.serviceWorker.getRegistration()
  if (!registration) return null
  return registration.pushManager.getSubscription()
}

/**
 * Re-bind the existing browser subscription to the currently logged-in user.
 * The server upserts by (provider, address), so this is safe after every login
 * and on boot — it fixes "shared browser still bound to the previous account".
 */
export async function registerCurrentWebPush(): Promise<boolean> {
  const sub = await currentSubscription()
  if (!sub) return false
  const json = sub.toJSON()
  const payload: WebPushSubscription = {
    endpoint: json.endpoint || sub.endpoint,
    keys: json.keys,
  }
  await registerWebPush(payload, navigator.userAgent)
  return true
}

/**
 * Remove this browser's push target from the server and unsubscribe locally.
 * Called on logout while the auth cookie is still valid.
 */
export async function unregisterCurrentWebPush(): Promise<void> {
  const sub = await currentSubscription()
  if (!sub) return
  try {
    await unregisterWebPush(sub.endpoint)
  } finally {
    // Always remove the local subscription: even if the server call failed, the
    // browser must stop receiving pushes meant for the previous account.
    await sub.unsubscribe()
  }
}
