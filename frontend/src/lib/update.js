import {writable} from 'svelte/store'
import {Events} from '@wailsio/runtime'
import {formatMessage} from './format.js'

// Wails event the Go backend emits (no payload) on every update snapshot change. The frontend never subscribes to the
// framework's own wails:updater:* events; it re-reads GetUpdateState instead.
export const updateChangedEvent = 'update:changed'

const sizeUnits = ['KB', 'MB', 'GB']

// formatBytes words a byte count for the download line: "512 B", "3.2 MB". Anything unusable is "0 B".
export function formatBytes(value) {
  const n = Number(value)
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  if (n < 1024) return `${Math.floor(n)} B`
  let size = n / 1024
  let unit = 0
  while (size >= 1024 && unit < sizeUnits.length - 1) {
    size /= 1024
    unit++
  }
  let text = roundSize(size)
  // Rounding can reach 1024 ("1024 KB" for 1048524 bytes). Promote to the next unit so the figure reads "1.0 MB".
  if (Number(text) >= 1024 && unit < sizeUnits.length - 1) {
    size /= 1024
    unit++
    text = roundSize(size)
  }
  return `${text} ${sizeUnits[unit]}`
}

function roundSize(size) {
  return size.toFixed(size >= 100 ? 0 : 1)
}

// downloadFraction returns 0..1 for a download with a known total, or null when the total is unknown (indeterminate).
export function downloadFraction(progress) {
  const total = Number(progress?.total) || 0
  if (total <= 0) return null
  const written = Math.max(0, Number(progress?.written) || 0)
  return Math.min(1, written / total)
}

// Phases in which an update operation runs or has finished its work: "Check now" stays disabled in all of them.
const lockedPhases = new Set(['checking', 'downloading', 'verifying', 'installing', 'ready', 'restarting'])

export function checkLocked(phase) {
  return lockedPhases.has(phase)
}

function normalize(snapshot) {
  const snap = snapshot || {}
  return {
    unavailable: Boolean(snap.unavailable),
    currentVersion: snap.currentVersion || '',
    checksEnabled: Boolean(snap.checksEnabled),
    phase: snap.phase || 'idle',
    latest: snap.latest || null,
    progress: {written: Number(snap.progress?.written) || 0, total: Number(snap.progress?.total) || 0},
    notifyOnly: Boolean(snap.notifyOnly),
    notifyReason: snap.notifyReason || '',
    error: snap.error || ''
  }
}

const initialState = {
  // loaded: a snapshot has been read at least once. Until then no update UI shows.
  loaded: false,
  snapshot: normalize(null),
  // dismissedVersion: the version the user postponed with "Later". Memory only; a manual check or another version
  // clears it.
  dismissedVersion: '',
  // acting: the name of the bound call in flight ('check', 'install' or 'restart'), or ''. Install stays set for the
  // whole download, so Cancel never looks at it.
  acting: '',
  // savingChecks: the startup-check toggle is being saved. Kept apart from acting so a save and a check do not clear
  // each other's marker.
  savingChecks: false,
  // lastOp: the last operation the user started ('check' or 'install'). Any error phase belongs to it, which picks the
  // retry and the title. It is kept, not cleared, so the title does not flip while a retry runs.
  lastOp: '',
  // canceling: Cancel was clicked and the download has not ended yet.
  canceling: false,
  // actionError: the plain sentence of a rejected call that the snapshot does not carry (refusals).
  actionError: '',
  // notice: a plain note, for example that a newer release replaced the one the user clicked Install on.
  notice: '',
  // checkHeld: a check began while an available release or an error was on screen, so the checking phase keeps the
  // banner. A check that began from idle (a startup check) holds nothing.
  checkHeld: false,
  // settingsError: a failed change of the startup-check toggle.
  settingsError: ''
}

// createUpdateStore builds the update state shared by the banner and the Settings section. `api` holds the bound calls
// (getState, check, install, cancel, restart, setChecksEnabled), so this module needs no bindings import. App creates
// one store and calls start() on mount.
export function createUpdateStore(api) {
  let state = initialState
  const store = writable(state)
  let readSeq = 0
  let appliedSeq = 0
  let unsubscribe = null

  function commit(patch) {
    state = {...state, ...patch}
    store.set(state)
  }

  // apply stores a fresh snapshot. A phase change clears the notes that belonged to the previous phase; a check keeps the
  // banner it began over; a download that ended clears the canceling flag; a different version un-dismisses the banner.
  function apply(snapshot) {
    const snap = normalize(snapshot)
    const prev = state.snapshot
    const patch = {loaded: true, snapshot: snap}
    if (snap.phase !== prev.phase) {
      patch.actionError = ''
      patch.notice = ''
      patch.checkHeld = snap.phase === 'checking' && (prev.phase === 'available' || prev.phase === 'error') && bannerVisible(state)
    }
    if (snap.phase !== 'downloading') patch.canceling = false
    if (state.dismissedVersion && state.dismissedVersion !== snap.latest?.version) patch.dismissedVersion = ''
    commit(patch)
  }

  // applyRead applies a snapshot read as number seq, unless a read started later was already applied. Every snapshot
  // source (reads and the toggle's result) goes through here, so an older one cannot overwrite a newer one.
  function applyRead(seq, snapshot) {
    if (seq < appliedSeq) return
    appliedSeq = seq
    apply(snapshot)
  }

  // refresh reads the snapshot. Overlapping reads are allowed; a result older than one already applied is dropped, and a
  // failed read keeps the last snapshot (the next update:changed reads again).
  async function refresh() {
    const seq = ++readSeq
    try {
      applyRead(seq, await api.getState())
    } catch {
      // Keep the last snapshot.
    }
  }

  // start subscribes before the first read, so an update:changed emitted during that read is not missed. It returns the
  // function that stops the subscription.
  function start() {
    if (!unsubscribe) unsubscribe = Events.On(updateChangedEvent, () => refresh())
    refresh()
    return stop
  }

  function stop() {
    unsubscribe?.()
    unsubscribe = null
  }

  // beginAction marks a call in flight and records it as lastOp when it is a check or an install.
  function beginAction(name, extra = {}) {
    const patch = {acting: name, actionError: '', notice: '', ...extra}
    if (name !== 'restart') patch.lastOp = name
    commit(patch)
  }

  // check runs a manual check. Whatever its outcome, it clears the "Later" dismissal, so a found update shows again.
  async function check() {
    if (state.acting === 'check') return
    beginAction('check')
    try {
      await api.check()
    } catch (err) {
      commit({actionError: formatMessage(err)})
    } finally {
      commit({acting: '', dismissedVersion: ''})
      await refresh()
    }
  }

  // install passes the version the user saw. The call returns only when the download ends.
  async function install(version) {
    if (state.acting === 'install') return
    beginAction('install', {canceling: false})
    try {
      await api.install(version)
    } catch (err) {
      commit({actionError: formatMessage(err)})
    } finally {
      commit({acting: ''})
      await refresh()
    }
    const snap = state.snapshot
    if (snap.phase === 'available' && snap.latest && snap.latest.version !== version && !state.actionError) {
      commit({notice: `Version ${snap.latest.version} is available now. Review it and install again.`})
    }
  }

  async function cancel() {
    if (state.canceling) return
    commit({canceling: true, actionError: ''})
    try {
      await api.cancel()
    } catch (err) {
      commit({canceling: false, actionError: formatMessage(err)})
    }
    await refresh()
  }

  async function restart() {
    if (state.acting === 'restart') return
    beginAction('restart')
    try {
      await api.restart()
    } catch (err) {
      commit({actionError: formatMessage(err)})
    } finally {
      commit({acting: ''})
      await refresh()
    }
  }

  // later hides the banner for the version it shows, until the next launch, a manual check or another version.
  function later() {
    const snap = state.snapshot
    if (snap.phase !== 'available' || !snap.latest) return
    commit({dismissedVersion: snap.latest.version})
  }

  // setChecksEnabled stores the startup-check toggle and shows the snapshot the call returns, through the same guard as
  // the reads.
  async function setChecksEnabled(enabled) {
    if (state.savingChecks) return
    commit({savingChecks: true, settingsError: ''})
    const seq = ++readSeq
    try {
      applyRead(seq, await api.setChecksEnabled(Boolean(enabled)))
    } catch (err) {
      commit({settingsError: formatMessage(err)})
    } finally {
      commit({savingChecks: false})
    }
  }

  return {subscribe: store.subscribe, start, stop, refresh, check, install, cancel, restart, later, setChecksEnabled}
}

// updateError returns the plain sentence to show for the update, or ''. The snapshot's own error wins.
export function updateError(view) {
  return view.snapshot.error || view.actionError || ''
}

// retryKind picks the retry for an error. The last operation the user started owns the error: a failed check retries the
// check, a failed install retries the install of the version shown (without a release to install, a check). Only when
// this session started no operation (after a reload) does the snapshot decide: a known release means a failed install.
export function retryKind(view) {
  const version = view.snapshot.latest?.version
  if (view.lastOp === 'check') return 'check'
  if (view.lastOp === 'install') return version ? 'install' : 'check'
  return version && !view.snapshot.notifyOnly ? 'install' : 'check'
}

// bannerVisible reports whether the dashboard banner shows. Idle and up-to-date show nothing, and so does a checking
// phase that began from idle, so a quiet startup check never opens a banner.
export function bannerVisible(view) {
  if (!view?.loaded || view.snapshot.unavailable) return false
  switch (view.snapshot.phase) {
    case 'available':
      return Boolean(view.snapshot.latest) && view.dismissedVersion !== view.snapshot.latest.version
    case 'checking':
      return Boolean(view.checkHeld)
    case 'downloading':
    case 'verifying':
    case 'installing':
    case 'ready':
    case 'restarting':
    case 'error':
      return true
    default:
      return false
  }
}

// readyLine words the ready state, for the banner title and the Settings status line.
export function readyLine(version) {
  return version ? `Version ${version} is ready. Restart to finish updating.` : 'Update ready. Restart to finish updating.'
}

// settingsStatus words the Settings section's status line from the snapshot.
export function settingsStatus(view) {
  const snap = view.snapshot
  const version = snap.latest?.version
  switch (snap.phase) {
    case 'checking':
      return 'Checking for updates…'
    case 'up-to-date':
      return 'You have the latest version.'
    case 'available': {
      if (!version) return ''
      const dismissed = view.dismissedVersion === version ? ' Press Check now to show it again.' : ''
      return `Version ${version} is available.${dismissed}`
    }
    case 'downloading':
      return 'Downloading update…'
    case 'verifying':
      return 'Verifying download…'
    case 'installing':
      return 'Installing update…'
    case 'ready':
      return readyLine(version)
    case 'restarting':
      return 'Restarting…'
    default:
      return ''
  }
}
