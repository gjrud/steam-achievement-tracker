// Test stand-in for @wailsio/runtime (aliased in vite.config.js, test mode only). It keeps Events.On
// subscriptions in memory so tests can emit events and count listeners.

const listeners = new Map()

export const Events = {
  // On mirrors the runtime: it returns a function that removes this subscription.
  On(name, callback) {
    let set = listeners.get(name)
    if (!set) {
      set = new Set()
      listeners.set(name, set)
    }
    const entry = {callback}
    set.add(entry)
    return () => {
      const current = listeners.get(name)
      if (!current) return
      current.delete(entry)
      if (current.size === 0) listeners.delete(name)
    }
  }
}

// Browser.OpenURL records the URL instead of opening anything. __failBrowserOpen makes the next calls reject, the way
// the runtime rejects when no browser can be launched.
let openedUrls = []
let browserOpenError = null

export const Browser = {
  OpenURL(url) {
    openedUrls.push(String(url))
    if (browserOpenError) return Promise.reject(browserOpenError)
    return Promise.resolve()
  }
}

export function __openedUrls() {
  return [...openedUrls]
}

export function __failBrowserOpen(error) {
  browserOpenError = error
}

export function __resetBrowser() {
  openedUrls = []
  browserOpenError = null
}

// __emitWailsEvent delivers synchronously to a snapshot of the listeners, so a callback may unsubscribe safely.
export function __emitWailsEvent(name, data) {
  const set = listeners.get(name)
  if (!set) return
  const event = {name, data, sender: ''}
  for (const entry of [...set]) entry.callback(event)
}

export function __resetWailsEvents() {
  listeners.clear()
}

export function __listenerCount(name) {
  return listeners.get(name)?.size ?? 0
}
