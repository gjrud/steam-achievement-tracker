import {cleanup, render, screen} from '@testing-library/svelte'
import {afterEach, describe, expect, test} from 'vitest'
import SummaryCards from './SummaryCards.svelte'
import {integer} from '../format.js'
import {syncDetails, syncTitle} from '../sync.js'

afterEach(() => {
  cleanup()
})

const summary = {
  ownedGamesCount: 1234,
  purchasedGamesCount: 987,
  completedGamesCount: 7,
  completedSetsCount: 9,
  totalAchievementsUnlocked: 4321
}
const finishedRun = {
  status: 'success',
  startedAt: '2026-05-22T10:00:00Z',
  finishedAt: '2026-05-22T10:05:00Z',
  gamesSynced: 6,
  gamesTotal: 6,
  gamesFailed: 0
}

function renderCards(props = {}) {
  return render(SummaryCards, {
    props: {summary, latestSyncRun: finishedRun, integer, syncTitle, syncDetails, ...props}
  })
}

function cardFor(label) {
  return screen.getByText(label).closest('article')
}

describe('SummaryCards', () => {
  test('shows the all, purchased, completed, sets, and achievement totals in order', () => {
    renderCards()

    const labels = Array.from(document.querySelectorAll('.summary-card > span')).map((el) => el.textContent)
    expect(labels).toEqual([
      'All games',
      'Purchased games',
      'Completed games',
      'Completed sets',
      'Achievements',
      'Last sync'
    ])
    expect(cardFor('All games')).toHaveTextContent(integer(1234))
    expect(cardFor('Purchased games')).toHaveTextContent(integer(987))
    expect(cardFor('Completed games')).toHaveTextContent(integer(7))
    expect(cardFor('Completed sets')).toHaveTextContent(integer(9))
    expect(cardFor('Achievements')).toHaveTextContent(integer(4321))
  })

  test('shows the last finished sync with its status and game counts', () => {
    renderCards()

    const card = cardFor('Last sync')
    expect(card).toHaveTextContent(syncTitle(finishedRun))
    expect(card).toHaveTextContent('6/6 games synced, 0 failed')
  })

  test('shows running progress in the current sync card, which stays a polite live region', () => {
    renderCards({
      syncIsRunning: true,
      latestSyncRun: {...finishedRun, status: 'running', finishedAt: '', gamesSynced: 2}
    })

    const card = cardFor('Current sync')
    expect(card).toHaveAttribute('aria-live', 'polite')
    expect(card).toHaveTextContent('Sync running')
    expect(card).toHaveTextContent('2/6 games synced')
    expect(screen.queryByText('Last sync')).not.toBeInTheDocument()
  })

  test('says a sync is starting before its run record arrives', () => {
    renderCards({syncIsRunning: true, latestSyncRun: null})

    const card = cardFor('Current sync')
    expect(card).toHaveTextContent('Sync starting')
    expect(card).toHaveTextContent('Starting sync…')
  })

  test('says no sync has run yet when there is no run record', () => {
    renderCards({latestSyncRun: null})

    const card = cardFor('Last sync')
    expect(card).toHaveTextContent('No sync yet')
    expect(card).toHaveTextContent('Manual sync or startup sync will populate the dashboard.')
  })
})
