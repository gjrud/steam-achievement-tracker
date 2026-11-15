import {cleanup, fireEvent, render, screen} from '@testing-library/svelte'
import {tick} from 'svelte'
import {afterEach, beforeEach, describe, expect, test, vi} from 'vitest'
import ProfileCard from './ProfileCard.svelte'
import {formatDateTime} from '../format.js'

const MINUTE = 60 * 1000
const HOUR = 60 * MINUTE

const profile = {displayName: 'Gjrud', avatarUrl: ''}
const finishedRun = {
  status: 'success',
  startedAt: '2026-05-22T10:00:00Z',
  finishedAt: '2026-05-22T10:05:00Z',
  gamesSynced: 6,
  gamesTotal: 6,
  gamesFailed: 0
}
const runningRun = {...finishedRun, status: 'running', finishedAt: '', gamesSynced: 2}
// Two hours after finishedRun ended, so its time reads "2 hours ago" and the running run's start reads "Started 2 hours ago".
const NOW = Date.parse(finishedRun.finishedAt) + 2 * HOUR

beforeEach(() => {
  // Fake the clock and the refresh interval so relative times are deterministic.
  vi.useFakeTimers({toFake: ['Date', 'setInterval', 'clearInterval']})
  vi.setSystemTime(NOW)
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

function renderCard(props = {}) {
  return render(ProfileCard, {
    props: {state: {profile, syncInProgress: false}, latestSyncRun: finishedRun, syncIsRunning: false, onSyncNow: vi.fn(), ...props}
  })
}

const card = () => document.querySelector('.profile-card')
const band = () => document.querySelector('.sync-band')
const status = () => screen.getByRole('status')

describe('ProfileCard', () => {
  test('shows the display name and a fallback letter when there is no avatar', () => {
    renderCard()

    expect(screen.getByText('Gjrud')).toBeInTheDocument()
    expect(screen.getByText('G')).toBeInTheDocument()
    expect(document.querySelector('.profile-card img')).toBeNull()
  })

  test('shows the avatar image when the profile has one', () => {
    renderCard({state: {profile: {displayName: 'Gjrud', avatarUrl: 'https://example.test/a.jpg'}, syncInProgress: false}})

    expect(document.querySelector('.profile-card img')).toHaveAttribute('src', 'https://example.test/a.jpg')
    expect(screen.queryByText('G')).not.toBeInTheDocument()
  })

  test('runs onSyncNow from the Sync now button', async () => {
    const onSyncNow = vi.fn()
    renderCard({onSyncNow})

    await fireEvent.click(screen.getByRole('button', {name: 'Sync now'}))

    expect(onSyncNow).toHaveBeenCalledOnce()
  })

  test('disables the sync button while a sync is in progress', () => {
    renderCard({state: {profile, syncInProgress: true}, syncIsRunning: true})

    expect(screen.getByRole('button', {name: 'Syncing…'})).toBeDisabled()
  })

  test('shows a successful sync as an all-clear band with its counts and time', () => {
    renderCard()

    expect(card()).toHaveClass('tone-ok')
    expect(screen.getByText('All games synced')).toBeInTheDocument()
    expect(band()).toHaveTextContent('6 / 6 games · 2 hours ago')
    expect(status()).toHaveTextContent('Last sync: All games synced')
  })

  test('dates the band with a time element carrying the ISO time and the absolute date as its title', () => {
    renderCard()

    const time = band().querySelector('time')
    expect(time).toHaveAttribute('datetime', finishedRun.finishedAt)
    expect(time).toHaveAttribute('title', formatDateTime(finishedRun.finishedAt))
    expect(time).toHaveTextContent('2 hours ago')
  })

  test('keeps the band free of links and controls, and off the live region', () => {
    renderCard({latestSyncRun: {...finishedRun, status: 'partial', gamesSynced: 3, gamesFailed: 3}})

    expect(band().querySelectorAll('a, button')).toHaveLength(0)
    expect(document.querySelector('[aria-live]')).toBeNull()
  })

  test('shows a partial sync as a warning with its failure count', () => {
    renderCard({latestSyncRun: {...finishedRun, status: 'partial', gamesSynced: 3, gamesFailed: 3}})

    expect(card()).toHaveClass('tone-warn')
    expect(screen.getByText('Partial sync · 3 failed')).toBeInTheDocument()
    expect(band()).toHaveTextContent('3 / 6 games synced · 2 hours ago')
    expect(status()).toHaveTextContent('Last sync: Partial sync, 3 failed')
  })

  test('shows a failed sync in its own red tone, with the error count when there are errors', () => {
    renderCard({latestSyncRun: {...finishedRun, status: 'failed', gamesSynced: 0, gamesFailed: 6}})

    expect(card()).toHaveClass('tone-fail')
    expect(card()).not.toHaveClass('tone-warn')
    expect(screen.getByText('Sync failed · 6 errors')).toBeInTheDocument()
    expect(band()).toHaveTextContent('0 / 6 games synced · 2 hours ago')
  })

  test('shows a failed sync without a count when nothing is recorded as failed', () => {
    renderCard({latestSyncRun: {...finishedRun, status: 'failed', gamesSynced: 0, gamesFailed: 0}})

    expect(screen.getByText('Sync failed')).toBeInTheDocument()
  })

  test('shows a canceled sync as neutral, not as success', () => {
    renderCard({latestSyncRun: {...finishedRun, status: 'canceled', gamesSynced: 4}})

    expect(card()).toHaveClass('tone-none')
    expect(card()).not.toHaveClass('tone-ok')
    expect(screen.getByText('Sync canceled')).toBeInTheDocument()
    expect(screen.queryByText('All games synced')).not.toBeInTheDocument()
    expect(band()).toHaveTextContent('4 / 6 games synced · 2 hours ago')
  })

  test('shows running progress with a spinner that is hidden from assistive tech', () => {
    renderCard({syncIsRunning: true, latestSyncRun: runningRun})

    expect(card()).toHaveClass('tone-run')
    expect(screen.getByText('Syncing 2 / 6')).toBeInTheDocument()
    expect(band()).toHaveTextContent('Started 2 hours ago')
    expect(band().querySelector('time')).toHaveAttribute('datetime', runningRun.startedAt)
    expect(band().querySelector('svg.spin')).toHaveAttribute('aria-hidden', 'true')
    expect(status()).toHaveTextContent('Current sync: Sync running')
  })

  test('says a sync is starting before its run record arrives, with no time', () => {
    renderCard({syncIsRunning: true, latestSyncRun: null})

    expect(card()).toHaveClass('tone-run')
    expect(screen.getByText('Sync starting')).toBeInTheDocument()
    expect(band()).toHaveTextContent('Starting sync…')
    expect(band().querySelector('time')).toBeNull()
    expect(status()).toHaveTextContent('Current sync: Sync starting')
  })

  test('shows starting, not the previous finished run, when Sync now is pressed', () => {
    renderCard({syncIsRunning: true, latestSyncRun: finishedRun})

    expect(card()).toHaveClass('tone-run')
    expect(screen.getByText('Sync starting')).toBeInTheDocument()
    expect(band()).toHaveTextContent('Starting sync…')
    expect(band()).not.toHaveTextContent('All games synced')
    expect(band()).not.toHaveTextContent('2 hours ago')
    expect(band().querySelector('time')).toBeNull()
    expect(status()).toHaveTextContent('Current sync: Sync starting')
  })

  test('says no sync has run yet when there is no run record, with no time', () => {
    renderCard({latestSyncRun: null})

    expect(card()).toHaveClass('tone-none')
    expect(band().querySelector('.sync-headline')).toHaveTextContent(/^No sync yet$/)
    expect(band()).toHaveTextContent('Press Sync now to fetch your library')
    expect(band().querySelector('time')).toBeNull()
    expect(status()).toHaveTextContent('No sync yet')
  })

  test('drops the counts from a finished run with no games and keeps its time', () => {
    renderCard({latestSyncRun: {...finishedRun, status: 'failed', gamesSynced: 0, gamesTotal: 0, gamesFailed: 0}})

    expect(screen.getByText('Sync failed')).toBeInTheDocument()
    expect(band()).not.toHaveTextContent('0 / 0')
    expect(band().querySelector('time')).toHaveTextContent('2 hours ago')
  })

  test('shows one error with the singular in the failed headline', () => {
    renderCard({latestSyncRun: {...finishedRun, status: 'failed', gamesSynced: 0, gamesFailed: 1}})

    expect(screen.getByText('Sync failed · 1 error')).toBeInTheDocument()
    expect(status()).toHaveTextContent('Last sync: Sync failed, 1 error')
  })

  test.each([
    ['running', {syncIsRunning: true, latestSyncRun: {...finishedRun, status: 'running', finishedAt: ''}}, 'spin'],
    ['starting', {syncIsRunning: true, latestSyncRun: null}, 'spin'],
    ['success', {latestSyncRun: finishedRun}, 'check'],
    ['partial', {latestSyncRun: {...finishedRun, status: 'partial', gamesFailed: 1}}, 'warn'],
    ['failed', {latestSyncRun: {...finishedRun, status: 'failed', gamesFailed: 1}}, 'warn'],
    ['canceled', {latestSyncRun: {...finishedRun, status: 'canceled'}}, 'stop'],
    ['unknown status', {latestSyncRun: {...finishedRun, status: 'weird'}}, 'stop'],
    ['no sync yet', {latestSyncRun: null}, 'clock']
  ])('draws the %s state with the %s icon', (_name, props, icon) => {
    renderCard(props)

    const svgs = band().querySelectorAll('svg[data-icon]')
    expect(svgs).toHaveLength(1)
    expect(svgs[0]).toHaveAttribute('data-icon', icon)
  })

  test('does not draw the clock icon for a canceled sync', () => {
    renderCard({latestSyncRun: {...finishedRun, status: 'canceled', gamesSynced: 4}})

    expect(band().querySelector('svg[data-icon="clock"]')).toBeNull()
    expect(band().querySelector('svg[data-icon="stop"]')).not.toBeNull()
  })

  test('changes the live status only on a real state change, not as counts tick', async () => {
    const {rerender} = renderCard({syncIsRunning: true, latestSyncRun: runningRun})
    const announced = status().textContent

    await rerender({latestSyncRun: {...runningRun, gamesSynced: 4}})
    await tick()
    expect(screen.getByText('Syncing 4 / 6')).toBeInTheDocument()
    expect(status().textContent).toBe(announced)

    await rerender({syncIsRunning: false, latestSyncRun: finishedRun})
    await tick()
    expect(status()).toHaveTextContent('Last sync: All games synced')
  })

  test('re-words the relative time every 30 seconds, not sooner', async () => {
    renderCard()
    expect(band().querySelector('time')).toHaveTextContent('2 hours ago')

    vi.setSystemTime(NOW + HOUR)
    vi.advanceTimersByTime(29 * 1000)
    await tick()
    expect(band().querySelector('time')).toHaveTextContent('2 hours ago')

    vi.advanceTimersByTime(1000)
    await tick()
    expect(band().querySelector('time')).toHaveTextContent('3 hours ago')
  })

  test('stops the refresh timer when the card is removed', () => {
    renderCard()
    expect(vi.getTimerCount()).toBe(1)

    cleanup()

    expect(vi.getTimerCount()).toBe(0)
  })

  test.each([
    ['running', {syncIsRunning: true, latestSyncRun: {...finishedRun, status: 'running', finishedAt: ''}}, 'run'],
    ['starting', {syncIsRunning: true, latestSyncRun: null}, 'run'],
    ['success', {latestSyncRun: finishedRun}, 'ok'],
    ['partial', {latestSyncRun: {...finishedRun, status: 'partial', gamesFailed: 1}}, 'warn'],
    ['failed', {latestSyncRun: {...finishedRun, status: 'failed', gamesFailed: 1}}, 'fail'],
    ['canceled', {latestSyncRun: {...finishedRun, status: 'canceled'}}, 'none'],
    ['no sync yet', {latestSyncRun: null}, 'none']
  ])('marks the card with the tone for %s', (_name, props, tone) => {
    renderCard(props)

    expect(card()).toHaveClass(`tone-${tone}`)
  })
})
