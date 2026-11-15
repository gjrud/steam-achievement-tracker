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

  describe('vetted counts', () => {
    function valueFor(label) {
      return statFor(label).querySelector('dd').textContent.replace(/\s+/g, ' ').trim()
    }

    test('shows the plain total when no counted entry is unvetted', () => {
      render(SummaryStats, {
        props: {
          summary: {...summary, completedGamesVettedCount: 7, completedSetsVettedCount: 9},
          integer
        }
      })

      expect(valueFor('Completed games')).toBe('7')
      expect(valueFor('Completed sets')).toBe('9')
      expect(document.querySelector('.vetted')).toBeNull()
      expect(statFor('Completed games').querySelector('dd')).not.toHaveAttribute('title')
    })

    test('shows vetted of total for games only when only the games counter has unvetted entries', () => {
      render(SummaryStats, {
        props: {
          summary: {...summary, completedGamesCount: 15, completedGamesVettedCount: 12, completedSetsVettedCount: 9},
          integer
        }
      })

      const games = statFor('Completed games').querySelector('dd')
      expect(games.querySelector('.vetted')).toHaveTextContent('12')
      expect(games.querySelector('.vetted-total')).toHaveTextContent('(15)')
      expect(valueFor('Completed games')).toBe('12 vetted of (15)')
      expect(games).toHaveAttribute('title', '12 vetted of 15 total')
      expect(games).not.toHaveAttribute('aria-label')
      // The spoken form is carried by the visually hidden text, not by a dd aria-label.
      expect(games.querySelector('.sr-only')).toHaveTextContent('vetted of')
      expect(valueFor('Completed sets')).toBe('9')
      expect(statFor('Completed sets').querySelector('.vetted')).toBeNull()
    })

    test('shows vetted of total for sets only when only the sets counter has unvetted entries', () => {
      render(SummaryStats, {
        props: {
          summary: {...summary, completedGamesVettedCount: 7, completedSetsCount: 20, completedSetsVettedCount: 4},
          integer
        }
      })

      expect(valueFor('Completed games')).toBe('7')
      expect(statFor('Completed games').querySelector('.vetted')).toBeNull()
      expect(valueFor('Completed sets')).toBe('4 vetted of (20)')
      expect(statFor('Completed sets').querySelector('dd')).toHaveAttribute('title', '4 vetted of 20 total')
    })

    test('shows vetted of total for both counters when both have unvetted entries', () => {
      render(SummaryStats, {
        props: {
          summary: {...summary, completedGamesCount: 15, completedGamesVettedCount: 12, completedSetsCount: 20, completedSetsVettedCount: 4},
          integer
        }
      })

      expect(valueFor('Completed games')).toBe('12 vetted of (15)')
      expect(valueFor('Completed sets')).toBe('4 vetted of (20)')
    })

    test('treats missing vetted fields as all vetted and shows the plain totals', () => {
      const {completedGamesVettedCount, completedSetsVettedCount, ...withoutVetted} = {...summary, completedGamesCount: 15, completedSetsCount: 20}
      expect(completedGamesVettedCount).toBeUndefined()
      expect(completedSetsVettedCount).toBeUndefined()
      render(SummaryStats, {props: {summary: withoutVetted, integer}})

      expect(valueFor('Completed games')).toBe('15')
      expect(valueFor('Completed sets')).toBe('20')
      expect(document.querySelector('.vetted')).toBeNull()
    })
  })
})
