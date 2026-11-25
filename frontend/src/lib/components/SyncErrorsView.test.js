import {cleanup, fireEvent, render, screen} from '@testing-library/svelte'
import {afterEach, describe, expect, test, vi} from 'vitest'
import {game} from '../../test/app-fixtures.js'
import SyncErrorsView from './SyncErrorsView.svelte'

afterEach(() => {
  cleanup()
})

function props(overrides = {}) {
  return {
    games: [game({appid: 4, name: 'Broken A', syncWarning: true, lastError: 'HTTP 503'})],
    tagLabel: (tag) => tag,
    toneClass: () => '',
    tagTone: () => '',
    gameTitle: (g) => g.name,
    gameWarning: () => 'Sync failed',
    hasAchievements: () => false,
    shouldShowSyncErrorMessage: () => true,
    completionLabel: () => '',
    pct: (v) => v,
    onOpenGameMenu: vi.fn(),
    onRunGameAction: vi.fn(),
    ...overrides
  }
}

describe('SyncErrorsView rows', () => {
  test('a right click on a row opens the game menu and cancels the browser menu', async () => {
    const onOpenGameMenu = vi.fn()
    const games = [game({appid: 4, name: 'Broken A', syncWarning: true, lastError: 'HTTP 503'})]
    render(SyncErrorsView, {props: props({games, onOpenGameMenu})})

    const notCancelled = await fireEvent.contextMenu(screen.getByRole('heading', {name: 'Broken A'}).closest('article'), {clientX: 30, clientY: 40})

    expect(notCancelled).toBe(false)
    expect(onOpenGameMenu).toHaveBeenCalledOnce()
    const [event, openedGame] = onOpenGameMenu.mock.calls[0]
    expect(event.clientX).toBe(30)
    expect(openedGame).toBe(games[0])
  })
})
