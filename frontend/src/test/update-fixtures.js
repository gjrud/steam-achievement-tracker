import {vi} from 'vitest'
import {createUpdateStore} from '../lib/update.js'

// Fixtures for the update UI. The snapshot shape mirrors GetUpdateState (app_update.go); the bound calls are mocks, so
// no test reaches GitHub or the updater.

export const releaseURL = 'https://github.com/gjrud/steam-achievement-tracker/releases/tag/v1.2.0'

export function latestRelease(overrides = {}) {
  return {version: '1.2.0', size: 52428800, releaseURL, ...overrides}
}

export function updateSnapshot(overrides = {}) {
  return {
    unavailable: false,
    currentVersion: '1.1.0',
    checksEnabled: true,
    phase: 'idle',
    latest: null,
    progress: {written: 0, total: 0},
    notifyOnly: false,
    notifyReason: '',
    error: '',
    ...overrides
  }
}

export function availableSnapshot(overrides = {}) {
  return updateSnapshot({phase: 'available', latest: latestRelease(), ...overrides})
}

// fakeUpdateApi returns the bound-call mocks plus setSnapshot, which changes what GetUpdateState returns next.
export function fakeUpdateApi(initial = updateSnapshot(), overrides = {}) {
  let current = initial
  const api = {
    getState: vi.fn(async () => current),
    check: vi.fn(async () => {}),
    install: vi.fn(async () => {}),
    cancel: vi.fn(async () => {}),
    restart: vi.fn(async () => {}),
    setChecksEnabled: vi.fn(async (enabled) => ({...current, checksEnabled: enabled})),
    ...overrides
  }
  return {
    api,
    setSnapshot(snapshot) {
      current = snapshot
    }
  }
}

// flush lets pending promises and the store's reads settle. Tests use real timers.
export function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0))
}

// loadedStore builds the real store over fake calls, starts it and waits for the first read.
export async function loadedStore(snapshot = updateSnapshot(), overrides = {}) {
  const fake = fakeUpdateApi(snapshot, overrides)
  const store = createUpdateStore(fake.api)
  const stop = store.start()
  await flush()
  return {store, api: fake.api, setSnapshot: fake.setSnapshot, stop}
}
