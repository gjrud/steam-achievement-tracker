import {cleanup, fireEvent, render, screen} from '@testing-library/svelte'
import {afterEach, describe, expect, test, vi} from 'vitest'
import ProfileCard from './ProfileCard.svelte'
import {syncDetails, syncTitle} from '../sync.js'

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
    expect(block).toHaveTextContent(syncTitle(finishedRun))
    expect(block).toHaveTextContent('6/6 games synced, 0 failed')
  })

  test('shows running progress as the current sync', () => {
    renderCard({
      syncIsRunning: true,
      latestSyncRun: {...finishedRun, status: 'running', finishedAt: '', gamesSynced: 2}
    })

    const block = syncBlock()
    expect(block).toHaveAttribute('aria-live', 'polite')
    expect(screen.getByText('Current sync')).toBeInTheDocument()
    expect(screen.queryByText('Last sync')).not.toBeInTheDocument()
    expect(block).toHaveTextContent('Sync running')
    expect(block).toHaveTextContent('2/6 games synced')
  })

  test('says a sync is starting before its run record arrives', () => {
    renderCard({syncIsRunning: true, latestSyncRun: null})

    const block = syncBlock()
    expect(screen.getByText('Current sync')).toBeInTheDocument()
    expect(block).toHaveTextContent('Sync starting')
    expect(block).toHaveTextContent('Starting sync…')
  })

  test('says no sync has run yet when there is no run record', () => {
    renderCard({latestSyncRun: null})

    const block = syncBlock()
    expect(screen.getByText('Last sync')).toBeInTheDocument()
    expect(block).toHaveTextContent('No sync yet')
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
