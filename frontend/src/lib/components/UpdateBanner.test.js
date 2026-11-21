import {get} from 'svelte/store'
import {cleanup, render, screen, waitFor} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import UpdateBanner from './UpdateBanner.svelte'
import {createUpdateStore} from '../update.js'
import {availableSnapshot, fakeUpdateApi, flush, latestRelease, loadedStore, releaseURL, updateSnapshot} from '../../test/update-fixtures.js'
import {__emitWailsEvent, __openedUrls, Browser, RuntimeError} from '../../test/wails-runtime-mock.js'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const MB = 1024 * 1024

// visible skips the announcer's copy, which repeats the banner text in a screen-reader-only region.
const visible = {ignore: '.sr-only'}

function banner() {
  return screen.queryByRole('region', {name: 'Application update'})
}

// announcer is the banner's one persistent status region. Visible copy is not a live region of its own, so a message
// is announced once, through this region.
function announcer() {
  return screen.getByRole('status')
}

// renderWith loads the store with snapshot, renders the banner over it and returns the parts a test needs.
async function renderWith(snapshot, overrides = {}) {
  const loaded = await loadedStore(snapshot, overrides)
  render(UpdateBanner, {props: {updates: loaded.store}})
  await flush()
  return loaded
}

describe('UpdateBanner phases', () => {
  test('available offers Install, Later and the release notes link', async () => {
    const {api} = await renderWith(availableSnapshot())

    expect(await screen.findByText('Version 1.2.0 is available', visible)).toBeInTheDocument()
    expect(screen.getByText('You have version 1.1.0. Download size 50.0 MB.', visible)).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Install'})).toBeEnabled()
    expect(screen.getByRole('button', {name: 'Later'})).toBeInTheDocument()
    expect(screen.getByRole('link', {name: 'Release notes'})).toHaveAttribute('href', releaseURL)
    expect(api.install).not.toHaveBeenCalled()
  })

  test('Install passes the version on screen', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(availableSnapshot())

    await user.click(await screen.findByRole('button', {name: 'Install'}))

    expect(api.install).toHaveBeenCalledOnce()
    expect(api.install).toHaveBeenCalledWith('1.2.0')
  })

  test('Later hides the banner until a new version', async () => {
    const user = userEvent.setup()
    const {setSnapshot} = await renderWith(availableSnapshot())

    await user.click(await screen.findByRole('button', {name: 'Later'}))
    expect(banner()).not.toBeInTheDocument()

    // The same version, re-read after an update:changed, stays dismissed.
    __emitWailsEvent('update:changed')
    await flush()
    expect(banner()).not.toBeInTheDocument()

    setSnapshot(availableSnapshot({latest: latestRelease({version: '1.3.0', releaseURL: 'https://github.com/gjrud/steam-achievement-tracker/releases/tag/v1.3.0'})}))
    __emitWailsEvent('update:changed')
    expect(await screen.findByText('Version 1.3.0 is available', visible)).toBeInTheDocument()
  })

  test('notify-only offers no Install, shows the reason and the release link', async () => {
    const user = userEvent.setup()
    await renderWith(availableSnapshot({notifyOnly: true, notifyReason: 'This build is updated from the release page.'}))

    expect(await screen.findByText('This build is updated from the release page.', visible)).toBeInTheDocument()
    // The reason is the message of this phase, so the announcement carries it with the title.
    await waitFor(() => expect(announcer().textContent).toBe('Version 1.2.0 is available. This build is updated from the release page.'))
    expect(screen.queryByRole('button', {name: 'Install'})).not.toBeInTheDocument()
    expect(screen.getByRole('link', {name: 'Release page'})).toBeInTheDocument()
    expect(screen.queryByRole('link', {name: 'Release notes'})).not.toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Later'})).toBeInTheDocument()

    await user.click(screen.getByRole('link', {name: 'Release page'}))
    expect(__openedUrls()).toEqual([releaseURL])
  })

  test('downloading with a known total shows a percentage and a determinate meter', async () => {
    await renderWith(updateSnapshot({phase: 'downloading', latest: latestRelease(), progress: {written: 50 * MB, total: 200 * MB}}))

    expect(await screen.findByText('Downloading version 1.2.0', visible)).toBeInTheDocument()
    expect(screen.getByText('50.0 MB of 200 MB (25%)', visible)).toBeInTheDocument()
    const meter = screen.getByRole('progressbar', {name: 'Download progress'})
    expect(meter).toHaveAttribute('aria-valuenow', '25')
    expect(meter).toHaveAttribute('aria-valuemin', '0')
    expect(meter).toHaveAttribute('aria-valuemax', '100')
    expect(meter).not.toHaveClass('indeterminate')
    expect(screen.getByRole('button', {name: 'Cancel'})).toBeEnabled()
  })

  test('downloading with an unknown total is indeterminate', async () => {
    await renderWith(updateSnapshot({phase: 'downloading', latest: latestRelease(), progress: {written: 3355443, total: 0}}))

    expect(await screen.findByText('3.2 MB downloaded', visible)).toBeInTheDocument()
    const meter = screen.getByRole('progressbar', {name: 'Download progress'})
    expect(meter).toHaveClass('indeterminate')
    expect(meter).not.toHaveAttribute('aria-valuenow')
  })

  test('downloading before any byte shows the preparing line', async () => {
    await renderWith(updateSnapshot({phase: 'downloading', latest: latestRelease()}))

    expect(await screen.findByText('Preparing download…', visible)).toBeInTheDocument()
  })

  test('Cancel calls CancelUpdate and reads Canceling… while the download unwinds', async () => {
    const user = userEvent.setup()
    let release
    const {api} = await renderWith(updateSnapshot({phase: 'downloading', latest: latestRelease()}), {
      cancel: vi.fn(() => new Promise((resolve) => (release = resolve)))
    })

    await user.click(await screen.findByRole('button', {name: 'Cancel'}))

    expect(api.cancel).toHaveBeenCalledOnce()
    const canceling = await screen.findByRole('button', {name: 'Canceling…'})
    expect(canceling).toBeDisabled()
    expect(screen.getByText('Downloading version 1.2.0', visible)).toHaveFocus()
    release()
  })

  test('verifying shows its title and a meter, with no buttons', async () => {
    await renderWith(updateSnapshot({phase: 'verifying', latest: latestRelease()}))

    expect(await screen.findByText('Verifying download…', visible)).toBeInTheDocument()
    expect(screen.getByRole('progressbar', {name: 'Update progress'})).toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  test('installing shows its title and a meter, with no buttons', async () => {
    await renderWith(updateSnapshot({phase: 'installing', latest: latestRelease()}))

    expect(await screen.findByText('Installing update…', visible)).toBeInTheDocument()
    expect(screen.getByRole('progressbar', {name: 'Update progress'})).toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  test('ready offers only Restart now, with no Later and no release link', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(updateSnapshot({phase: 'ready', latest: latestRelease()}))

    expect(await screen.findByText('Version 1.2.0 is ready. Restart to finish updating.', visible)).toBeInTheDocument()
    expect(screen.getByText('Restarting closes the app and cancels a running sync. The update then applies.', visible)).toBeInTheDocument()
    const buttons = screen.getAllByRole('button')
    expect(buttons.map((button) => button.textContent)).toEqual(['Restart now'])
    expect(screen.queryByRole('link', {name: 'Release notes'})).not.toBeInTheDocument()
    expect(screen.queryByRole('link', {name: 'Release page'})).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Restart now'}))
    expect(api.restart).toHaveBeenCalledOnce()
  })

  test('a failed restart shows its sentence in the status and keeps Restart now', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(updateSnapshot({phase: 'ready', latest: latestRelease()}), {
      restart: vi.fn(async () => {
        throw new RuntimeError('Could not restart. The update stays ready.')
      })
    })

    await user.click(await screen.findByRole('button', {name: 'Restart now'}))

    expect(api.restart).toHaveBeenCalledOnce()
    await waitFor(() => expect(announcer()).toHaveTextContent('Could not restart. The update stays ready.'))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByText('Could not restart. The update stays ready.', visible)).toBeInTheDocument()
    expect(screen.getByText('Version 1.2.0 is ready. Restart to finish updating.', visible)).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Restart now'})).toBeEnabled()
  })

  test('restarting shows its title, an indeterminate meter and the wait note, with no buttons', async () => {
    await renderWith(updateSnapshot({phase: 'restarting', latest: latestRelease()}))

    expect(await screen.findByText('Restarting…', visible)).toBeInTheDocument()
    expect(screen.getByText('This can take up to 30 seconds.', visible)).toBeInTheDocument()
    const meter = screen.getByRole('progressbar', {name: 'Update progress'})
    expect(meter).toHaveClass('indeterminate')
    expect(meter).not.toHaveAttribute('aria-valuenow')
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  test('a failed check shows the check title, its sentence in the status and Try again', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(updateSnapshot({phase: 'error', error: 'Could not check for updates. Try again later.'}))

    expect(await screen.findByText('Could not check for updates', visible)).toBeInTheDocument()
    await waitFor(() => expect(announcer()).toHaveTextContent('Could not check for updates. Try again later.'))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Install'})).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Try again'}))
    expect(api.check).toHaveBeenCalledOnce()
    expect(api.install).not.toHaveBeenCalled()
  })

  test('a failed download names the version, offers the release link and retries the install', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(
      updateSnapshot({phase: 'error', latest: latestRelease(), error: 'Could not download the update. Try again later.'})
    )

    expect(await screen.findByText('Could not update to version 1.2.0', visible)).toBeInTheDocument()
    await waitFor(() => expect(announcer()).toHaveTextContent('Could not download the update. Try again later.'))
    expect(screen.getByRole('link', {name: 'Release page'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Try again'}))
    expect(api.install).toHaveBeenCalledWith('1.2.0')
    expect(api.check).not.toHaveBeenCalled()
  })

  test('a folder that is not writable shows its sentence and the release link', async () => {
    await renderWith(
      updateSnapshot({
        phase: 'error',
        latest: latestRelease(),
        error: 'The app folder is not writable, so the update cannot be installed. Download it from the release page.'
      })
    )

    await waitFor(() => expect(announcer()).toHaveTextContent('The app folder is not writable'))
    expect(screen.getByRole('link', {name: 'Release page'})).toHaveAttribute('href', releaseURL)
  })

  test('an error with no release retries the check, and the title says so', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(updateSnapshot({phase: 'error', error: 'Could not check for updates. Try again later.'}))

    expect(await screen.findByText('Could not check for updates', visible)).toBeInTheDocument()
    await user.click(screen.getByRole('button', {name: 'Try again'}))

    expect(api.check).toHaveBeenCalledOnce()
    expect(api.install).not.toHaveBeenCalled()
  })

  test('a failed check with a release on record keeps no release link and retries the check', async () => {
    const user = userEvent.setup()
    const {store, api, setSnapshot} = await loadedStore(updateSnapshot({phase: 'idle'}))
    // The check itself fails and the backend reports the error phase with the release it last saw.
    api.check.mockImplementationOnce(async () => {
      setSnapshot(updateSnapshot({phase: 'error', latest: latestRelease(), error: 'Could not check for updates. Try again later.'}))
      throw new RuntimeError('Could not check for updates. Try again later.')
    })
    await store.check()
    render(UpdateBanner, {props: {updates: store}})
    await flush()

    expect(await screen.findByText('Could not check for updates', visible)).toBeInTheDocument()
    expect(screen.queryByRole('link', {name: 'Release page'})).not.toBeInTheDocument()
    expect(screen.queryByRole('link', {name: 'Release notes'})).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Try again'}))
    expect(api.check).toHaveBeenCalledTimes(2)
    expect(api.install).not.toHaveBeenCalled()
  })

  test('a retry in flight keeps the failed-download title until its outcome is known', async () => {
    const user = userEvent.setup()
    let finish
    const {api} = await renderWith(updateSnapshot({phase: 'error', latest: latestRelease(), error: 'Could not download the update.'}), {
      install: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })

    await user.click(await screen.findByRole('button', {name: 'Try again'}))

    // The retry is still running and the phase is still error: the title must not flip to the check wording.
    expect(screen.getByText('Could not update to version 1.2.0', visible)).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Try again'})).toBeDisabled()
    expect(api.install).toHaveBeenCalledWith('1.2.0')

    finish()
  })

  test('no Later button while downloading or after an error', async () => {
    await renderWith(updateSnapshot({phase: 'downloading', latest: latestRelease()}))
    expect(await screen.findByText('Downloading version 1.2.0', visible)).toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Later'})).not.toBeInTheDocument()
    cleanup()

    await renderWith(updateSnapshot({phase: 'error', latest: latestRelease(), error: 'Could not download the update.'}))
    expect(await screen.findByText('Could not download the update.', visible)).toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Later'})).not.toBeInTheDocument()
  })

  test('a failed re-check on an available release keeps Install and Later under the sentence', async () => {
    await renderWith(availableSnapshot({error: 'Could not check for updates. Try again later.'}))

    expect(await screen.findByText('Version 1.2.0 is available', visible)).toBeInTheDocument()
    await waitFor(() => expect(announcer()).toHaveTextContent('Could not check for updates. Try again later.'))
    expect(screen.getByText('Could not check for updates. Try again later.', visible)).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Install'})).toBeEnabled()
    expect(screen.getByRole('button', {name: 'Later'})).toBeInTheDocument()
  })

  test('a rejected install from the available phase shows its sentence in the banner', async () => {
    const user = userEvent.setup()
    const {api} = await renderWith(availableSnapshot(), {
      install: vi.fn(async () => {
        throw new RuntimeError('Install is not available for this release.')
      })
    })

    await user.click(await screen.findByRole('button', {name: 'Install'}))

    await waitFor(() => expect(announcer()).toHaveTextContent('Install is not available for this release.'))
    expect(screen.getByText('Install is not available for this release.', visible)).toBeInTheDocument()
    expect(api.install).toHaveBeenCalledWith('1.2.0')
    expect(screen.getByText('Version 1.2.0 is available', visible)).toBeInTheDocument()
  })

  test('a finished install that replaced the release shows the notice', async () => {
    const user = userEvent.setup()
    const {setSnapshot} = await renderWith(availableSnapshot(), {
      install: vi.fn(async () => {
        setSnapshot(availableSnapshot({latest: latestRelease({version: '1.3.0'})}))
      })
    })

    await user.click(await screen.findByRole('button', {name: 'Install'}))

    expect(await screen.findByText('Version 1.3.0 is available now. Review it and install again.', visible)).toBeInTheDocument()
    expect(screen.getByText('Version 1.3.0 is available', visible)).toBeInTheDocument()
  })

  test('Release notes opens the release page through Browser.OpenURL', async () => {
    const user = userEvent.setup()
    await renderWith(availableSnapshot())

    await user.click(await screen.findByRole('link', {name: 'Release notes'}))

    expect(__openedUrls()).toEqual([releaseURL])
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('a failed Browser.OpenURL shows the URL in the status', async () => {
    const user = userEvent.setup()
    await renderWith(availableSnapshot())
    const opener = vi.spyOn(Browser, 'OpenURL').mockRejectedValueOnce(new Error('no browser'))

    await user.click(await screen.findByRole('link', {name: 'Release notes'}))

    expect(opener).toHaveBeenCalledWith(releaseURL)
    expect(screen.getByText(`Could not open the browser. Visit ${releaseURL} to read it.`, visible)).toBeInTheDocument()
    // The URL is not the last words of the announcement, so the sentence ends after "to read it." with no added full stop.
    await waitFor(() => expect(announcer().textContent).toBe(`Version 1.2.0 is available. Could not open the browser. Visit ${releaseURL} to read it.`))
  })
})

describe('UpdateBanner hidden states', () => {
  test.each([
    ['unavailable', updateSnapshot({unavailable: true, phase: 'available', latest: latestRelease()})],
    ['idle', updateSnapshot({phase: 'idle'})],
    ['checking', updateSnapshot({phase: 'checking'})],
    ['up-to-date', updateSnapshot({phase: 'up-to-date'})]
  ])('is not shown when %s', async (_name, snapshot) => {
    const {store} = await renderWith(snapshot)

    // The store has read its snapshot, so the hidden banner is the rule at work and not an unloaded store.
    expect(get(store).loaded).toBe(true)
    await flush()
    expect(banner()).not.toBeInTheDocument()
  })

  test('is not shown before the first read succeeds', async () => {
    const fake = fakeUpdateApi(availableSnapshot())
    fake.api.getState.mockRejectedValue(new Error('boom'))
    const store = createUpdateStore(fake.api)
    store.start()
    render(UpdateBanner, {props: {updates: store}})
    await flush()

    expect(get(store).loaded).toBe(false)
    expect(banner()).not.toBeInTheDocument()
  })

  test('is not shown without a store', () => {
    render(UpdateBanner, {props: {updates: null}})

    expect(banner()).not.toBeInTheDocument()
  })
})

describe('UpdateBanner checking hold', () => {
  test.each([
    ['an available release', availableSnapshot()],
    ['an error', updateSnapshot({phase: 'error', error: 'Could not check for updates. Try again later.'})]
  ])('a check that began over %s keeps the banner with no actions', async (_name, prior) => {
    const {setSnapshot} = await renderWith(prior)

    setSnapshot(updateSnapshot({phase: 'checking'}))
    __emitWailsEvent('update:changed')

    expect(await screen.findByText('Checking for updates…', visible)).toBeInTheDocument()
    expect(banner()).toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toHaveLength(0)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()

    setSnapshot(updateSnapshot({phase: 'up-to-date'}))
    __emitWailsEvent('update:changed')

    await waitFor(() => expect(banner()).not.toBeInTheDocument())
  })
})

describe('UpdateBanner focus and live region', () => {
  test('Install moves focus to the title when the download replaces the button', async () => {
    const user = userEvent.setup()
    let finish
    const {setSnapshot} = await renderWith(availableSnapshot(), {
      install: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })

    await user.click(await screen.findByRole('button', {name: 'Install'}))
    setSnapshot(updateSnapshot({phase: 'downloading', latest: latestRelease()}))
    __emitWailsEvent('update:changed')

    expect(await screen.findByText('Downloading version 1.2.0', visible)).toHaveFocus()
    finish()
  })

  test('Try again keeps focus on the title while the retry runs', async () => {
    const user = userEvent.setup()
    let finish
    await renderWith(updateSnapshot({phase: 'error', error: 'Could not check for updates. Try again later.'}), {
      check: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })

    await user.click(await screen.findByRole('button', {name: 'Try again'}))

    expect(screen.getByText('Could not check for updates', visible)).toHaveFocus()
    finish()
  })

  test('when a retry clears the banner, focus moves to the host rather than the removed title', async () => {
    const user = userEvent.setup()
    let finish
    const {setSnapshot} = await renderWith(updateSnapshot({phase: 'error', error: 'Could not check for updates. Try again later.'}), {
      check: vi.fn(() => new Promise((resolve) => (finish = resolve)))
    })
    const host = banner().parentElement

    await user.click(await screen.findByRole('button', {name: 'Try again'}))
    expect(screen.getByText('Could not check for updates', visible)).toHaveFocus()

    setSnapshot(updateSnapshot({phase: 'up-to-date'}))
    finish()

    await waitFor(() => expect(banner()).not.toBeInTheDocument())
    expect(document.activeElement).toBe(host)
  })

  test('Later moves focus to the host instead of dropping it to the body', async () => {
    const user = userEvent.setup()
    await renderWith(availableSnapshot())
    const host = banner().parentElement

    await user.click(await screen.findByRole('button', {name: 'Later'}))

    expect(banner()).not.toBeInTheDocument()
    expect(document.activeElement).toBe(host)
  })

  test('the host is not a live region; the status region is the one persistent announcer', async () => {
    const {setSnapshot} = await renderWith(availableSnapshot())
    const host = banner().parentElement

    expect(host).not.toHaveAttribute('aria-live')
    expect(host.querySelectorAll('[aria-live="polite"],[aria-live="assertive"]')).toHaveLength(0)
    expect(host.querySelectorAll('[role="alert"]')).toHaveLength(0)
    expect(announcer().parentElement).toBe(host)
    expect(announcer()).toHaveClass('sr-only')

    setSnapshot(updateSnapshot({phase: 'error', latest: latestRelease(), error: 'Could not download the update.'}))
    __emitWailsEvent('update:changed')
    await waitFor(() => expect(announcer()).toHaveTextContent('Could not update to version 1.2.0. Could not download the update.'))
  })

  test('download progress stays out of every live region, so only the title is announced', async () => {
    const {setSnapshot} = await renderWith(updateSnapshot({phase: 'downloading', latest: latestRelease(), progress: {written: 50 * MB, total: 200 * MB}}))
    const liveSelector = '[role="status"],[role="alert"],[aria-live="polite"],[aria-live="assertive"]'

    expect(announcer().textContent).toBe('Downloading version 1.2.0.')
    expect(screen.getByText('50.0 MB of 200 MB (25%)', visible).closest(liveSelector)).toBeNull()

    setSnapshot(updateSnapshot({phase: 'downloading', latest: latestRelease(), progress: {written: 100 * MB, total: 200 * MB}}))
    __emitWailsEvent('update:changed')

    expect(await screen.findByText('100 MB of 200 MB (50%)', visible)).toBeInTheDocument()
    expect(screen.getByText('100 MB of 200 MB (50%)', visible).closest(liveSelector)).toBeNull()
    expect(announcer().textContent).toBe('Downloading version 1.2.0.')
  })

  test('the status region stays mounted when the banner is hidden, so a later banner is announced', async () => {
    const user = userEvent.setup()
    const {setSnapshot} = await renderWith(availableSnapshot())
    const host = banner().parentElement
    const status = announcer()

    await user.click(await screen.findByRole('button', {name: 'Later'}))
    expect(status.isConnected).toBe(true)
    expect(announcer()).toBe(status)
    expect(status.textContent).toBe('')

    setSnapshot(updateSnapshot({phase: 'ready', latest: latestRelease()}))
    __emitWailsEvent('update:changed')

    expect(await screen.findByText('Version 1.2.0 is ready. Restart to finish updating.', visible)).toBeInTheDocument()
    expect(banner().parentElement).toBe(host)
    await waitFor(() => expect(announcer()).toHaveTextContent('Version 1.2.0 is ready. Restart to finish updating.'))
    expect(announcer()).toBe(status)
  })
})
