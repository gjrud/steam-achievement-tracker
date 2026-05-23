import {cleanup, fireEvent, render, screen, waitFor, within} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {__resetWailsMocks, __setWailsMocks} from './test/wails-app-mock.js'

afterEach(() => {
  cleanup()
  __resetWailsMocks()
  vi.restoreAllMocks()
})

describe('App setup states', () => {
  test('shows missing API key setup and retries key lookup', async () => {
    const retry = vi.fn(async () => profileSetupState())
    __setWailsMocks({
      GetAppState: vi.fn(async () => missingKeyState()),
      RetryAPIKey: retry
    })

    render(App)

    expect(await screen.findByRole('heading', {name: 'Steam Web API key missing'})).toBeInTheDocument()
    expect(screen.getByText(/secret-tool store/)).toBeInTheDocument()

    await fireEvent.click(screen.getByRole('button', {name: 'Retry key lookup'}))

    expect(retry).toHaveBeenCalledOnce()
    expect(await screen.findByRole('heading', {name: 'Connect your profile'})).toBeInTheDocument()
  })

  test('validates and saves a Steam profile', async () => {
    const preview = {steamId64: '76561198000000000', displayName: 'Test Player', avatarUrl: ''}
    const validate = vi.fn(async () => preview)
    const save = vi.fn(async () => dashboardState())
    __setWailsMocks({
      GetAppState: vi.fn(async () => profileSetupState()),
      ValidateProfile: validate,
      SaveProfile: save
    })
    const user = userEvent.setup()

    render(App)

    await user.type(await screen.findByPlaceholderText(/SteamID64/), 'test-player')
    await user.click(screen.getByRole('button', {name: 'Validate'}))

    expect(validate).toHaveBeenCalledWith('test-player')
    expect(await screen.findByText('Profile found')).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Test Player'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Save and sync'}))

    expect(save).toHaveBeenCalledWith(preview)
    expect(await screen.findByRole('heading', {name: 'Dashboard'})).toBeInTheDocument()
  })

  test('shows startup init errors with log path', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => ({...baseState(), initError: 'database failed'}))})

    render(App)

    expect(await screen.findByRole('heading', {name: 'Startup failed'})).toBeInTheDocument()
    expect(screen.getByText('database failed')).toBeInTheDocument()
    expect(screen.getByText(/app.log/)).toBeInTheDocument()
  })
})

describe('App dashboard views', () => {
  test('switches views and filters suggestions by tag', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})
    const user = userEvent.setup()

    render(App)

    expect(await screen.findByRole('heading', {name: 'Dashboard'})).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Hades'})).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Vampire Survivors'})).toBeInTheDocument()
    expect(screen.getByRole('button', {name: /Almost there\s+1/})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: /Almost there\s+1/}))

    expect(screen.getByRole('heading', {name: 'Hades'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Vampire Survivors'})).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Clear tag filter'}))

    expect(screen.getByRole('heading', {name: 'Vampire Survivors'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Completed'}))
    expect(await screen.findByRole('heading', {name: 'Portal'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Hades'})).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Sync errors'}))
    expect(await screen.findByRole('heading', {name: 'Broken Game'})).toBeInTheDocument()
    expect(screen.getByText('HTTP 503')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Disable'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Disabled'}))
    expect(await screen.findByRole('heading', {name: 'Hidden Game'})).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Re-enable'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Settings'}))
    expect(await screen.findByRole('heading', {name: 'Settings'})).toBeInTheDocument()
    expect(screen.getByText('/tmp/steam-achievement-tracker')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Clear data'})).toBeInTheDocument()
  })

  test('searches within the active view and shows empty states', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})
    const user = userEvent.setup()

    render(App)

    const search = await screen.findByPlaceholderText('Search games in this view')
    await user.type(search, 'missing title')

    expect(screen.getByText('No games match the current filters.')).toBeInTheDocument()

    await user.clear(search)
    await user.click(screen.getByRole('button', {name: 'Completed'}))
    await user.type(screen.getByPlaceholderText('Search games in this view'), 'missing title')

    expect(screen.getByText('No matching games.')).toBeInTheDocument()
  })

  test('shows fresh sync failures as sync errors, not no achievements', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState({warnings: [game({appid: 7, name: 'Fresh Failure', tags: ['sync_warning'], syncWarning: true, totalAchievements: 0, unlockedAchievements: 0, lastError: 'HTTP 503', lastErrorAt: '2026-05-22T10:00:00Z', lastSyncedAt: ''})]}))})
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Sync errors'}))
    const row = screen.getByRole('heading', {name: 'Fresh Failure'}).closest('article')

    expect(within(row).getAllByText('Sync error')).toHaveLength(2)
    expect(within(row).getByText('No previous sync data')).toBeInTheDocument()
    expect(within(row).queryByText('No achievements')).not.toBeInTheDocument()
  })

  test('shows playtime for no-achievement games when Steam reports it', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState({suggestions: [game({appid: 8, name: 'Played No Achievements', tags: ['no_achievements'], totalAchievements: 0, unlockedAchievements: 0, playtimeForever: 75}), game({appid: 9, name: 'Unplayed No Achievements', tags: ['no_achievements', 'untouched'], totalAchievements: 0, unlockedAchievements: 0, playtimeForever: 0})]}))})

    render(App)

    const playedCard = (await screen.findByRole('heading', {name: 'Played No Achievements'})).closest('article')
    expect(within(playedCard).getAllByText('No achievements')).toHaveLength(1)
    expect(within(playedCard).getByText('1h 15m')).toBeInTheDocument()

    const unplayedCard = screen.getByRole('heading', {name: 'Unplayed No Achievements'}).closest('article')
    expect(within(unplayedCard).getAllByText('No achievements')).toHaveLength(1)
    expect(within(unplayedCard).queryByText('0m')).not.toBeInTheDocument()
  })

  test('clears active data through in-app confirmation modal', async () => {
    const clear = vi.fn(async () => profileSetupState())
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      ClearActiveUserData: clear
    })
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Settings'}))
    await user.click(screen.getByRole('button', {name: 'Clear data'}))

    const dialog = screen.getByRole('dialog', {name: 'Clear active user data?'})
    expect(within(dialog).getByText('76561198000000000')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', {name: 'Copy confirmation value'})).toBeInTheDocument()
    expect(within(dialog).getByRole('button', {name: 'Clear data'})).toBeDisabled()

    await user.type(within(dialog).getByLabelText(/Type this exact value/), '76561198000000000')
    await user.click(within(dialog).getByRole('button', {name: 'Clear data'}))

    expect(clear).toHaveBeenCalledOnce()
    await waitFor(() => expect(screen.getByRole('heading', {name: 'Connect your profile'})).toBeInTheDocument())
  })

  test('calls game action handlers and refreshes dashboard state', async () => {
    const disable = vi.fn(async () => dashboardState({warnings: []}))
    const enable = vi.fn(async () => dashboardState({disabled: []}))
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      DisableGame: disable,
      EnableGame: enable
    })
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Sync errors'}))
    await user.click(screen.getByRole('button', {name: 'Disable'}))

    expect(disable).toHaveBeenCalledWith(4)
    await waitFor(() => expect(screen.getByText('No sync errors.')).toBeInTheDocument())

    await user.click(screen.getByRole('button', {name: 'Disabled'}))
    await user.click(screen.getByRole('button', {name: 'Re-enable'}))

    expect(enable).toHaveBeenCalledWith(5)
    await waitFor(() => expect(screen.getByText('No disabled games.')).toBeInTheDocument())
  })
})

function baseState() {
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
    secretSetupCommand: 'secret-tool store service steam-achievement-tracker username steam-web-api-key'
  }
}

function missingKeyState() {
  return {...baseState(), apiKeyPresent: false}
}

function profileSetupState() {
  return {...baseState(), apiKeyPresent: true, profileExists: false}
}

function dashboardState(overrides = {}) {
  const suggestions = overrides.suggestions ?? [
    game({appid: 1, name: 'Hades', tags: ['almost_there'], completionPercent: 86, unlockedAchievements: 43, totalAchievements: 50, missingAvgUnlock: 38, playtimeForever: 620}),
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
    profile: {steamId64: '76561198000000000', displayName: 'Test Player', avatarUrl: ''},
    dashboard: {
      summary: {
        ownedGamesCount: 6,
        completedGamesCount: completed.length,
        totalAchievementsUnlocked: 180
      },
      latestSyncRun: {
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

function game(overrides = {}) {
  return {
    appid: 0,
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
    ...overrides
  }
}
