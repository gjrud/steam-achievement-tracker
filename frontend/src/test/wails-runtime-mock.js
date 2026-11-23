// Test stand-in for @wailsio/runtime (aliased in vite.config.js, test mode only). It keeps Events.On
// subscriptions in memory so tests can emit events and count listeners.

// RuntimeError mirrors the class of the same name in @wailsio/runtime 3.1 (dist/runtime.js). Wails rejects a bound
// method that returned a Go error with a RuntimeError whose message is the Go error text.
export class RuntimeError extends Error {
  constructor(message, options) {
    super(message, options)
    this.name = 'RuntimeError'
  }
}

// The runtime exports RuntimeError through its Call namespace; format.js reads it from there.
export const Call = {RuntimeError}

const listeners = new Map()

// Event names as the runtime's Events.Types exposes them (the common:* window events the title bar uses).
const Types = {
  Common: {
    WindowMaximise: 'common:WindowMaximise',
    WindowUnMaximise: 'common:WindowUnMaximise',
    WindowRestore: 'common:WindowRestore'
  }
}

export const Events = {
  Types,
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

// Window stands in for the runtime's thisWindow. Maximised state is kept here so IsMaximised reports what the last
// ToggleMaximise (or __setMaximised) set. ToggleMaximise emits the matching common:* event, as Wails does. Each
// window call is recorded so tests can assert which calls ran.
let maximised = false
let windowCalls = []

function record(name) {
  windowCalls.push(name)
  return Promise.resolve()
}

export const Window = {
  Minimise() {
    return record('Minimise')
  },
  Close() {
    return record('Close')
  },
  ToggleMaximise() {
    windowCalls.push('ToggleMaximise')
    maximised = !maximised
    __emitWailsEvent(maximised ? Types.Common.WindowMaximise : Types.Common.WindowUnMaximise)
    return Promise.resolve()
  },
  IsMaximised() {
    return Promise.resolve(maximised)
  }
}

export function __setMaximised(value) {
  maximised = Boolean(value)
}

export function __windowCalls() {
  return [...windowCalls]
}

export function __resetWindow() {
  maximised = false
  windowCalls = []
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
