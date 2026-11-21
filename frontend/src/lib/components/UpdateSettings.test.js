import {get} from 'svelte/store'
import {cleanup, render, screen, waitFor} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import UpdateBanner from './UpdateBanner.svelte'
import UpdateSettings from './UpdateSettings.svelte'
import {createUpdateStore} from '../update.js'
import {RuntimeError} from '../../test/wails-runtime-mock.js'
import {availableSnapshot, fakeUpdateApi, flush, latestRelease, loadedStore, updateSnapshot} from '../../test/update-fixtures.js'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

// visible skips the announcer, which repeats the status line in a screen-reader-only region.
const visible = {ignore: '.sr-only'}

function section() {
  return screen.queryByRole('heading', {name: 'Updates'})
}

function startupSwitch() {
  return screen.getByRole('switch', {name: 'Check for updates on startup'})
}

function checkNow() {
  return screen.getByRole('button', {name: 'Check now'})
}

// expectCheckNowLocked checks the aria-disabled state. Check now is never natively disabled, so it keeps focus.
function expectCheckNowLocked(locked) {
  expect(checkNow()).toHaveAttribute('aria-disabled', locked ? 'true' : 'false')
}

async function renderWith(snapshot, overrides = {}) {
  const loaded = await loadedStore(snapshot, overrides)
  render(UpdateSettings, {props: {updates: loaded.store}})
  await flush()
  return loaded
}

describe('UpdateSettings visibility', () => {
  test('is hidden when the updater is unavailable in this build', async () => {
    const {store} = await renderWith(updateSnapshot({unavailable: true}))

    // The store has read its snapshot, so the hidden section is the rule at work and not an unloaded store.
    expect(get(store).loaded).toBe(true)
    expect(section()).not.toBeInTheDocument()
    expect(screen.queryByRole('switch', {name: 'Check for updates on startup'})).not.toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Check now'})).not.toBeInTheDocument()
  })

  test('is hidden before the first read succeeds', async () => {
    const fake = fakeUpdateApi(availableSnapshot())
    fake.api.getState.mockRejectedValue(new Error('boom'))
    const store = createUpdateStore(fake.api)
    store.start()
    render(UpdateSettings, {props: {updates: store}})
    await flush()

    expect(get(store).loaded).toBe(false)
    expect(section()).not.toBeInTheDocument()
  })

  test('shows the heading and the current version when available', async () => {
    await renderWith(updateSnapshot({currentVersion: '1.1.0'}))

    expect(await screen.findByRole('heading', {name: 'Updates'})).toBeInTheDocument()
    expect(screen.getByText('Current version: 1.1.0')).toBeInTheDocument()
  })
})

describe('UpdateSettings startup check switch', () => {
  test('reflects the stored value', async () => {
    await renderWith(updateSnapshot({checksEnabled: false}))

    expect(await screen.findByRole('switch', {name: 'Check for updates on startup'})).not.toBeChecked()
  })

  test('is on when checks are enabled', async () => {
    await renderWith(updateSnapshot({checksEnabled: true}))

    expect(await screen.findByRole('switch', {name: 'Check for updates on startup'})).toBeChecked()
  })

  test('describes that nothing downloads until Install is chosen', async () => {
    await renderWith(updateSnapshot())

    const sw = await screen.findByRole('switch', {name: 'Check for updates on startup'})
    expect(sw).toHaveAccessibleDescription('Contacts GitHub shortly after the app starts. Nothing is downloaded until you choose Install.')
  })

  test('toggling calls SetUpdateChecksEnabled and stores the snapshot it returns', async () => {
    const user = userEvent.setup()
    const {api, store} = await renderWith(updateSnapshot({checksEnabled: true}))

    await user.click(await screen.findByRole('switch', {name: 'Check for updates on startup'}))

    expect(api.setChecksEnabled).toHaveBeenCalledOnce()
    expect(api.setChecksEnabled).toHaveBeenCalledWith(false)
    // The box is unchecked by the click itself, so the assertion is on the store: it held true before the call.
    await waitFor(() => expect(get(store).savingChecks).toBe(false))
    expect(get(store).snapshot.checksEnabled).toBe(false)
  })

  test('snaps back to the value the snapshot returns when it differs from the click', async () => {
    const user = userEvent.setup()
    await renderWith(updateSnapshot({checksEnabled: true}), {
      setChecksEnabled: vi.fn(async () => updateSnapshot({checksEnabled: true}))
    })

    await user.click(await screen.findByRole('switch', {name: 'Check for updates on startup'}))

    await waitFor(() => expect(startupSwitch()).toBeChecked())
  })

  test('a rejected change shows its sentence and keeps the old value', async () => {
    const user = userEvent.setup()
    await renderWith(updateSnapshot({checksEnabled: true}), {
      setChecksEnabled: vi.fn(async () => {
        throw new RuntimeError('Could not save the setting. Try again.')
      })
    })

    await user.click(await screen.findByRole('switch', {name: 'Check for updates on startup'}))

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not save the setting. Try again.')
    expect(startupSwitch()).toBeChecked()
  })

  test('is marked busy while the change is saved', async () => {
    const user = userEvent.setup()
    let finish
    await renderWith(updateSnapshot({checksEnabled: true}), {
      setChecksEnabled: vi.fn(() => new Promise((resolve) => (finish = () => resolve(updateSnapshot({checksEnabled: false})))))
    })

    await user.click(await screen.findByRole('switch', {name: 'Check for updates on startup'}))
    expect(startupSwitch()).toHaveAttribute('aria-busy', 'true')

    finish()
    await waitFor(() => expect(startupSwitch()).toHaveAttribute('aria-busy', 'false'))
  })
})

describe('UpdateSettings Check now', () => {
  test.each([
    ['checking', updateSnapshot({phase: 'checking'})],
    ['downloading', updateSnapshot({phase: 'downloading', latest: latestRelease()})],
    ['verifying', updateSnapshot({phase: 'verifying', latest: latestRelease()})],
    ['installing', updateSnapshot({phase: 'installing', latest: latestRelease()})],
    ['ready', updateSnapshot({phase: 'ready', latest: latestRelease()})],
    ['restarting', updateSnapshot({phase: 'restarting', latest: latestRelease()})]
  ])('is aria-disabled while %s', async (_name, snapshot) => {
    await renderWith(snapshot)

    expect(await screen.findByRole('button', {name: 'Check now'})).toHaveAttribute('aria-disabled', 'true')
  })

  test.each([
    ['idle', updateSnapshot({phase: 'idle'})],
    ['up-to-date', updateSnapshot({phase: 'up-to-date'})],
    ['available', availableSnapshot()],
    ['error', updateSnapshot({phase: 'error', error: 'Could not check for updates. Try again later.'})]
  ])('is enabled when %s', async (_name, snapshot) => {
    await renderWith(snapshot)

    expect(await screen.findByRole('button', {name: 'Check now'})).toHaveAttribute('aria-disabled', 'false')
  })

  test('runs a manual check when clicked', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(updateSnapshot({phase: 'idle'}))

    await user.click(await screen.findByRole('button', {name: 'Check now'}))

    expect(api.check).toHaveBeenCalledOnce()
  })

  test('a click while locked runs nothing', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(updateSnapshot({phase: 'downloading', latest: latestRelease()}))

    await user.click(await screen.findByRole('button', {name: 'Check now'}))

    expect(api.check).not.toHaveBeenCalled()
  })

  test('keeps focus on Check now while its check runs, and a second click runs nothing', async () => {
    const user = userEvent.setup()
    let finish
    const {api} = await renderWith(updateSnapshot({phase: 'idle'}), {
      check: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })

    await user.click(await screen.findByRole('button', {name: 'Check now'}))
    expect(checkNow()).toHaveAttribute('aria-disabled', 'true')
    expect(checkNow()).toHaveFocus()

    await user.click(checkNow())
    expect(api.check).toHaveBeenCalledOnce()

    finish()
    await waitFor(() => expect(checkNow()).toHaveAttribute('aria-disabled', 'false'))
    expect(checkNow()).toHaveFocus()
  })

  test('Check now stays enabled while the startup switch saves', async () => {
    const user = userEvent.setup()
    let finish
    const {api} = await renderWith(updateSnapshot({phase: 'idle', checksEnabled: true}), {
      setChecksEnabled: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })

    await user.click(await screen.findByRole('switch', {name: 'Check for updates on startup'}))
    expect(startupSwitch()).toHaveAttribute('aria-busy', 'true')
    expectCheckNowLocked(false)

    await user.click(checkNow())
    expect(api.check).toHaveBeenCalledOnce()

    finish(updateSnapshot({checksEnabled: false}))
    await waitFor(() => expect(startupSwitch()).toHaveAttribute('aria-busy', 'false'))
  })

  test('the startup switch stays usable while a check runs', async () => {
    const user = userEvent.setup()
    let finish
    const {api} = await renderWith(updateSnapshot({phase: 'idle', checksEnabled: true}), {
      check: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })

    await user.click(await screen.findByRole('button', {name: 'Check now'}))
    expect(checkNow()).toHaveAttribute('aria-disabled', 'true')

    await user.click(startupSwitch())
    expect(api.setChecksEnabled).toHaveBeenCalledWith(false)

    finish()
  })

  test('a rejected check shows its sentence in the section', async () => {
    const user = userEvent.setup()
    await renderWith(updateSnapshot({phase: 'idle'}), {
      check: vi.fn(async () => {
        throw new RuntimeError('Could not check for updates. Try again later.')
      })
    })

    await user.click(await screen.findByRole('button', {name: 'Check now'}))

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not check for updates. Try again later.')
  })
})

describe('UpdateSettings status line', () => {
  // Phases the banner shows. Settings stays silent for them (the banner speaks), even when this test renders Settings alone.
  const bannerPhases = new Set(['available', 'downloading', 'verifying', 'installing', 'ready', 'restarting'])

  test.each([
    ['checking', updateSnapshot({phase: 'checking'}), 'Checking for updates…'],
    ['up-to-date', updateSnapshot({phase: 'up-to-date'}), 'You have the latest version.'],
    ['available', availableSnapshot(), 'Version 1.2.0 is available.'],
    ['downloading', updateSnapshot({phase: 'downloading', latest: latestRelease()}), 'Downloading update…'],
    ['verifying', updateSnapshot({phase: 'verifying', latest: latestRelease()}), 'Verifying download…'],
    ['installing', updateSnapshot({phase: 'installing', latest: latestRelease()}), 'Installing update…'],
    ['ready', updateSnapshot({phase: 'ready', latest: latestRelease()}), 'Version 1.2.0 is ready. Restart to finish updating.'],
    ['restarting', updateSnapshot({phase: 'restarting', latest: latestRelease()}), 'Restarting…']
  ])('words the %s phase', async (_name, snapshot, line) => {
    await renderWith(snapshot)

    // The visible line is plain text. The announcer repeats it only when the banner is hidden.
    expect(await screen.findByText(line, visible)).toHaveClass('update-status')
    expect(screen.getByRole('status').textContent).toBe(bannerPhases.has(snapshot.phase) ? '' : line)
  })

  test('a dismissed release says how to show it again', async () => {
    const {store} = await renderWith(availableSnapshot())

    store.later()

    expect(await screen.findByText('Version 1.2.0 is available. Press Check now to show it again.', visible)).toBeInTheDocument()
    expect(screen.getByRole('status').textContent).toBe('Version 1.2.0 is available. Press Check now to show it again.')
  })

  test('with the banner on screen, only the banner announces the phase', async () => {
    const loaded = await loadedStore(updateSnapshot({phase: 'downloading', latest: latestRelease()}))
    render(UpdateBanner, {props: {updates: loaded.store}})
    render(UpdateSettings, {props: {updates: loaded.store}})
    await flush()

    const announcers = screen.getAllByRole('status')
    expect(announcers).toHaveLength(2)
    const spoken = announcers.filter((el) => el.textContent !== '')
    expect(spoken).toHaveLength(1)
    expect(spoken[0].textContent).toBe('Downloading version 1.2.0.')
    expect(screen.getByRole('region', {name: 'Application update'}).parentElement.contains(spoken[0])).toBe(true)
    // Settings still shows the phase line, but not as a live region.
    expect(screen.getByText('Downloading update…', visible)).toHaveClass('update-status')
  })

  test('an error the banner shows is not repeated in Settings as a second alert', async () => {
    const loaded = await loadedStore(updateSnapshot({phase: 'error', error: 'Could not check for updates. Try again later.'}))
    render(UpdateBanner, {props: {updates: loaded.store}})
    render(UpdateSettings, {props: {updates: loaded.store}})
    await flush()

    expect(screen.getByRole('region', {name: 'Application update'})).toHaveTextContent('Could not check for updates. Try again later.')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getAllByText('Could not check for updates. Try again later.')).toHaveLength(1)
  })

  test('an error the banner does not show is an alert in Settings', async () => {
    const user = userEvent.setup()
    await renderWith(updateSnapshot({phase: 'idle'}), {
      check: vi.fn(async () => {
        throw new RuntimeError('Could not check for updates. Try again later.')
      })
    })

    await user.click(await screen.findByRole('button', {name: 'Check now'}))

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not check for updates. Try again later.')
  })
})

describe('UpdateSettings locks while a call is in flight', () => {
  test('Check now is disabled while an install call is in flight', async () => {
    let finish
    const {store} = await renderWith(availableSnapshot(), {
      install: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })

    store.install('1.2.0')

    await waitFor(() => expect(checkNow()).toHaveAttribute('aria-disabled', 'true'))
    finish()
    await waitFor(() => expect(checkNow()).toHaveAttribute('aria-disabled', 'false'))
  })

  test('a click on the startup switch during a save does not flip the box', async () => {
    const user = userEvent.setup()
    let finish
    const {api} = await renderWith(updateSnapshot({checksEnabled: true}), {
      setChecksEnabled: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })

    await user.click(await screen.findByRole('switch', {name: 'Check for updates on startup'}))
    expect(startupSwitch()).not.toBeChecked()
    await user.click(startupSwitch())

    expect(api.setChecksEnabled).toHaveBeenCalledOnce()
    expect(startupSwitch()).not.toBeChecked()
    finish(updateSnapshot({checksEnabled: false}))
    await waitFor(() => expect(startupSwitch()).toHaveAttribute('aria-busy', 'false'))
  })
})
