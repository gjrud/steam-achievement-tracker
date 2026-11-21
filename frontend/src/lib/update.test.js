import {get} from 'svelte/store'
import {afterEach, describe, expect, test, vi} from 'vitest'
import {
  bannerVisible,
  checkLocked,
  createUpdateStore,
  downloadFraction,
  formatBytes,
  readyLine,
  retryKind,
  settingsStatus,
  updateError
} from './update.js'
import {Events, RuntimeError, __emitWailsEvent, __listenerCount} from '../test/wails-runtime-mock.js'
import {genericMessage} from './format.js'
import {availableSnapshot, fakeUpdateApi, flush, latestRelease, loadedStore, updateSnapshot} from '../test/update-fixtures.js'

afterEach(() => {
  vi.restoreAllMocks()
})

// deferred returns a promise the test settles by hand.
function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return {promise, resolve, reject}
}

describe('store reads', () => {
  test('loads the snapshot on start and marks it loaded', async () => {
    const {store, api} = await loadedStore(availableSnapshot())

    expect(api.getState).toHaveBeenCalledOnce()
    const view = get(store)
    expect(view.loaded).toBe(true)
    expect(view.snapshot.phase).toBe('available')
    expect(view.snapshot.latest.version).toBe('1.2.0')
  })

  test('is not loaded until a read succeeds', () => {
    const store = createUpdateStore(fakeUpdateApi().api)

    expect(get(store).loaded).toBe(false)
  })

  test('normalizes a sparse snapshot', async () => {
    const {store} = await loadedStore({phase: 'downloading'})

    const snap = get(store).snapshot
    expect(snap.phase).toBe('downloading')
    expect(snap.unavailable).toBe(false)
    expect(snap.latest).toBeNull()
    expect(snap.progress).toEqual({written: 0, total: 0})
    expect(snap.error).toBe('')
  })

  test('subscribes only to update:changed', async () => {
    const on = vi.spyOn(Events, 'On')
    const {stop} = await loadedStore()

    expect(on).toHaveBeenCalledTimes(1)
    expect(on.mock.calls.map(([name]) => name)).toEqual(['update:changed'])
    expect(__listenerCount('update:changed')).toBe(1)
    stop()
  })

  test('start twice keeps one subscription', async () => {
    const fake = fakeUpdateApi()
    const store = createUpdateStore(fake.api)
    store.start()
    store.start()
    await flush()

    expect(__listenerCount('update:changed')).toBe(1)
  })

  test('starting never downloads, checks or restarts on its own', async () => {
    const {api} = await loadedStore(availableSnapshot())

    expect(api.check).not.toHaveBeenCalled()
    expect(api.install).not.toHaveBeenCalled()
    expect(api.restart).not.toHaveBeenCalled()
  })

  test('stop removes the subscription, so later events read nothing', async () => {
    const {api, stop} = await loadedStore()
    api.getState.mockClear()

    stop()
    __emitWailsEvent('update:changed')
    await flush()

    expect(__listenerCount('update:changed')).toBe(0)
    expect(api.getState).not.toHaveBeenCalled()
  })

  test('refetches the snapshot on update:changed', async () => {
    const {store, api, setSnapshot} = await loadedStore(updateSnapshot())
    setSnapshot(availableSnapshot())

    __emitWailsEvent('update:changed')
    await flush()

    expect(api.getState).toHaveBeenCalledTimes(2)
    expect(get(store).snapshot.phase).toBe('available')
  })

  test('a read started before a toggle cannot overwrite the toggle result', async () => {
    const reads = []
    const api = {
      getState: vi.fn(() => new Promise((resolve) => reads.push(resolve))),
      check: vi.fn(),
      install: vi.fn(),
      cancel: vi.fn(),
      restart: vi.fn(),
      setChecksEnabled: vi.fn(async (enabled) => updateSnapshot({checksEnabled: enabled}))
    }
    const store = createUpdateStore(api)
    store.start()

    await store.setChecksEnabled(false)
    reads[0](updateSnapshot({checksEnabled: true}))
    await flush()

    expect(get(store).snapshot.checksEnabled).toBe(false)
  })

  test('drops a read that finishes after a newer one was applied', async () => {
    const reads = []
    const api = {
      getState: vi.fn(() => new Promise((resolve) => reads.push(resolve))),
      check: vi.fn(),
      install: vi.fn(),
      cancel: vi.fn(),
      restart: vi.fn(),
      setChecksEnabled: vi.fn()
    }
    const store = createUpdateStore(api)
    store.start()
    __emitWailsEvent('update:changed')

    reads[1](availableSnapshot())
    await flush()
    reads[0](updateSnapshot({phase: 'idle'}))
    await flush()

    expect(get(store).snapshot.phase).toBe('available')
  })

  test('a failed read keeps the last snapshot', async () => {
    const {store, api} = await loadedStore(availableSnapshot())
    api.getState.mockRejectedValueOnce(new Error('network down'))

    __emitWailsEvent('update:changed')
    await flush()

    expect(get(store).loaded).toBe(true)
    expect(get(store).snapshot.phase).toBe('available')
  })

  test('a later read after a failed one still applies', async () => {
    const {store, api, setSnapshot} = await loadedStore(availableSnapshot())
    api.getState.mockRejectedValueOnce(new Error('network down'))
    __emitWailsEvent('update:changed')
    await flush()

    setSnapshot(updateSnapshot({phase: 'up-to-date'}))
    __emitWailsEvent('update:changed')
    await flush()

    expect(get(store).snapshot.phase).toBe('up-to-date')
  })
})

describe('later', () => {
  test('dismisses only the version on screen', async () => {
    const {store} = await loadedStore(availableSnapshot())

    store.later()

    expect(get(store).dismissedVersion).toBe('1.2.0')
    expect(bannerVisible(get(store))).toBe(false)
  })

  test('is ignored outside the available phase', async () => {
    const {store} = await loadedStore(updateSnapshot({phase: 'downloading', latest: latestRelease()}))

    store.later()

    expect(get(store).dismissedVersion).toBe('')
  })

  test('survives a refresh of the same version', async () => {
    const {store, api} = await loadedStore(availableSnapshot())
    store.later()

    __emitWailsEvent('update:changed')
    await flush()

    expect(api.getState).toHaveBeenCalledTimes(2)
    expect(get(store).dismissedVersion).toBe('1.2.0')
    expect(bannerVisible(get(store))).toBe(false)
  })

  test('a newer version shows again and clears the dismissal', async () => {
    const {store, setSnapshot} = await loadedStore(availableSnapshot())
    store.later()

    setSnapshot(availableSnapshot({latest: latestRelease({version: '1.3.0'})}))
    __emitWailsEvent('update:changed')
    await flush()

    expect(get(store).dismissedVersion).toBe('')
    expect(bannerVisible(get(store))).toBe(true)
  })

  test('is not persisted: a fresh store on the same release shows the banner again', async () => {
    const {store} = await loadedStore(availableSnapshot())
    store.later()

    const relaunched = await loadedStore(availableSnapshot())

    expect(get(relaunched.store).dismissedVersion).toBe('')
    expect(bannerVisible(get(relaunched.store))).toBe(true)
  })

  test('a manual check clears the dismissal once it ends, and the banner returns then', async () => {
    const {store, api} = await loadedStore(availableSnapshot())
    store.later()
    const call = deferred()
    api.check.mockImplementationOnce(() => call.promise)

    const pending = store.check()
    expect(get(store).acting).toBe('check')
    expect(get(store).dismissedVersion).toBe('1.2.0')
    expect(bannerVisible(get(store))).toBe(false)

    call.resolve()
    await pending

    expect(get(store).dismissedVersion).toBe('')
    expect(bannerVisible(get(store))).toBe(true)
  })
})

describe('install', () => {
  test('passes the version the user saw', async () => {
    const {store, api} = await loadedStore(availableSnapshot())

    await store.install('1.2.0')

    expect(api.install).toHaveBeenCalledOnce()
    expect(api.install).toHaveBeenCalledWith('1.2.0')
  })

  test('shows a notice when a newer release replaced the one clicked', async () => {
    const {store, api, setSnapshot} = await loadedStore(availableSnapshot())
    api.install.mockImplementationOnce(async () => {
      setSnapshot(availableSnapshot({latest: latestRelease({version: '1.3.0'})}))
    })

    await store.install('1.2.0')

    expect(get(store).notice).toBe('Version 1.3.0 is available now. Review it and install again.')
    expect(get(store).actionError).toBe('')
    expect(get(store).snapshot.latest.version).toBe('1.3.0')
  })

  test('no notice when the install ends on the same version', async () => {
    const {store} = await loadedStore(availableSnapshot())

    await store.install('1.2.0')

    expect(get(store).notice).toBe('')
  })

  test('a rejected install shows its sentence and records install as the last operation', async () => {
    const {store, api} = await loadedStore(availableSnapshot())
    api.install.mockRejectedValueOnce(new RuntimeError('Install is not available for this release.'))

    await store.install('1.2.0')

    expect(get(store).actionError).toBe('Install is not available for this release.')
    expect(get(store).lastOp).toBe('install')
    expect(updateError(get(store))).toBe('Install is not available for this release.')
  })

  test('a rejection that is not a backend sentence shows the generic message', async () => {
    const {store, api} = await loadedStore(availableSnapshot())
    api.install.mockRejectedValueOnce(new TypeError('x is undefined'))

    await store.install('1.2.0')

    expect(get(store).actionError).toBe(genericMessage)
  })

  test('a second install while one runs is ignored', async () => {
    const {store, api} = await loadedStore(availableSnapshot())
    const call = deferred()
    api.install.mockImplementationOnce(() => call.promise)

    const first = store.install('1.2.0')
    expect(get(store).acting).toBe('install')
    await store.install('1.2.0')
    expect(api.install).toHaveBeenCalledOnce()

    call.resolve()
    await first
    expect(get(store).acting).toBe('')
  })

  test('canceling is cleared when the download ends', async () => {
    const {store, api, setSnapshot} = await loadedStore(availableSnapshot())
    const call = deferred()
    api.install.mockImplementationOnce(() => call.promise)
    setSnapshot(updateSnapshot({phase: 'downloading', latest: latestRelease()}))
    const pending = store.install('1.2.0')
    await flush()
    await store.cancel()
    expect(get(store).canceling).toBe(true)

    setSnapshot(updateSnapshot({phase: 'available', latest: latestRelease()}))
    call.resolve()
    await pending

    expect(get(store).canceling).toBe(false)
  })
})

describe('check', () => {
  test('a second check while one runs is ignored', async () => {
    const {store, api} = await loadedStore()
    const call = deferred()
    api.check.mockImplementationOnce(() => call.promise)

    const first = store.check()
    expect(get(store).acting).toBe('check')
    await store.check()
    expect(api.check).toHaveBeenCalledOnce()

    call.resolve()
    await first
    expect(get(store).acting).toBe('')
  })

  test('a rejected check shows its sentence and records check as the last operation', async () => {
    const {store, api} = await loadedStore()
    api.check.mockRejectedValueOnce(new RuntimeError('Could not check for updates. Try again later.'))

    await store.check()

    expect(get(store).lastOp).toBe('check')
    expect(get(store).actionError).toBe('Could not check for updates. Try again later.')
  })

  test('a new action clears the previous rejection', async () => {
    const {store, api} = await loadedStore()
    api.check.mockRejectedValueOnce(new RuntimeError('Could not check for updates. Try again later.'))
    await store.check()

    await store.check()

    expect(get(store).actionError).toBe('')
  })

  test('lastOp is set when a call starts, so a retry in flight keeps the operation it retries', async () => {
    const {store, api} = await loadedStore()
    api.check.mockRejectedValueOnce(new RuntimeError('Could not check for updates. Try again later.'))
    await store.check()
    const call = deferred()
    api.check.mockImplementationOnce(() => call.promise)

    const pending = store.check()
    expect(get(store).acting).toBe('check')
    expect(get(store).lastOp).toBe('check')

    call.resolve()
    await pending
    expect(get(store).lastOp).toBe('check')
  })

  test('a restart does not replace the recorded operation', async () => {
    const {store, api} = await loadedStore(availableSnapshot())
    await store.install('1.2.0')
    api.restart.mockRejectedValueOnce(new RuntimeError('Could not restart. The update stays ready.'))

    await store.restart()

    expect(get(store).lastOp).toBe('install')
    expect(get(store).actionError).toBe('Could not restart. The update stays ready.')
  })

  test('an error applied before a rejected install arrives still belongs to the install', async () => {
    const {store, api, setSnapshot} = await loadedStore(availableSnapshot())
    const call = deferred()
    api.install.mockImplementationOnce(() => call.promise)
    const pending = store.install('1.2.0')

    // The backend reports the failed download before the bound call returns its rejection.
    setSnapshot(updateSnapshot({phase: 'error', latest: latestRelease(), error: 'Could not download the update.'}))
    __emitWailsEvent('update:changed')
    await vi.waitFor(() => expect(get(store).snapshot.phase).toBe('error'))
    expect(retryKind(get(store))).toBe('install')

    call.reject(new RuntimeError('Could not download the update.'))
    await pending
    expect(retryKind(get(store))).toBe('install')
    expect(get(store).lastOp).toBe('install')
  })

  test('an error applied before a rejected check arrives retries the check, even with a release on record', async () => {
    const {store, api, setSnapshot} = await loadedStore(availableSnapshot())
    const call = deferred()
    api.check.mockImplementationOnce(() => call.promise)
    const pending = store.check()

    setSnapshot(updateSnapshot({phase: 'error', latest: latestRelease(), error: 'Could not check for updates.'}))
    __emitWailsEvent('update:changed')
    await vi.waitFor(() => expect(get(store).snapshot.phase).toBe('error'))
    expect(retryKind(get(store))).toBe('check')

    call.reject(new RuntimeError('Could not check for updates.'))
    await pending
    expect(retryKind(get(store))).toBe('check')
  })
})

describe('check from a visible banner', () => {
  test('a check that began while an available release was shown keeps the banner', async () => {
    const {store, setSnapshot} = await loadedStore(availableSnapshot())

    setSnapshot(updateSnapshot({phase: 'checking'}))
    __emitWailsEvent('update:changed')
    await flush()

    expect(get(store).checkHeld).toBe(true)
    expect(bannerVisible(get(store))).toBe(true)

    setSnapshot(updateSnapshot({phase: 'up-to-date'}))
    __emitWailsEvent('update:changed')
    await flush()

    expect(get(store).checkHeld).toBe(false)
    expect(bannerVisible(get(store))).toBe(false)
  })

  test('a check that began from an error keeps the banner too', async () => {
    const {store, setSnapshot} = await loadedStore(updateSnapshot({phase: 'error', error: 'Could not check for updates.'}))

    setSnapshot(updateSnapshot({phase: 'checking'}))
    __emitWailsEvent('update:changed')
    await flush()

    expect(bannerVisible(get(store))).toBe(true)
  })

  test('a startup check from idle holds nothing', async () => {
    const {store} = await loadedStore(updateSnapshot({phase: 'checking'}))

    expect(get(store).checkHeld).toBe(false)
    expect(bannerVisible(get(store))).toBe(false)
  })

  test('a check of a dismissed release holds nothing', async () => {
    const {store, setSnapshot} = await loadedStore(availableSnapshot())
    store.later()

    setSnapshot(updateSnapshot({phase: 'checking'}))
    __emitWailsEvent('update:changed')
    await flush()

    expect(bannerVisible(get(store))).toBe(false)
  })
})

describe('cancel, restart and in-flight rules', () => {
  test('cancel is sent once and marks the download as canceling until it ends', async () => {
    const {store, api, setSnapshot} = await loadedStore(updateSnapshot({phase: 'downloading', latest: latestRelease()}))
    const call = deferred()
    api.cancel.mockImplementationOnce(() => call.promise)

    const first = store.cancel()
    await store.cancel()
    expect(api.cancel).toHaveBeenCalledOnce()
    expect(get(store).canceling).toBe(true)

    call.resolve()
    await first
    expect(get(store).canceling).toBe(true)

    setSnapshot(updateSnapshot({phase: 'available', latest: latestRelease()}))
    __emitWailsEvent('update:changed')
    await flush()
    expect(get(store).canceling).toBe(false)
  })

  test('a rejected cancel clears the canceling flag and shows its sentence', async () => {
    const {store, api} = await loadedStore(updateSnapshot({phase: 'downloading', latest: latestRelease()}))
    api.cancel.mockRejectedValueOnce(new RuntimeError('Nothing to cancel.'))

    await store.cancel()

    expect(get(store).canceling).toBe(false)
    expect(get(store).actionError).toBe('Nothing to cancel.')
  })

  test('restart is sent once and a success leaves the restarting phase on screen', async () => {
    const {store, api, setSnapshot} = await loadedStore(updateSnapshot({phase: 'ready', latest: latestRelease()}))
    const call = deferred()
    api.restart.mockImplementationOnce(() => call.promise)
    const first = store.restart()
    await store.restart()
    expect(api.restart).toHaveBeenCalledOnce()
    expect(get(store).acting).toBe('restart')

    setSnapshot(updateSnapshot({phase: 'restarting', latest: latestRelease()}))
    call.resolve()
    await first

    expect(get(store).acting).toBe('')
    expect(get(store).snapshot.phase).toBe('restarting')
  })

  test('a rejected restart shows its sentence and keeps the staged update', async () => {
    const {store, api} = await loadedStore(updateSnapshot({phase: 'ready', latest: latestRelease()}))
    api.restart.mockRejectedValueOnce(new RuntimeError('Could not restart. The update stays ready.'))

    await store.restart()

    expect(get(store).actionError).toBe('Could not restart. The update stays ready.')
    expect(get(store).acting).toBe('')
    expect(get(store).snapshot.phase).toBe('ready')
  })
})

describe('setChecksEnabled', () => {
  test('stores the toggle and shows the snapshot the call returns', async () => {
    const {store, api} = await loadedStore(updateSnapshot({checksEnabled: true}))

    await store.setChecksEnabled(false)

    expect(api.setChecksEnabled).toHaveBeenCalledWith(false)
    expect(get(store).snapshot.checksEnabled).toBe(false)
    expect(get(store).savingChecks).toBe(false)
  })

  test('is marked saving while the call runs and ignores a second change', async () => {
    const {store, api} = await loadedStore(updateSnapshot({checksEnabled: true}))
    const call = deferred()
    api.setChecksEnabled.mockImplementationOnce(() => call.promise)

    const first = store.setChecksEnabled(false)
    expect(get(store).savingChecks).toBe(true)
    expect(get(store).acting).toBe('')
    await store.setChecksEnabled(true)
    expect(api.setChecksEnabled).toHaveBeenCalledOnce()

    call.resolve(updateSnapshot({checksEnabled: false}))
    await first
    expect(get(store).savingChecks).toBe(false)
  })

  test('a save and a check keep their own in-flight markers', async () => {
    const {store, api} = await loadedStore(updateSnapshot({checksEnabled: true}))
    const save = deferred()
    const check = deferred()
    api.setChecksEnabled.mockImplementationOnce(() => save.promise)
    api.check.mockImplementationOnce(() => check.promise)

    const saving = store.setChecksEnabled(false)
    const checking = store.check()
    expect(get(store).savingChecks).toBe(true)
    expect(get(store).acting).toBe('check')

    // The check ends first: the save is still in flight, so a second change is still refused.
    check.resolve()
    await checking
    expect(get(store).acting).toBe('')
    expect(get(store).savingChecks).toBe(true)
    await store.setChecksEnabled(true)
    expect(api.setChecksEnabled).toHaveBeenCalledOnce()

    save.resolve(updateSnapshot({checksEnabled: false}))
    await saving
    expect(get(store).savingChecks).toBe(false)
  })

  test('a check started during a save is not refused', async () => {
    const {store, api} = await loadedStore(updateSnapshot({checksEnabled: true}))
    const save = deferred()
    api.setChecksEnabled.mockImplementationOnce(() => save.promise)

    const saving = store.setChecksEnabled(false)
    await store.check()

    expect(api.check).toHaveBeenCalledOnce()
    save.resolve(updateSnapshot({checksEnabled: false}))
    await saving
  })

  test('a rejected change keeps the old value and shows the sentence', async () => {
    const {store, api} = await loadedStore(updateSnapshot({checksEnabled: true}))
    api.setChecksEnabled.mockRejectedValueOnce(new RuntimeError('Could not save the setting.'))

    await store.setChecksEnabled(false)

    expect(get(store).snapshot.checksEnabled).toBe(true)
    expect(get(store).settingsError).toBe('Could not save the setting.')
    expect(get(store).savingChecks).toBe(false)
  })
})

describe('pure helpers', () => {
  test('formatBytes words byte counts and treats unusable values as zero', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(-5)).toBe('0 B')
    expect(formatBytes(undefined)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(3355443)).toBe('3.2 MB')
    expect(formatBytes(104857600)).toBe('100 MB')
  })

  test('formatBytes promotes to the next unit instead of rounding up to 1024', () => {
    // 1023.95 KB would round to "1024 KB"; it reads as 1.0 MB.
    expect(formatBytes(1048524)).toBe('1.0 MB')
    expect(formatBytes(1048575)).toBe('1.0 MB')
    expect(formatBytes(1023 * 1024)).toBe('1023 KB')
    expect(formatBytes(1073741824 - 1024)).toBe('1.0 GB')
  })

  test('downloadFraction is null for an unknown total and clamps to 1', () => {
    expect(downloadFraction({written: 50, total: 200})).toBe(0.25)
    expect(downloadFraction({written: 0, total: 0})).toBeNull()
    expect(downloadFraction(undefined)).toBeNull()
    expect(downloadFraction({written: 300, total: 200})).toBe(1)
  })

  test('checkLocked covers every phase that runs or finishes an operation', () => {
    for (const phase of ['checking', 'downloading', 'verifying', 'installing', 'ready', 'restarting']) {
      expect(checkLocked(phase), phase).toBe(true)
    }
    for (const phase of ['idle', 'up-to-date', 'available', 'error']) {
      expect(checkLocked(phase), phase).toBe(false)
    }
  })

  test('retryKind follows the last operation the user started', () => {
    const view = (extra = {}) => ({snapshot: updateSnapshot({latest: latestRelease()}), lastOp: '', ...extra})

    expect(retryKind(view({lastOp: 'check'}))).toBe('check')
    expect(retryKind(view({lastOp: 'install'}))).toBe('install')
    // A failed check keeps the check retry even when a release is on record from an earlier check.
    expect(retryKind(view({lastOp: 'check', snapshot: updateSnapshot({latest: latestRelease(), phase: 'error'})}))).toBe('check')
  })

  test('retryKind falls back to the snapshot when this session started no operation', () => {
    const view = (extra = {}) => ({snapshot: updateSnapshot(), lastOp: '', ...extra})

    expect(retryKind(view({snapshot: updateSnapshot({latest: latestRelease()})}))).toBe('install')
    expect(retryKind(view({snapshot: updateSnapshot({latest: latestRelease(), notifyOnly: true})}))).toBe('check')
    expect(retryKind(view())).toBe('check')
  })

  test('retryKind turns a failed install with no release into a check', () => {
    const view = (extra = {}) => ({snapshot: updateSnapshot(), lastOp: 'install', ...extra})

    expect(retryKind(view())).toBe('check')
    // A release without a version has nothing to install either.
    expect(retryKind(view({snapshot: updateSnapshot({latest: latestRelease({version: ''})})}))).toBe('check')
  })

  test('updateError prefers the snapshot error over the action error', () => {
    expect(updateError({snapshot: updateSnapshot({error: 'From Go.'}), actionError: 'From click.'})).toBe('From Go.')
    expect(updateError({snapshot: updateSnapshot(), actionError: 'From click.'})).toBe('From click.')
    expect(updateError({snapshot: updateSnapshot(), actionError: ''})).toBe('')
  })

  test('bannerVisible is false for unloaded, unavailable, idle, checking and up-to-date', () => {
    const view = (snapshot, extra = {}) => ({loaded: true, snapshot, dismissedVersion: '', ...extra})

    expect(bannerVisible(null)).toBe(false)
    expect(bannerVisible({loaded: false, snapshot: updateSnapshot({phase: 'available', latest: latestRelease()})})).toBe(false)
    expect(bannerVisible(view(updateSnapshot({unavailable: true, phase: 'available', latest: latestRelease()})))).toBe(false)
    for (const phase of ['idle', 'checking', 'up-to-date']) {
      expect(bannerVisible(view(updateSnapshot({phase})))).toBe(false)
    }
    expect(bannerVisible(view(updateSnapshot({phase: 'available', latest: latestRelease()})))).toBe(true)
    expect(bannerVisible(view(updateSnapshot({phase: 'available', latest: latestRelease()}), {dismissedVersion: '1.2.0'}))).toBe(false)
    for (const phase of ['downloading', 'verifying', 'installing', 'ready', 'restarting', 'error']) {
      expect(bannerVisible(view(updateSnapshot({phase})))).toBe(true)
    }
  })

  test('bannerVisible shows checking only when the check held the banner', () => {
    const view = (extra = {}) => ({loaded: true, snapshot: updateSnapshot({phase: 'checking'}), dismissedVersion: '', ...extra})

    expect(bannerVisible(view())).toBe(false)
    expect(bannerVisible(view({checkHeld: false}))).toBe(false)
    expect(bannerVisible(view({checkHeld: true}))).toBe(true)
  })

  test('readyLine names the version and falls back without one', () => {
    expect(readyLine('1.2.0')).toBe('Version 1.2.0 is ready. Restart to finish updating.')
    expect(readyLine('')).toBe('Update ready. Restart to finish updating.')
  })

  test('settingsStatus words each phase and notes a dismissed release', () => {
    const view = (snapshot, extra = {}) => ({snapshot, dismissedVersion: '', ...extra})

    expect(settingsStatus(view(updateSnapshot({phase: 'checking'})))).toBe('Checking for updates…')
    expect(settingsStatus(view(updateSnapshot({phase: 'up-to-date'})))).toBe('You have the latest version.')
    expect(settingsStatus(view(updateSnapshot({phase: 'available', latest: latestRelease()})))).toBe('Version 1.2.0 is available.')
    expect(settingsStatus(view(updateSnapshot({phase: 'available', latest: latestRelease()}), {dismissedVersion: '1.2.0'}))).toBe(
      'Version 1.2.0 is available. Press Check now to show it again.'
    )
    expect(settingsStatus(view(updateSnapshot({phase: 'downloading'})))).toBe('Downloading update…')
    expect(settingsStatus(view(updateSnapshot({phase: 'verifying'})))).toBe('Verifying download…')
    expect(settingsStatus(view(updateSnapshot({phase: 'installing'})))).toBe('Installing update…')
    expect(settingsStatus(view(updateSnapshot({phase: 'ready', latest: latestRelease()})))).toBe(
      'Version 1.2.0 is ready. Restart to finish updating.'
    )
    expect(settingsStatus(view(updateSnapshot({phase: 'ready'})))).toBe('Update ready. Restart to finish updating.')
    expect(settingsStatus(view(updateSnapshot({phase: 'restarting'})))).toBe('Restarting…')
    expect(settingsStatus(view(updateSnapshot({phase: 'idle'})))).toBe('')
  })
})
