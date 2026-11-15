import {cleanup, fireEvent, render, screen, within} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, beforeEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {__resetWailsMocks, __setWailsMocks} from './test/wails-app-mock.js'
import {
  advance,
  dashboardState,
  failureMessage,
  finishedState,
  hadesCard,
  rejecting,
  settle,
  syncingState,
  syncRun
} from './test/app-fixtures.js'

afterEach(() => {
  cleanup()
  __resetWailsMocks()
  vi.restoreAllMocks()
})

// Polling tests run under fake timers, so every async step goes through settle()/advance() and fireEvent.
// userEvent and waitFor both wait on timers the fake clock never fires, so they hang in this describe.
describe('App sync polling', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    cleanup()
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  test('polls while a sync runs, shows progress, and stops when it finishes', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(4))
      .mockResolvedValueOnce(dashboardState())
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()

    expect(screen.getByRole('status')).toHaveTextContent('Current sync: Sync running')
    expect(screen.getByText(/Syncing 1 \/ 6/)).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Syncing…'})).toBeDisabled()
    expect(getState).toHaveBeenCalledTimes(1)

    await advance(1000)
    expect(screen.getByText(/Syncing 4 \/ 6/)).toBeInTheDocument()
    expect(getState).toHaveBeenCalledTimes(2)

    await advance(1000)
    expect(screen.getByRole('status')).toHaveTextContent('Last sync: Partial sync, 1 failed')
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
    expect(getState).toHaveBeenCalledTimes(3)

    await advance(10_000)
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('keeps polling after a failed poll, shows the error, and clears it on the next poll', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockRejectedValueOnce(new Error(failureMessage))
      .mockResolvedValueOnce(syncingState(3))
      .mockResolvedValueOnce(dashboardState())
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    await advance(1000)
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(1000)
    expect(screen.queryByText(failureMessage)).not.toBeInTheDocument()
    expect(screen.getByText(/Syncing 3 \/ 6/)).toBeInTheDocument()
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('keeps a failed action error through background polls until a later action succeeds', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(3))
      .mockResolvedValueOnce(syncingState(4))
      .mockResolvedValue(finishedState())
    const disable = vi.fn()
      .mockRejectedValueOnce(new Error(failureMessage))
      .mockResolvedValueOnce(syncingState(5))
    __setWailsMocks({GetAppState: getState, DisableGame: disable})

    render(App)
    await settle()

    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    // Polling continues while the sync runs; the action's banner must survive each poll.
    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByText(/Syncing 3 \/ 6/)).toBeInTheDocument()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.getByText(/Syncing 4 \/ 6/)).toBeInTheDocument()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    // A later action that succeeds takes over the error state and clears the banner.
    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    await settle()
    expect(disable).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(failureMessage)).not.toBeInTheDocument()
    expect(screen.getByText(/Syncing 5 \/ 6/)).toBeInTheDocument()
  })

  test('stops polling when the dashboard unmounts', async () => {
    const getState = vi.fn(async () => syncingState(2))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    cleanup()
    await advance(5000)

    expect(getState).toHaveBeenCalledTimes(1)
  })

  test('ignores a slow poll that resolves after a newer action', async () => {
    let resolveSlowPoll
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(2))
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveSlowPoll = resolve
      }))
    const disable = vi.fn(async () => finishedState())
    __setWailsMocks({GetAppState: getState, DisableGame: disable})

    render(App)
    await settle()
    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(2)

    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    await settle()
    expect(disable).toHaveBeenCalledWith(1, 0)
    expect(screen.getByText(/6 \/ 6 games/)).toBeInTheDocument()

    resolveSlowPoll(syncingState(5))
    await settle()
    expect(screen.getByText(/6 \/ 6 games/)).toBeInTheDocument()
    expect(screen.queryByText(/Syncing 5 \/ 6/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(2)
  })

  test('restarts polling when clearing data fails while a sync runs', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(finishedState())
    __setWailsMocks({GetAppState: getState, ClearActiveUserData: rejecting()})

    render(App)
    await settle()
    expect(getState).toHaveBeenCalledTimes(1)

    // clearActiveUserData stops polling before the call; the rejection must restart it.
    await fireEvent.click(screen.getByRole('button', {name: 'Settings'}))
    await fireEvent.click(screen.getByRole('button', {name: 'Clear data'}))
    const dialog = screen.getByRole('dialog', {name: 'Clear active user data?'})
    await fireEvent.input(within(dialog).getByLabelText(/Type this exact value/), {target: {value: '76561198000000000'}})
    await fireEvent.click(within(dialog).getByRole('button', {name: 'Clear data'}))
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
  })

  test('restarts polling when an action fails while a poll is in flight', async () => {
    let resolveSlowPoll
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveSlowPoll = resolve
      }))
      .mockResolvedValueOnce(finishedState())
    __setWailsMocks({GetAppState: getState, DisableGame: rejecting()})

    render(App)
    await settle()
    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(2)

    // The failing action takes over the request token, so the stale poll below must not reschedule.
    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    resolveSlowPoll(syncingState(5))
    await settle()
    expect(screen.queryByText(/Syncing 5 \/ 6/)).not.toBeInTheDocument()

    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
  })

  test('does not poll after a rejected SyncNow when the dashboard is idle', async () => {
    const getState = vi.fn(async () => dashboardState())
    __setWailsMocks({GetAppState: getState, SyncNow: rejecting()})

    render(App)
    await settle()
    await fireEvent.click(screen.getByRole('button', {name: 'Sync now'}))
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(1)
  })

  // Each action runs while the dashboard reports a sync. A rejection must leave polling running until
  // the next poll reports the sync finished. RetryAPIKey and SaveProfile are not listed: they render only
  // in setup views, which the dashboard's syncing state never reaches.
  const runningOnly = (gamesSynced) => dashboardState({latestSyncRun: syncRun({status: 'running', finishedAt: '', gamesSynced})})
  const cardMenuAction = (label) => async () => {
    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: label}))
  }
  const syncingRejections = [
    {
      name: 'SyncNow when the run reports running but syncInProgress is false (button stays enabled)',
      initial: () => runningOnly(1),
      mocks: () => ({SyncNow: rejecting()}),
      trigger: () => fireEvent.click(screen.getByRole('button', {name: 'Sync now'}))
    },
    {
      name: 'MarkGamePreviouslyCompleted',
      initial: () => syncingState(1),
      mocks: () => ({MarkGamePreviouslyCompleted: rejecting()}),
      trigger: cardMenuAction('Mark as previously completed')
    },
    {
      name: 'ToggleGameMissingAchievementsInDLC',
      initial: () => syncingState(1),
      mocks: () => ({ToggleGameMissingAchievementsInDLC: rejecting()}),
      trigger: cardMenuAction('Flag missing achievements in DLC')
    },
    {
      name: 'EnableGame from the Disabled view',
      initial: () => syncingState(1),
      mocks: () => ({EnableGame: rejecting()}),
      trigger: async () => {
        await fireEvent.click(screen.getByRole('button', {name: 'Disabled'}))
        await fireEvent.click(screen.getByRole('button', {name: 'Re-enable'}))
      }
    }
  ]

  for (const row of syncingRejections) {
    test(`keeps polling after a rejected ${row.name} until the sync finishes`, async () => {
      const getState = vi.fn()
        .mockResolvedValueOnce(row.initial())
        .mockResolvedValueOnce(syncingState(3))
        .mockResolvedValueOnce(finishedState())
      __setWailsMocks({GetAppState: getState, ...row.mocks()})

      render(App)
      await settle()
      await row.trigger()
      await settle()
      expect(screen.getByText(failureMessage)).toBeInTheDocument()
      expect(getState).toHaveBeenCalledTimes(1)

      await advance(1000)
      expect(screen.getByText(/Syncing 3 \/ 6/)).toBeInTheDocument()
      expect(getState).toHaveBeenCalledTimes(2)

      await advance(1000)
      expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
      expect(getState).toHaveBeenCalledTimes(3)

      await advance(5000)
      expect(getState).toHaveBeenCalledTimes(3)
    })
  }

  test('pauses polling during a library settings save and resumes it while the sync still runs', async () => {
    const saved = {includeUnvettedApps: false, includePlayedFreeGames: true}
    let resolveSave
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(2))
      .mockResolvedValue({...syncingState(3), settings: saved})
    const setSettings = vi.fn(() => new Promise((resolve) => {
      resolveSave = resolve
    }))
    __setWailsMocks({GetAppState: getState, SetLibrarySettings: setSettings})

    render(App)
    await settle()
    await fireEvent.click(screen.getByRole('button', {name: 'Settings'}))
    expect(getState).toHaveBeenCalledTimes(1)
    const unvetted = screen.getByRole('switch', {name: 'Include unvetted apps'})

    // A poll timer is pending from the running sync. It must not fire while the save is in flight.
    await fireEvent.click(unvetted)
    expect(setSettings).toHaveBeenCalledWith(false, true)
    await advance(3000)
    expect(getState).toHaveBeenCalledTimes(1)

    resolveSave({...syncingState(2), settings: saved})
    await settle()
    expect(unvetted).not.toBeChecked()
    expect(unvetted).toHaveAttribute('aria-busy', 'false')
    expect(getState).toHaveBeenCalledTimes(1)

    // The sync is still running, so polling restarts and keeps the saved setting.
    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('switch', {name: 'Include unvetted apps'})).not.toBeChecked()
  })
})

describe('App sync now', () => {
  test('calls SyncNow and shows a disabled Syncing state until the sync ends', async () => {
    const syncNow = vi.fn(async () => syncingState(0))
    __setWailsMocks({
      // Later polls report syncing so a stalled runner cannot flip the button back to idle.
      GetAppState: vi.fn()
        .mockResolvedValueOnce(dashboardState())
        .mockResolvedValue(syncingState(0)),
      SyncNow: syncNow
    })
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Sync now'}))

    expect(syncNow).toHaveBeenCalledOnce()
    expect(await screen.findByRole('button', {name: 'Syncing…'})).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent('Current sync: Sync running')
  })
})
