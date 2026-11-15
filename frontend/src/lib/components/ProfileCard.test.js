import {cleanup, fireEvent, render, screen} from '@testing-library/svelte'
import {afterEach, describe, expect, test, vi} from 'vitest'
import ProfileCard from './ProfileCard.svelte'
import {syncDetails, syncTitle} from '../sync.js'
import {formatDateTime} from '../format.js'

afterEach(() => {
  cleanup()
})

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

function renderCard(props = {}) {
  return render(ProfileCard, {
    props: {state: {profile, syncInProgress: false}, latestSyncRun: finishedRun, syncIsRunning: false, onSyncNow: vi.fn(), ...props}
  })
}

function syncBlock() {
  return screen.getByText(/^(Current sync|Last sync)$/).closest('[aria-live]')
}

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

  test('shows the last finished sync with its status and game counts', () => {
    renderCard()

    const block = syncBlock()
    expect(block).toHaveAttribute('aria-live', 'polite')
    expect(screen.getByText('Last sync')).toBeInTheDocument()
    expect(block.querySelector('.sync-line')).toHaveTextContent(syncTitle(finishedRun))
    expect(block.querySelector('.sync-line')).not.toHaveTextContent(formatDateTime(finishedRun.finishedAt))
    expect(block.querySelector('.sync-time')).toHaveTextContent(formatDateTime(finishedRun.finishedAt))
    expect(block.querySelector('.sync-detail:not(.sync-time)')).toHaveTextContent('6/6 games synced, 0 failed')
    expect(block.querySelector('.sync-detail:not(.sync-time)')).not.toHaveTextContent(formatDateTime(finishedRun.finishedAt))
  })

  test('shows the date and time once, on its own line under the status', () => {
    renderCard()

    const block = syncBlock()
    const date = formatDateTime(finishedRun.finishedAt)
    expect(block.textContent.split(date)).toHaveLength(2)
    const lines = Array.from(block.querySelectorAll('.sync-line, .sync-detail')).map((el) => el.textContent.trim())
    // The first line is the visually hidden "Last sync" label followed by the status.
    expect(lines).toEqual(['Last sync Success', date, '6/6 games synced, 0 failed'])
  })

  test('shows running progress as the current sync', () => {
    renderCard({syncIsRunning: true, latestSyncRun: runningRun})

    const block = syncBlock()
    expect(block).toHaveAttribute('aria-live', 'polite')
    expect(screen.getByText('Current sync')).toBeInTheDocument()
    expect(screen.queryByText('Last sync')).not.toBeInTheDocument()
    expect(block.querySelector('.sync-line')).toHaveTextContent('Sync running')
    expect(block.querySelector('.sync-time')).toHaveTextContent(`Started ${formatDateTime(runningRun.startedAt)}`)
    expect(block).toHaveTextContent('2/6 games synced, 0 failed')
  })

  test('says a sync is starting before its run record arrives, with no date line', () => {
    renderCard({syncIsRunning: true, latestSyncRun: null})

    const block = syncBlock()
    expect(screen.getByText('Current sync')).toBeInTheDocument()
    expect(block.querySelector('.sync-line')).toHaveTextContent('Sync starting')
    expect(block.querySelector('.sync-time')).toBeNull()
    expect(block).toHaveTextContent('Starting sync…')
  })

  test('says no sync has run yet when there is no run record, with no date line', () => {
    renderCard({latestSyncRun: null})

    const block = syncBlock()
    expect(screen.getByText('Last sync')).toBeInTheDocument()
    expect(block.querySelector('.sync-line')).toHaveTextContent('No sync yet')
    expect(block.querySelector('.sync-time')).toBeNull()
    expect(block).toHaveTextContent(syncDetails(null))
  })

  test.each([
    ['running', {syncIsRunning: true, latestSyncRun: {...finishedRun, status: 'running', finishedAt: ''}}, 'run'],
    ['starting', {syncIsRunning: true, latestSyncRun: null}, 'run'],
    ['success', {latestSyncRun: finishedRun}, 'ok'],
    ['partial', {latestSyncRun: {...finishedRun, status: 'partial'}}, 'warn'],
    ['failed', {latestSyncRun: {...finishedRun, status: 'failed'}}, 'warn'],
    ['no sync yet', {latestSyncRun: null}, 'none']
  ])('marks the status dot for %s', (_name, props, tone) => {
    renderCard(props)

    expect(syncBlock().querySelector('.sync-dot')).toHaveClass(tone)
  })
})
