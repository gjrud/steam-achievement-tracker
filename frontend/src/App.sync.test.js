import {cleanup, fireEvent, render, screen, within} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, beforeEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {__resetWailsMocks, __setWailsMocks} from './test/wails-app-mock.js'
import {__listenerCount, RuntimeError} from './test/wails-runtime-mock.js'
import {syncAlreadyRunningMessage} from './lib/sync.js'
import {
  advance,
  dashboardState,
  emitSyncChanged,
  failureMessage,
  finishedState,
  hadesCard,
  profileSetupState,
  rejecting,
  SYNC_EVENT_GAP_MS,
  settle,
  syncingState,
  syncRun
} from './test/app-fixtures.js'

afterEach(() => {
  cleanup()
  __resetWailsMocks()
  vi.restoreAllMocks()
})

// The dashboard has two status regions now: the sync line in the profile card and the update banner's announcer. The
// sync tests read the profile card's.
function syncStatus() {
  return within(document.querySelector('.profile-card')).getByRole('status')
}

// The app reads state once on mount and again only when the backend emits sync:changed (or, after a failed read during
// a sync, every 2s until a read succeeds or the sync ends). These tests drive those events under fake timers, so every
// async step goes through settle()/advance() and fireEvent. userEvent and waitFor both wait on timers the fake clock
// never fires.
describe('App sync events', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    cleanup()
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  test('refetches on each sync:changed event and shows progress until the run finishes', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(4))
      .mockResolvedValueOnce(dashboardState())
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()

    expect(syncStatus()).toHaveTextContent('Current sync: Sync running')
    expect(screen.getByText(/Syncing 1 \/ 6/)).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Syncing…'})).toBeDisabled()
    expect(getState).toHaveBeenCalledTimes(1)

    emitSyncChanged()
    await settle()
    expect(screen.getByText(/Syncing 4 \/ 6/)).toBeInTheDocument()
    expect(getState).toHaveBeenCalledTimes(2)

    await advance(SYNC_EVENT_GAP_MS)
    emitSyncChanged()
    await settle()
    expect(syncStatus()).toHaveTextContent('Last sync: Partial sync, 1 failed')
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('makes no reads without an event', async () => {
    const getState = vi.fn(async () => syncingState(1))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    await advance(10_000)

    expect(getState).toHaveBeenCalledTimes(1)
    expect(screen.getByText(/Syncing 1 \/ 6/)).toBeInTheDocument()
  })

  test('coalesces a burst of events during an in-flight read into exactly one extra read', async () => {
    let resolveSlowRead
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveSlowRead = resolve
      }))
      .mockResolvedValueOnce(syncingState(5))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()

    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)

    // The burst lands inside the gap: one read is owed, and it waits for the slow read in flight.
    for (let i = 0; i < 5; i++) emitSyncChanged()
    await advance(SYNC_EVENT_GAP_MS)
    expect(getState).toHaveBeenCalledTimes(2)

    resolveSlowRead(syncingState(3))
    await settle()
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.getByText(/Syncing 5 \/ 6/)).toBeInTheDocument()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('a burst of events inside the gap costs one leading read and one trailing read', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(2))
      .mockResolvedValueOnce(syncingState(6))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()

    for (let i = 0; i < 5; i++) emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)

    await advance(SYNC_EVENT_GAP_MS)
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.getByText(/Syncing 6 \/ 6/)).toBeInTheDocument()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('the last event inside the gap is read when the gap ends', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(2))
      .mockResolvedValueOnce(syncingState(5))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)

    await advance(100)
    emitSyncChanged()
    await advance(SYNC_EVENT_GAP_MS - 101)
    expect(getState).toHaveBeenCalledTimes(2)

    await advance(1)
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.getByText(/Syncing 5 \/ 6/)).toBeInTheDocument()
  })

  test('an event after a quiet gap is read at once', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(2))
      .mockResolvedValueOnce(syncingState(3))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    await advance(SYNC_EVENT_GAP_MS)

    emitSyncChanged()
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('unmounting during a deferred event read clears its timer and makes no read', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(2))
    __setWailsMocks({GetAppState: getState})

    const {unmount} = render(App)
    await settle()
    emitSyncChanged()
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)

    unmount()
    expect(vi.getTimerCount()).toBe(0)
    await advance(SYNC_EVENT_GAP_MS * 2)
    expect(getState).toHaveBeenCalledTimes(2)
  })

  test('an event during the initial read gets one follow-up read, which shows the final state', async () => {
    let resolveInitial
    const getState = vi.fn()
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveInitial = resolve
      }))
      .mockResolvedValueOnce(finishedState())
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    expect(getState).toHaveBeenCalledTimes(1)

    // The event lands while the initial read is still in flight: it queues one read rather than starting another.
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(1)

    resolveInitial(syncingState(1))
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)
    expect(syncStatus()).toHaveTextContent('Last sync: All games synced')
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(2)
  })

  test('defers an event that arrives during an action until the action finishes', async () => {
    let resolveDisable
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(finishedState())
    const disable = vi.fn(() => new Promise((resolve) => {
      resolveDisable = resolve
    }))
    __setWailsMocks({GetAppState: getState, DisableGame: disable})

    render(App)
    await settle()
    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))

    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(1)

    resolveDisable(syncingState(2))
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(/Syncing 2 \/ 6/)).not.toBeInTheDocument()
    expect(syncStatus()).toHaveTextContent('Last sync: All games synced')
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
  })

  test('an event that waits out its gap during an action is read once, after the action finishes', async () => {
    let resolveDisable
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(2))
      .mockResolvedValueOnce(finishedState())
    const disable = vi.fn(() => new Promise((resolve) => {
      resolveDisable = resolve
    }))
    __setWailsMocks({GetAppState: getState, DisableGame: disable})

    render(App)
    await settle()
    // The leading event is read at once (read #2 counting the mount read as #1), and opens the gap.
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)

    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    expect(disable).toHaveBeenCalledOnce()

    // A second event at 100 ms waits for the gap. The gap ends at 300 ms with the action still in flight, so no read.
    await advance(100)
    emitSyncChanged()
    await advance(SYNC_EVENT_GAP_MS - 100)
    expect(getState).toHaveBeenCalledTimes(2)

    // The action ends: exactly one more read, which returns the newer state.
    resolveDisable(syncingState(3))
    await settle()
    expect(getState).toHaveBeenCalledTimes(3)
    expect(syncStatus()).toHaveTextContent('Last sync: All games synced')
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('drops a read that an action overtook and reads again after the action', async () => {
    let resolveSlowRead
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveSlowRead = resolve
      }))
      .mockResolvedValueOnce(finishedState())
    const disable = vi.fn(async () => syncingState(4))
    __setWailsMocks({GetAppState: getState, DisableGame: disable})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)

    // The action takes over the request token while the read is still in flight.
    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    await settle()
    expect(screen.getByText(/Syncing 4 \/ 6/)).toBeInTheDocument()

    // The overtaken read resolves with older data: it must not be shown, and it must trigger a fresh read.
    resolveSlowRead(syncingState(2))
    await settle()
    expect(screen.queryByText(/Syncing 2 \/ 6/)).not.toBeInTheDocument()
    expect(getState).toHaveBeenCalledTimes(3)
    expect(syncStatus()).toHaveTextContent('Last sync: All games synced')
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
  })

  test('shows a failed event read and retries once after 2s while the sync runs', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
      .mockResolvedValueOnce(syncingState(3))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()
    expect(getState).toHaveBeenCalledTimes(2)

    await advance(1999)
    expect(getState).toHaveBeenCalledTimes(2)

    await advance(1)
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.queryByText(failureMessage)).not.toBeInTheDocument()
    expect(screen.getByText(/Syncing 3 \/ 6/)).toBeInTheDocument()

    await advance(10_000)
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('keeps retrying while reads keep failing during a sync, one timer at a time', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
      .mockResolvedValueOnce(finishedState())
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)

    await advance(2000)
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(2000)
    expect(getState).toHaveBeenCalledTimes(4)
    expect(screen.queryByText(failureMessage)).not.toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
  })

  test('does not retry a failed event read when no sync is running', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(dashboardState())
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(10_000)
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByText(failureMessage)).toBeInTheDocument()
  })

  test('a successful read clears a failed-read banner and cancels its pending retry', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
      .mockResolvedValueOnce(syncingState(3))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(1000)
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.queryByText(failureMessage)).not.toBeInTheDocument()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(3)
  })

  test('keeps an action error through event reads until a later action succeeds', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(3))
      .mockResolvedValueOnce(syncingState(4))
      .mockResolvedValue(finishedState())
    const disable = vi.fn()
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
      .mockResolvedValueOnce(syncingState(5))
    __setWailsMocks({GetAppState: getState, DisableGame: disable})

    render(App)
    await settle()

    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    // Successful event reads refresh the sync progress and leave the action's banner in place.
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByText(/Syncing 3 \/ 6/)).toBeInTheDocument()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(SYNC_EVENT_GAP_MS)
    emitSyncChanged()
    await settle()
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

  test('an event during an in-flight SyncNow is read once SyncNow ends', async () => {
    let resolveSync
    const getState = vi.fn()
      .mockResolvedValueOnce(dashboardState())
      .mockResolvedValueOnce(syncingState(5))
    const syncNow = vi.fn(() => new Promise((resolve) => {
      resolveSync = resolve
    }))
    __setWailsMocks({GetAppState: getState, SyncNow: syncNow})

    render(App)
    await settle()
    await fireEvent.click(screen.getByRole('button', {name: 'Sync now'}))
    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(1)

    resolveSync(syncingState(1))
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByText(/Syncing 5 \/ 6/)).toBeInTheDocument()
  })

  test('an event that arrives while the trailing read is in flight gets one more read after it', async () => {
    let resolveTrailing
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValueOnce(syncingState(2))
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveTrailing = resolve
      }))
      .mockResolvedValueOnce(syncingState(6))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    // t=0: leading read. t=100: owed a trailing read. t=300: the trailing read starts and stays in flight.
    emitSyncChanged()
    await settle()
    await advance(100)
    emitSyncChanged()
    await advance(SYNC_EVENT_GAP_MS - 100)
    expect(getState).toHaveBeenCalledTimes(3)

    // t=400: an event inside the trailing read's gap. It is owed a read, which queues behind the in-flight read.
    await advance(100)
    emitSyncChanged()
    await advance(SYNC_EVENT_GAP_MS - 100)
    expect(getState).toHaveBeenCalledTimes(3)

    resolveTrailing(syncingState(4))
    await settle()
    expect(getState).toHaveBeenCalledTimes(4)
    expect(screen.getByText(/Syncing 6 \/ 6/)).toBeInTheDocument()
  })

  test('a successful action cancels a pending failed-read retry', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
    const disable = vi.fn(async () => syncingState(2))
    __setWailsMocks({GetAppState: getState, DisableGame: disable})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    await settle()
    expect(screen.queryByText(failureMessage)).not.toBeInTheDocument()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByText(/Syncing 2 \/ 6/)).toBeInTheDocument()
  })

  test('a failed action inside the retry window still lets the failed read\'s retry clear Syncing…', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
      .mockResolvedValueOnce(finishedState())
    const disable = vi.fn(async () => {
      throw new RuntimeError('Disable failed')
    })
    __setWailsMocks({GetAppState: getState, DisableGame: disable})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(1000)
    await fireEvent.contextMenu(hadesCard())
    await fireEvent.click(screen.getByRole('button', {name: 'Disable game'}))
    await settle()
    expect(disable).toHaveBeenCalledTimes(1)
    expect(screen.getByText('Disable failed')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Syncing…'})).toBeDisabled()
    expect(getState).toHaveBeenCalledTimes(2)

    // The retry armed 2s after the failed read still fires, though the action failed in between and no event followed.
    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(3)
    expect(syncStatus()).toHaveTextContent('Last sync: All games synced')
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
    // The action's own error is not a read error, so the successful read leaves it in place.
    expect(screen.getByText('Disable failed')).toBeInTheDocument()
  })

  test('after an already-running refusal, Sync stays disabled until the queued refresh resolves', async () => {
    let resolveRefresh
    const getState = vi.fn()
      .mockResolvedValueOnce(dashboardState())
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveRefresh = resolve
      }))
    const syncNow = vi.fn(async () => {
      throw new RuntimeError(syncAlreadyRunningMessage)
    })
    __setWailsMocks({GetAppState: getState, SyncNow: syncNow})

    render(App)
    await settle()
    await fireEvent.click(screen.getByRole('button', {name: 'Sync now'}))
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)
    // The refusal has ended the SyncNow call, but the refresh that says why is still in flight.
    expect(screen.getByRole('button', {name: 'Syncing…'})).toBeDisabled()
    expect(screen.queryByText(syncAlreadyRunningMessage)).not.toBeInTheDocument()

    resolveRefresh(syncingState(2))
    await settle()
    expect(screen.getByText(/Syncing 2 \/ 6/)).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Syncing…'})).toBeDisabled()
  })

  test('a failed refresh after an already-running refusal keeps Sync disabled and retries after 2s', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(dashboardState())
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
      .mockResolvedValueOnce(syncingState(3))
    const syncNow = vi.fn(async () => {
      throw new RuntimeError(syncAlreadyRunningMessage)
    })
    __setWailsMocks({GetAppState: getState, SyncNow: syncNow})

    render(App)
    await settle()
    await fireEvent.click(screen.getByRole('button', {name: 'Sync now'}))
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByText(failureMessage)).toBeInTheDocument()
    // The held sync flag is what arms the retry: the failed read left the optimistic state in place.
    expect(screen.getByRole('button', {name: 'Syncing…'})).toBeDisabled()

    await advance(1999)
    expect(getState).toHaveBeenCalledTimes(2)
    await advance(1)
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.queryByText(failureMessage)).not.toBeInTheDocument()
    expect(screen.getByText(/Syncing 3 \/ 6/)).toBeInTheDocument()
  })

  test('unsubscribes from sync:changed on unmount and makes no reads after it', async () => {
    const getState = vi.fn(async () => syncingState(2))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    expect(__listenerCount('sync:changed')).toBe(1)

    cleanup()
    expect(__listenerCount('sync:changed')).toBe(0)

    emitSyncChanged()
    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(1)
  })

  test('cancels a pending failed-read retry on unmount', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockRejectedValueOnce(new RuntimeError(failureMessage))
    __setWailsMocks({GetAppState: getState})

    render(App)
    await settle()
    emitSyncChanged()
    await settle()

    cleanup()
    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(2)
  })

  test('does not read again after a rejected SyncNow until an event arrives', async () => {
    const getState = vi.fn(async () => dashboardState())
    __setWailsMocks({GetAppState: getState, SyncNow: rejecting()})

    render(App)
    await settle()
    await fireEvent.click(screen.getByRole('button', {name: 'Sync now'}))
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    await advance(5000)
    expect(getState).toHaveBeenCalledTimes(1)

    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(2)
  })

  // Each action runs while the dashboard reports a sync. A rejection must leave the banner in place and the
  // dashboard must still follow sync:changed until the run finishes. RetryAPIKey and SaveProfile are not listed:
  // they render only in setup views, which the dashboard's syncing state never reaches. SaveAPIKey is not listed
  // either: its error is shown by the key form, not this banner, and it never calls beginAction, so there is no
  // banner or read hold to check. Its live-sync behaviour has its own test below.
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
    test(`follows sync:changed after a rejected ${row.name} until the sync finishes`, async () => {
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

      await advance(5000)
      expect(getState).toHaveBeenCalledTimes(1)

      emitSyncChanged()
      await settle()
      expect(screen.getByText(/Syncing 3 \/ 6/)).toBeInTheDocument()
      expect(getState).toHaveBeenCalledTimes(2)

      await advance(SYNC_EVENT_GAP_MS)
      emitSyncChanged()
      await settle()
      expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
      expect(getState).toHaveBeenCalledTimes(3)

      await advance(5000)
      expect(getState).toHaveBeenCalledTimes(3)
    })
  }

  test('keeps a settings save during a sync and reads the saved setting after it', async () => {
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

    await fireEvent.click(unvetted)
    expect(setSettings).toHaveBeenCalledWith(false, true)

    // An event during the save waits for it: a read now could race the save and show the old setting.
    emitSyncChanged()
    await advance(3000)
    expect(getState).toHaveBeenCalledTimes(1)

    resolveSave({...syncingState(2), settings: saved})
    await settle()
    expect(unvetted).not.toBeChecked()
    expect(unvetted).toHaveAttribute('aria-busy', 'false')
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('switch', {name: 'Include unvetted apps'})).not.toBeChecked()
  })

  test('a failed clear-data action keeps its banner while later events refresh the sync', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(syncingState(1))
      .mockResolvedValue(finishedState())
    __setWailsMocks({GetAppState: getState, ClearActiveUserData: rejecting()})

    render(App)
    await settle()

    await fireEvent.click(screen.getByRole('button', {name: 'Settings'}))
    await fireEvent.click(screen.getByRole('button', {name: 'Remove profile'}))
    const dialog = screen.getByRole('dialog', {name: 'Remove active profile?'})
    await fireEvent.input(within(dialog).getByLabelText(/Type this exact value/), {target: {value: '76561198000000000'}})
    await fireEvent.click(within(dialog).getByRole('button', {name: 'Remove profile'}))
    await settle()
    expect(screen.getByText(failureMessage)).toBeInTheDocument()

    // The failed clear re-reads the state once (the backend may have deleted the key first). The banner survives it.
    expect(getState).toHaveBeenCalledTimes(2)
    await advance(1000)
    expect(getState).toHaveBeenCalledTimes(2)

    emitSyncChanged()
    await settle()
    expect(getState).toHaveBeenCalledTimes(3)
    expect(screen.getByText(failureMessage)).toBeInTheDocument()
  })
})

describe('App sync now', () => {
  test('calls SyncNow and shows a disabled Syncing state until the sync ends', async () => {
    const syncNow = vi.fn(async () => syncingState(0))
    __setWailsMocks({
      GetAppState: vi.fn().mockResolvedValueOnce(dashboardState()),
      SyncNow: syncNow
    })
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Sync now'}))

    expect(syncNow).toHaveBeenCalledOnce()
    expect(await screen.findByRole('button', {name: 'Syncing…'})).toBeDisabled()
    expect(syncStatus()).toHaveTextContent('Current sync: Sync running')
  })

  test('a second click while SyncNow is in flight does not call it again', async () => {
    let resolveSync
    const syncNow = vi.fn(() => new Promise((resolve) => {
      resolveSync = resolve
    }))
    __setWailsMocks({
      GetAppState: vi.fn().mockResolvedValueOnce(dashboardState()),
      SyncNow: syncNow
    })

    render(App)
    const button = await screen.findByRole('button', {name: 'Sync now'})
    // Two native clicks before Svelte re-renders: the second one reaches the handler, as a double click would.
    button.click()
    button.click()

    expect(syncNow).toHaveBeenCalledOnce()
    expect(await screen.findByRole('button', {name: 'Syncing…'})).toBeDisabled()

    resolveSync(syncingState(0))
    expect(await screen.findByText(/Syncing 0 \/ 6/)).toBeInTheDocument()
    expect(syncNow).toHaveBeenCalledOnce()
  })

  test('an overlapping SyncNow refusal shows no banner and re-reads the state', async () => {
    const getState = vi.fn()
      .mockResolvedValueOnce(dashboardState())
      .mockResolvedValueOnce(syncingState(2))
    const syncNow = vi.fn(async () => {
      throw new RuntimeError(syncAlreadyRunningMessage)
    })
    __setWailsMocks({GetAppState: getState, SyncNow: syncNow})

    render(App)
    await fireEvent.click(await screen.findByRole('button', {name: 'Sync now'}))

    expect(await screen.findByText(/Syncing 2 \/ 6/)).toBeInTheDocument()
    expect(screen.queryByText(syncAlreadyRunningMessage)).not.toBeInTheDocument()
    expect(getState).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('button', {name: 'Syncing…'})).toBeDisabled()
  })

  test('other SyncNow errors still show the banner and do not re-read', async () => {
    const getState = vi.fn().mockResolvedValueOnce(dashboardState())
    __setWailsMocks({GetAppState: getState, SyncNow: rejecting()})

    render(App)
    await fireEvent.click(await screen.findByRole('button', {name: 'Sync now'}))

    expect(await screen.findByText(failureMessage)).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
    expect(getState).toHaveBeenCalledTimes(1)
  })

  test('Sync now is not sent while a clear is in flight, so the clear keeps its request token', async () => {
    let resolveClear
    const clear = vi.fn(() => new Promise((resolve) => {
      resolveClear = resolve
    }))
    const syncNow = vi.fn(async () => syncingState(0))
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      ClearActiveUserData: clear,
      SyncNow: syncNow
    })

    render(App)
    await fireEvent.click(await screen.findByRole('button', {name: 'Settings'}))
    await fireEvent.click(screen.getByRole('button', {name: 'Remove profile'}))
    const dialog = screen.getByRole('dialog', {name: 'Remove active profile?'})
    await fireEvent.input(within(dialog).getByLabelText(/Type this exact value/), {target: {value: '76561198000000000'}})
    await fireEvent.click(within(dialog).getByRole('button', {name: 'Remove profile'}))
    expect(clear).toHaveBeenCalledOnce()

    // The open modal covers the header Sync now button in the browser. jsdom does no hit-testing, so this click
    // reaches the handler the way a stray event would; the guard in syncNow is what keeps SyncNow from being called.
    await fireEvent.click(screen.getByRole('button', {name: 'Sync now'}))
    expect(syncNow).not.toHaveBeenCalled()

    resolveClear(profileSetupState())
    expect(await screen.findByRole('heading', {name: 'Connect your profile'})).toBeInTheDocument()
    expect(syncNow).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog', {name: 'Remove active profile?'})).not.toBeInTheDocument()
  })
})
