import {cleanup, fireEvent, render, screen} from '@testing-library/svelte'
import {afterEach, describe, expect, test, vi} from 'vitest'
import {game} from '../../test/app-fixtures.js'
import GameCard from './GameCard.svelte'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const hades = () => game({appid: 1, name: 'Hades'})

describe('GameCard context menu', () => {
  test('a right click on the card passes its event and the game', async () => {
    const onContextMenu = vi.fn()
    const card = hades()
    render(GameCard, {props: {game: card, onContextMenu}})

    await fireEvent.contextMenu(screen.getByRole('heading', {name: 'Hades'}).closest('article'), {clientX: 30, clientY: 40})

    expect(onContextMenu).toHaveBeenCalledOnce()
    const [event, openedGame] = onContextMenu.mock.calls[0]
    expect(onContextMenu.mock.calls[0]).toHaveLength(2)
    expect(event.clientX).toBe(30)
    expect(event.clientY).toBe(40)
    expect(event.defaultPrevented).toBe(true)
    expect(openedGame).toBe(card)
  })
})
