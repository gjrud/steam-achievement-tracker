import {cleanup, render, screen} from '@testing-library/svelte'
import {afterEach, describe, expect, test} from 'vitest'
import SummaryStats from './SummaryStats.svelte'
import {integer} from '../format.js'

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

function statFor(label) {
  return screen.getByText(label).closest('.stat')
}

describe('SummaryStats', () => {
  test('shows the all, purchased, completed, sets, and achievement counters in order', () => {
    render(SummaryStats, {props: {summary, integer}})

    const labels = Array.from(document.querySelectorAll('dl > .stat > dt')).map((el) => el.textContent)
    expect(labels).toEqual(['All games', 'Purchased', 'Completed games', 'Completed sets', 'Achievements'])
    expect(statFor('All games').querySelector('dd')).toHaveTextContent(integer(1234))
    expect(statFor('Purchased').querySelector('dd')).toHaveTextContent(integer(987))
    expect(statFor('Completed games').querySelector('dd')).toHaveTextContent(integer(7))
    expect(statFor('Completed sets').querySelector('dd')).toHaveTextContent(integer(9))
    expect(statFor('Achievements').querySelector('dd')).toHaveTextContent(integer(4321))
  })

  test('groups the counters under Library, Completed and Unlocked', () => {
    render(SummaryStats, {props: {summary, integer}})

    const groups = Array.from(document.querySelectorAll('.stat-group')).map((group) => ({
      label: group.querySelector('.stat-group-label').textContent,
      terms: Array.from(group.querySelectorAll('dt')).map((el) => el.textContent)
    }))
    expect(groups).toEqual([
      {label: 'Library', terms: ['All games', 'Purchased']},
      {label: 'Completed', terms: ['Completed games', 'Completed sets']},
      {label: 'Unlocked', terms: ['Achievements']}
    ])
  })

  test('renders no sync information', () => {
    render(SummaryStats, {props: {summary, integer}})

    expect(screen.queryByText('Last sync')).not.toBeInTheDocument()
    expect(screen.queryByText('Current sync')).not.toBeInTheDocument()
    expect(document.querySelector('[aria-live]')).toBeNull()
  })
})
