import {cleanup, render, screen, waitFor} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {__resetWailsMocks, __setWailsMocks} from './test/wails-app-mock.js'
import {__emitWailsEvent, __listenerCount} from './test/wails-runtime-mock.js'
import {availableSnapshot, flush, latestRelease, releaseURL, updateSnapshot} from './test/update-fixtures.js'
import {baseState, dashboardState} from './test/app-fixtures.js'

afterEach(() => {
  cleanup()
  __resetWailsMocks()
  vi.restoreAllMocks()
})

function banner() {
  return screen.queryByRole('region', {name: 'Application update'})
}

async function openSettings(user) {
  await user.click(await screen.findByRole('button', {name: 'Settings'}))
  return screen.findByRole('heading', {name: 'Settings'})
}

describe('App update integration', () => {
  test('the banner is not shown on the init-error screen', async () => {
    const getUpdateState = vi.fn(async () => availableSnapshot())
    __setWailsMocks({
      GetAppState: vi.fn(async () => ({...baseState(), initError: 'Could not open the database.'})),
      GetUpdateState: getUpdateState
    })

    render(App)

    expect(await screen.findByRole('heading', {name: 'Startup failed'})).toBeInTheDocument()
    await waitFor(() => expect(getUpdateState).toHaveBeenCalled())
    await flush()
    expect(banner()).not.toBeInTheDocument()
    expect(screen.queryByText('Version 1.2.0 is available')).not.toBeInTheDocument()
  })

  test('the banner is shown on the dashboard when an update is available', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: vi.fn(async () => availableSnapshot())
    })

    render(App)

    expect(await screen.findByRole('region', {name: 'Application update'})).toBeInTheDocument()
    expect(screen.getByText('Version 1.2.0 is available')).toBeInTheDocument()
    expect(screen.getByRole('link', {name: 'Release notes'})).toHaveAttribute('href', releaseURL)
  })

  test('Install on the dashboard banner calls InstallUpdate with the version shown', async () => {
    const user = userEvent.setup()
    const installUpdate = vi.fn(async () => {})
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: vi.fn(async () => availableSnapshot()),
      InstallUpdate: installUpdate
    })

    render(App)
    await user.click(await screen.findByRole('button', {name: 'Install'}))

    expect(installUpdate).toHaveBeenCalledOnce()
    expect(installUpdate).toHaveBeenCalledWith('1.2.0')
  })

  test('update:changed refreshes the banner without a reload', async () => {
    let current = updateSnapshot()
    const getUpdateState = vi.fn(async () => current)
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: getUpdateState
    })

    render(App)
    await waitFor(() => expect(getUpdateState).toHaveBeenCalled())
    await flush()
    expect(banner()).not.toBeInTheDocument()

    current = availableSnapshot()
    __emitWailsEvent('update:changed')

    expect(await screen.findByText('Version 1.2.0 is available')).toBeInTheDocument()
  })

  test('the banner stays hidden when the update state cannot be read', async () => {
    // No GetUpdateState override: the default mock rejects, the way a missing binding would.
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})

    render(App)

    expect(await screen.findByRole('heading', {name: 'Dashboard'})).toBeInTheDocument()
    await flush()
    expect(banner()).not.toBeInTheDocument()
  })

  test('an unavailable updater shows no update UI on the dashboard or in Settings', async () => {
    const user = userEvent.setup()
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: vi.fn(async () => updateSnapshot({unavailable: true, phase: 'available', latest: latestRelease()}))
    })

    render(App)
    await screen.findByRole('heading', {name: 'Dashboard'})
    await flush()
    expect(banner()).not.toBeInTheDocument()

    await openSettings(user)
    await flush()
    expect(screen.queryByRole('heading', {name: 'Updates'})).not.toBeInTheDocument()
    expect(screen.queryByRole('switch', {name: 'Check for updates on startup'})).not.toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Check now'})).not.toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Install'})).not.toBeInTheDocument()
  })

  test('Settings shows the Updates section when the updater is available', async () => {
    const user = userEvent.setup()
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: vi.fn(async () => updateSnapshot({checksEnabled: true}))
    })

    render(App)
    await openSettings(user)

    expect(await screen.findByRole('heading', {name: 'Updates'})).toBeInTheDocument()
    expect(screen.getByRole('switch', {name: 'Check for updates on startup'})).toBeChecked()
    expect(screen.getByRole('button', {name: 'Check now'})).toBeEnabled()
    expect(banner()).not.toBeInTheDocument()
  })

  test('Settings shows the Updates section when the first update read lands after Settings was opened', async () => {
    const user = userEvent.setup()
    // The store is not loaded until GetUpdateState resolves. The gear opens Settings first; the section must appear
    // once the snapshot arrives, with no reload.
    let resolveRead
    const pendingRead = new Promise((resolve) => {
      resolveRead = resolve
    })
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: vi.fn(() => pendingRead)
    })

    render(App)
    await openSettings(user)
    await flush()
    expect(screen.queryByRole('heading', {name: 'Updates'})).not.toBeInTheDocument()

    resolveRead(updateSnapshot({checksEnabled: true}))

    expect(await screen.findByRole('heading', {name: 'Updates'})).toBeInTheDocument()
    expect(screen.getByRole('switch', {name: 'Check for updates on startup'})).toBeChecked()
  })

  test('the Settings toggle reaches SetUpdateChecksEnabled through the app', async () => {
    const user = userEvent.setup()
    // The backend keeps the value it stored (true), not the one clicked (false): the box must snap back to it. Starting
    // from a result that differs from the click keeps the final assertion from passing before the result arrives.
    const setChecks = vi.fn(async () => updateSnapshot({checksEnabled: true}))
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: vi.fn(async () => updateSnapshot({checksEnabled: true})),
      SetUpdateChecksEnabled: setChecks
    })

    render(App)
    await openSettings(user)
    await user.click(await screen.findByRole('switch', {name: 'Check for updates on startup'}))

    expect(setChecks).toHaveBeenCalledWith(false)
    await waitFor(() => expect(screen.getByRole('switch', {name: 'Check for updates on startup'})).toBeChecked())
  })

  test('Check now from Settings calls CheckForUpdates', async () => {
    const user = userEvent.setup()
    const check = vi.fn(async () => {})
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: vi.fn(async () => updateSnapshot({phase: 'idle'})),
      CheckForUpdates: check
    })

    render(App)
    await openSettings(user)
    await user.click(await screen.findByRole('button', {name: 'Check now'}))

    expect(check).toHaveBeenCalledOnce()
  })

  test('the update UI does not appear on the missing-key setup screen', async () => {
    const getUpdateState = vi.fn(async () => availableSnapshot())
    __setWailsMocks({
      GetAppState: vi.fn(async () => ({...baseState(), apiKeyPresent: false})),
      GetUpdateState: getUpdateState
    })

    render(App)

    expect(await screen.findByRole('heading', {name: 'Steam Web API key missing'})).toBeInTheDocument()
    await waitFor(() => expect(getUpdateState).toHaveBeenCalled())
    await flush()
    expect(banner()).not.toBeInTheDocument()
  })

  test('unmounting the app removes the update:changed listener', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      GetUpdateState: vi.fn(async () => updateSnapshot())
    })

    const view = render(App)
    await screen.findByRole('heading', {name: 'Dashboard'})
    await flush()
    expect(__listenerCount('update:changed')).toBe(1)

    view.unmount()
    await flush()

    expect(__listenerCount('update:changed')).toBe(0)
  })
})
