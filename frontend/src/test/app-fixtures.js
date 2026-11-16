import {fireEvent, screen} from '@testing-library/svelte'
import {tick} from 'svelte'
import {vi} from 'vitest'
import {__emitWailsEvent} from './wails-runtime-mock.js'

export const failureMessage = 'Wails call failed'

// emitSyncChanged plays the backend's sync:changed event. The app refetches GetAppState on each one.
export function emitSyncChanged() {
  __emitWailsEvent('sync:changed')
}

// settle flushes pending promises and Svelte updates without advancing fake time.
// Each pass drains microtasks (advanceTimersByTimeAsync(0)) and one Svelte tick. Three passes are
// enough for the chains used here: GetAppState resolve -> state assignment -> DOM update.
// Under fake timers, do not use waitFor or userEvent: they rely on timers the fake clock never fires.
// Drive those tests with fireEvent and settle()/advance() instead.
export async function settle() {
  for (let i = 0; i < 3; i++) {
    await vi.advanceTimersByTimeAsync(0)
    await tick()
  }
}

// advance moves fake time forward and flushes whatever the timers started.
export async function advance(ms) {
  await vi.advanceTimersByTimeAsync(ms)
  await settle()
}

export function rejecting() {
  return vi.fn(async () => {
    throw new Error(failureMessage)
  })
}

export function syncRun(overrides = {}) {
  return {
    status: 'success',
    startedAt: '2026-05-22T10:00:00Z',
    finishedAt: '2026-05-22T10:05:00Z',
    gamesSynced: 6,
    gamesTotal: 6,
    gamesFailed: 0,
    ...overrides
  }
}

export function syncingState(gamesSynced) {
  return dashboardState({
    syncInProgress: true,
    latestSyncRun: syncRun({status: 'running', finishedAt: '', gamesSynced})
  })
}

export function finishedState() {
  return dashboardState({latestSyncRun: syncRun()})
}

export function hadesGame(overrides = {}) {
  return game({appid: 1, name: 'Hades', tags: ['almost_there'], completionPercent: 86, unlockedAchievements: 43, totalAchievements: 50, ...overrides})
}

export function hadesCard() {
  return screen.getByRole('heading', {name: 'Hades'}).closest('article')
}

export async function openHadesMenu() {
  const card = (await screen.findByRole('heading', {name: 'Hades'})).closest('article')
  await fireEvent.contextMenu(card)
}

export async function openClearDialog(user) {
  await user.click(await screen.findByRole('button', {name: 'Settings'}))
  await user.click(screen.getByRole('button', {name: 'Clear data'}))
  return screen.getByRole('dialog', {name: 'Clear active user data?'})
}

export function baseState() {
  return {
    appName: 'Steam Achievement Tracker',
    apiKeyPresent: true,
    apiKeyError: '',
    dataDir: '/tmp/steam-achievement-tracker',
    logFile: '/tmp/steam-achievement-tracker/logs/app.log',
    profileExists: false,
    profile: null,
    dashboard: null,
    syncInProgress: false,
    initError: '',
    settings: {includeUnvettedApps: true, includePlayedFreeGames: true}
  }
}

export function missingKeyState() {
  return {...baseState(), apiKeyPresent: false}
}

export function profileSetupState() {
  return {...baseState(), apiKeyPresent: true, profileExists: false}
}

export function dashboardState(overrides = {}) {
  const suggestions = overrides.suggestions ?? [
    hadesGame({missingAvgUnlock: 38, playtimeForever: 620}),
    game({appid: 2, name: 'Vampire Survivors', tags: ['new_achievements_added'], completionPercent: 96, unlockedAchievements: 120, totalAchievements: 125, playtimeForever: 900}),
    game({appid: 3, name: 'Empty Game', tags: ['no_achievements'], totalAchievements: 0, unlockedAchievements: 0, playtimeForever: 0})
  ]
  const warnings = overrides.warnings ?? [
    game({appid: 4, name: 'Broken Game', tags: ['sync_warning'], syncWarning: true, lastError: 'HTTP 503', lastErrorAt: '2026-05-22T10:00:00Z', lastSyncedAt: '2026-05-21T10:00:00Z'})
  ]
  const completed = overrides.completed ?? [
    game({appid: 6, name: 'Portal', tags: ['completed'], completionPercent: 100, unlockedAchievements: 15, totalAchievements: 15, playtimeForever: 120})
  ]
  const disabled = overrides.disabled ?? [
    game({appid: 5, name: 'Hidden Game', disabled: true, totalAchievements: 10, unlockedAchievements: 2, completionPercent: 20, playtimeForever: 70})
  ]

  return {
    ...baseState(),
    profileExists: true,
    syncInProgress: overrides.syncInProgress ?? false,
    profile: {steamId64: '76561198000000000', displayName: 'Test Player', avatarUrl: ''},
    dashboard: {
      summary: {
        ownedGamesCount: 6,
        purchasedGamesCount: 5,
        completedGamesCount: completed.length,
        completedGamesVettedCount: completed.length,
        completedSetsCount: completed.length,
        completedSetsVettedCount: completed.length,
        totalAchievementsUnlocked: 180
      },
      latestSyncRun: overrides.latestSyncRun ?? {
        status: 'partial',
        startedAt: '2026-05-22T10:00:00Z',
        finishedAt: '2026-05-22T10:05:00Z',
        gamesSynced: 5,
        gamesTotal: 6,
        gamesFailed: warnings.length
      },
      suggestions,
      completed,
      warnings,
      disabled
    }
  }
}

export function game(overrides = {}) {
  const appid = overrides.appid ?? 0
  const groupId = overrides.groupId ?? 0
  return {
    appid,
    groupId,
    entryKey: `${appid}:${groupId}`,
    isSet: groupId !== 0,
    setName: '',
    dlcAppId: 0,
    coverKind: '',
    name: 'Game',
    coverUrl: '',
    totalAchievements: 10,
    unlockedAchievements: 0,
    completionPercent: 0,
    missingAvgUnlock: null,
    playtimeForever: 0,
    tags: [],
    syncWarning: false,
    lastError: '',
    lastErrorAt: '',
    lastSyncedAt: '',
    disabled: false,
    manualWasCompleted: false,
    missingAchievementsInDLC: false,
    dlcNotOwned: false,
    unvetted: false,
    isCompleted: false,
    ...overrides
  }
}

// manyGames builds count games named "Batch Game 001"... so grids go past the 48-card first render.
// overrides may be an object (same for every game) or a function (index) => object.
export function manyGames(count, overrides = {}) {
  return Array.from({length: count}, (_, index) => {
    const number = String(index + 1).padStart(3, '0')
    const extra = typeof overrides === 'function' ? overrides(index) : overrides
    return game({appid: 1000 + index, name: `Batch Game ${number}`, ...extra})
  })
}
