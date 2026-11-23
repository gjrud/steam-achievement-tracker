import {cleanup, fireEvent, render, screen, waitFor} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {__resetWailsMocks, __setWailsMocks} from './test/wails-app-mock.js'
import {
  baseState,
  dashboardState,
  missingKeyState,
  openClearDialog,
  openHadesMenu,
  profileSetupState
} from './test/app-fixtures.js'

const bar = () => document.querySelector('.title-bar')
const navQuery = () => screen.queryByRole('navigation', {name: 'Dashboard views'})

// renderWith waits for the first state read to land, so the screen under test is the one that renders.
async function renderWith(state) {
  __setWailsMocks({GetAppState: vi.fn(async () => state)})
  render(App)
  await waitFor(() => expect(document.querySelector('.loading-screen')).not.toBeInTheDocument())
}

afterEach(() => {
  cleanup()
  __resetWailsMocks()
  vi.restoreAllMocks()
})

describe('title bar on every screen', () => {
  test('the loading screen shows the bar with icon and window controls only', async () => {
    __setWailsMocks({GetAppState: vi.fn(() => new Promise(() => {}))})
    render(App)

    expect(bar()).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Minimize'})).toBeInTheDocument()
    expect(navQuery()).not.toBeInTheDocument()
    expect(screen.getByText('Loading tracker…')).toBeInTheDocument()
  })

  test('the startup-error screen shows the bar with icon and window controls only', async () => {
    await renderWith({...baseState(), initError: 'Could not open the database.'})

    expect(screen.getByText('Could not open the database.')).toBeInTheDocument()
    expect(navQuery()).not.toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Settings'})).not.toBeInTheDocument()
  })

  test('the missing-key screen shows the bar with icon and window controls only', async () => {
    await renderWith(missingKeyState())

    expect(navQuery()).not.toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Close'})).toBeInTheDocument()
  })

  test('the profile setup screen shows the bar with icon and window controls only', async () => {
    await renderWith(profileSetupState())

    expect(navQuery()).not.toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Maximize'})).toBeInTheDocument()
  })

  test('the dashboard shows the tabs and the gear in the bar, and no tab row in the body', async () => {
    await renderWith(dashboardState())

    expect(navQuery()).toBeInTheDocument()
    expect(bar().contains(navQuery())).toBe(true)
    expect(screen.getByRole('button', {name: 'Settings'})).toBeInTheDocument()
    expect(document.querySelector('.tabs')).not.toBeInTheDocument()
    expect(screen.getAllByRole('navigation', {name: 'Dashboard views'})).toHaveLength(1)
  })
})

describe('scroll structure', () => {
  test('the page scrolls as a document: there is no inner scroller, and the bar and the custom scrollbar are direct children of the app root', async () => {
    await renderWith(dashboardState())

    expect(document.querySelector('.app-scroller')).not.toBeInTheDocument()
    expect(document.querySelector('.app-shell')).toBeInTheDocument()
    const scrollbar = document.querySelector('.app-scrollbar')
    expect(scrollbar).toBeInTheDocument()
    expect(scrollbar.parentElement).toBe(bar().parentElement)
    expect(bar().contains(scrollbar)).toBe(false)
  })

  test('the custom scrollbar is a decorative mirror: aria-hidden and not in the tab order', async () => {
    await renderWith(dashboardState())

    const scrollbar = document.querySelector('.app-scrollbar')
    expect(scrollbar).toHaveAttribute('aria-hidden', 'true')
    expect(scrollbar).not.toHaveAttribute('tabindex')
    expect(scrollbar.querySelector('[tabindex]')).toBeNull()
  })

  test('the custom scrollbar is present on a setup screen too', async () => {
    await renderWith(missingKeyState())

    expect(document.querySelector('.app-scrollbar')).toBeInTheDocument()
  })

  test('a tab click does not take focus (the bar cancels the focus change), so typing starts the search', async () => {
    await renderWith(dashboardState())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', {name: 'Completed'}))
    expect(screen.getByRole('button', {name: 'Completed'})).not.toHaveFocus()
    await user.keyboard('p')

    expect(screen.getByPlaceholderText('Search games in this view')).toHaveValue('p')
  })

  test('changing view scrolls the document back to the top, and re-selecting the current view does not', async () => {
    await renderWith(dashboardState())
    // Record that the DOM still shows the old view when the scroll happens (the reset runs before Svelte flushes the view
    // change). This catches a reset moved after the flush; it cannot tell the order of two statements in one tick.
    const oldViewInDomAtScroll = []
    const scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => {
      oldViewInDomAtScroll.push(screen.getByRole('button', {name: 'Not completed'}).getAttribute('aria-current') === 'page')
    })
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', {name: 'Not completed'}))
    expect(scrollTo).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', {name: 'Completed'}))
    expect(scrollTo).toHaveBeenCalledTimes(1)
    expect(scrollTo).toHaveBeenLastCalledWith({top: 0, behavior: 'instant'})
    expect(oldViewInDomAtScroll).toEqual([true])

    await user.click(screen.getByRole('button', {name: 'Settings'}))
    expect(scrollTo).toHaveBeenCalledTimes(2)
    expect(scrollTo).toHaveBeenLastCalledWith({top: 0, behavior: 'instant'})
  })
})

describe('title bar and overlays', () => {
  test('Escape closes the game menu while focus is in the bar', async () => {
    await renderWith(dashboardState())
    await openHadesMenu()
    expect(screen.getByRole('menu')).toBeInTheDocument()

    screen.getByRole('button', {name: 'Completed'}).focus()
    await fireEvent.keyDown(document.activeElement, {key: 'Escape'})

    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  test('a game menu opened at the top edge is clamped below the title bar, not under it', async () => {
    await renderWith(dashboardState())
    const card = (await screen.findByRole('heading', {name: 'Hades'})).closest('article')

    await fireEvent.contextMenu(card, {clientX: 100, clientY: 0})

    const menu = await screen.findByRole('menu')
    await waitFor(() => expect(menu.style.visibility).toBe('visible'))
    // 42 px bar (--title-bar-height, read from the stylesheet or its 42 px fallback in jsdom) plus the 12 px menu margin.
    expect(parseFloat(menu.style.top)).toBe(54)
  })

  test('the top clamp reads --title-bar-height, so a taller bar pushes the menu lower', async () => {
    const root = document.documentElement
    root.style.setProperty('--title-bar-height', '60px')
    try {
      await renderWith(dashboardState())
      const card = (await screen.findByRole('heading', {name: 'Hades'})).closest('article')

      await fireEvent.contextMenu(card, {clientX: 100, clientY: 0})

      const menu = await screen.findByRole('menu')
      await waitFor(() => expect(menu.style.visibility).toBe('visible'))
      // 60 px bar plus the 12 px menu margin.
      expect(parseFloat(menu.style.top)).toBe(72)
    } finally {
      root.style.removeProperty('--title-bar-height')
    }
  })

  test('a mousedown on the bar closes the game menu, since a bar drag fires no click', async () => {
    await renderWith(dashboardState())
    await openHadesMenu()
    expect(screen.getByRole('menu')).toBeInTheDocument()

    await fireEvent.mouseDown(bar())

    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  test('with the clear-data dialog open, the tabs and the gear are disabled and the window controls stay live', async () => {
    await renderWith(dashboardState())
    const user = userEvent.setup()
    await openClearDialog(user)

    expect(screen.getByRole('button', {name: 'Not completed'})).toBeDisabled()
    expect(screen.getByRole('button', {name: 'Disabled'})).toBeDisabled()
    expect(screen.getByRole('button', {name: 'Settings'})).toBeDisabled()
    expect(screen.getByRole('button', {name: 'Minimize'})).toBeEnabled()
    expect(screen.getByRole('button', {name: 'Maximize'})).toBeEnabled()
    expect(screen.getByRole('button', {name: 'Close'})).toBeEnabled()
  })

  test('closing the dialog re-enables the tabs and the gear', async () => {
    await renderWith(dashboardState())
    const user = userEvent.setup()
    const dialog = await openClearDialog(user)
    expect(screen.getByRole('button', {name: 'Settings'})).toBeDisabled()

    await user.keyboard('{Escape}')

    expect(dialog).not.toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Settings'})).toBeEnabled()
    expect(screen.getByRole('button', {name: 'Not completed'})).toBeEnabled()
  })
})
