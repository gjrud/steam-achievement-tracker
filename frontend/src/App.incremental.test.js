import {cleanup, fireEvent, render, screen, waitFor} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {tick} from 'svelte'
import {afterEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {INITIAL, STEP} from './lib/components/IncrementalGrid.svelte'
import {__resetWailsMocks, __setWailsMocks} from './test/wails-app-mock.js'
import {dashboardState, manyGames} from './test/app-fixtures.js'

// Controllable IntersectionObserver: records observe/unobserve/disconnect and lets a test fire the callback.
function stubIntersectionObserver() {
  const instances = []

  class StubIntersectionObserver {
    constructor(callback, options) {
      this.callback = callback
      this.options = options
      this.targets = new Set()
      this.calls = []
      this.disconnected = false
      instances.push(this)
    }

    observe(target) {
      this.targets.add(target)
      this.calls.push(['observe', target])
    }

    unobserve(target) {
      this.targets.delete(target)
      this.calls.push(['unobserve', target])
    }

    disconnect() {
      this.disconnected = true
      this.targets.clear()
      this.calls.push(['disconnect'])
    }
  }

  vi.stubGlobal('IntersectionObserver', StubIntersectionObserver)

  return {
    instances,
    get latest() {
      return instances.at(-1)
    },
    // fire reports isIntersecting for every target the latest observer watches, like the browser would.
    fire(isIntersecting) {
      const observer = instances.at(-1)
      const entries = [...observer.targets].map((target) => ({target, isIntersecting}))
      observer.callback(entries, observer)
    }
  }
}

const cardCount = () => document.querySelectorAll('.game-card').length
const showMoreButton = (name) => screen.queryByRole('button', {name})
const cardNamed = (name) => screen.getByRole('heading', {name}).closest('article')
const alternatingTags = (index) => ({tags: index % 2 ? ['almost_there'] : ['new_achievements_added']})

function batchDashboard(overrides = {}) {
  return dashboardState({
    suggestions: manyGames(120, alternatingTags),
    completed: manyGames(120, {tags: ['completed'], completionPercent: 100, unlockedAchievements: 10, totalAchievements: 10, isCompleted: true}),
    ...overrides
  })
}

async function renderDashboard(state = batchDashboard()) {
  __setWailsMocks({GetAppState: vi.fn(async () => state)})
  render(App)
  await screen.findByRole('button', {name: 'Show more (48 of 120)'})
}

afterEach(() => {
  cleanup()
  __resetWailsMocks()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('IncrementalGrid constants', () => {
  test('first render and each growth step are 48 cards', () => {
    expect(INITIAL).toBe(48)
    expect(STEP).toBe(48)
  })
})

describe('App incremental card grid with IntersectionObserver', () => {
  test('mounts only the first 48 cards and labels the sentinel with the counts', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()

    expect(cardCount()).toBe(48)
    const button = screen.getByRole('button', {name: 'Show more (48 of 120)'})
    expect(button).toHaveAttribute('type', 'button')
    expect(io.latest.calls).toContainEqual(['observe', button])
    expect(io.latest.options).toEqual({rootMargin: '1500px'})
    expect(screen.queryByRole('heading', {name: 'Batch Game 049'})).not.toBeInTheDocument()
  })

  test('grows by 48 when the sentinel intersects', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()

    io.fire(true)

    await waitFor(() => expect(cardCount()).toBe(96))
    expect(screen.getByRole('button', {name: 'Show more (96 of 120)'})).toBeInTheDocument()
  })

  test('re-observes the sentinel after each growth so growth continues while it stays visible', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()
    const button = screen.getByRole('button', {name: 'Show more (48 of 120)'})

    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))
    await tick()

    expect(io.latest.calls.slice(-2)).toEqual([['unobserve', button], ['observe', button]])

    io.fire(false)
    await tick()
    expect(cardCount()).toBe(96)

    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(120))
    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
  })

  test('disconnects the observer when the grid is destroyed', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()

    await userEvent.setup().click(screen.getByRole('button', {name: 'Settings'}))

    expect(await screen.findByRole('heading', {name: 'Settings'})).toBeInTheDocument()
    expect(io.instances[0].disconnected).toBe(true)
  })

  test('disconnects the observer on unmount', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()

    cleanup()

    expect(io.instances.length).toBeGreaterThan(0)
    expect(io.instances.every((observer) => observer.disconnected)).toBe(true)
  })

  test('shows no Show more button when the list fits in the first render', async () => {
    stubIntersectionObserver()
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState({suggestions: manyGames(48, alternatingTags)}))})

    render(App)

    await waitFor(() => expect(cardCount()).toBe(48))
    expect(screen.queryByRole('button', {name: /Show more/})).not.toBeInTheDocument()
  })

  test('shows no Show more button for the default small fixture', async () => {
    stubIntersectionObserver()
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})

    render(App)

    expect(await screen.findByRole('heading', {name: 'Hades'})).toBeInTheDocument()
    expect(screen.queryByRole('button', {name: /Show more/})).not.toBeInTheDocument()
  })

  test('resets to the first 48 when the search text changes', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))

    await userEvent.setup().type(screen.getByPlaceholderText('Search games in this view'), 'a')

    await waitFor(() => expect(cardCount()).toBe(48))
    expect(screen.getByRole('button', {name: 'Show more (48 of 120)'})).toBeInTheDocument()
  })

  test('does not reset when only trailing spaces are added to the search', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()
    const user = userEvent.setup()
    const search = screen.getByPlaceholderText('Search games in this view')
    await user.type(search, 'a')
    await waitFor(() => expect(cardCount()).toBe(48))
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))

    await user.type(search, ' ')
    await tick()

    expect(search).toHaveValue('a ')
    expect(cardCount()).toBe(96)
    expect(screen.getByRole('button', {name: 'Show more (96 of 120)'})).toBeInTheDocument()
  })

  test('resets to the first 48 when the tag filter changes', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))

    await userEvent.setup().click(screen.getByRole('button', {name: /Almost there\s+60/}))

    await waitFor(() => expect(screen.getByRole('button', {name: 'Show more (48 of 60)'})).toBeInTheDocument())
    expect(cardCount()).toBe(48)
  })

  test('resets to the first 48 when switching views and back', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', {name: 'Completed'}))
    expect(await screen.findByText('120 results')).toBeInTheDocument()
    expect(cardCount()).toBe(48)

    await user.click(screen.getByRole('button', {name: 'Not completed'}))
    expect(await screen.findByRole('button', {name: 'Show more (48 of 120)'})).toBeInTheDocument()
    expect(cardCount()).toBe(48)
  })

  test('keeps the grown limit when the dashboard state refreshes to a different list', async () => {
    const io = stubIntersectionObserver()
    const disable = vi.fn(async () => batchDashboard({suggestions: manyGames(119, alternatingTags)}))
    __setWailsMocks({
      GetAppState: vi.fn(async () => batchDashboard()),
      DisableGame: disable
    })
    const user = userEvent.setup()
    render(App)
    await screen.findByRole('button', {name: 'Show more (48 of 120)'})
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))

    await fireEvent.contextMenu(cardNamed('Batch Game 001'))
    await user.click(await screen.findByRole('button', {name: 'Disable game'}))

    await waitFor(() => expect(disable).toHaveBeenCalledWith(1000, 0))
    expect(await screen.findByRole('button', {name: 'Show more (96 of 119)'})).toBeInTheDocument()
    expect(cardCount()).toBe(96)
  })

  test('observes only the new sentinel when the button is re-created after the list shrinks and grows again', async () => {
    const io = stubIntersectionObserver()
    const refreshes = [batchDashboard({suggestions: manyGames(30, alternatingTags)}), batchDashboard()]
    const disable = vi.fn(async () => refreshes.shift())
    __setWailsMocks({
      GetAppState: vi.fn(async () => batchDashboard()),
      DisableGame: disable
    })
    const user = userEvent.setup()
    render(App)
    await screen.findByRole('button', {name: 'Show more (48 of 120)'})
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))
    const oldButton = screen.getByRole('button', {name: 'Show more (96 of 120)'})

    // First refresh: 30 items, so the button is removed and every card is shown.
    await fireEvent.contextMenu(cardNamed('Batch Game 001'))
    await user.click(await screen.findByRole('button', {name: 'Disable game'}))
    await waitFor(() => expect(cardCount()).toBe(30))
    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
    expect(io.latest.calls).toContainEqual(['unobserve', oldButton])

    // Second refresh: 120 items, so a new button is created with the kept limit of 96.
    await fireEvent.contextMenu(cardNamed('Batch Game 001'))
    await user.click(await screen.findByRole('button', {name: 'Disable game'}))
    const newButton = await screen.findByRole('button', {name: 'Show more (96 of 120)'})

    expect(newButton).not.toBe(oldButton)
    expect(cardCount()).toBe(96)
    expect(io.latest.calls).toContainEqual(['observe', newButton])
    expect([...io.latest.targets]).toEqual([newButton])
  })

  test('keeps focus where it was when observer-driven growth finishes the list', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()
    const search = screen.getByPlaceholderText('Search games in this view')
    search.focus()

    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(120))

    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
    expect(document.activeElement).toBe(search)
  })

  test('keeps the full counts in the All chip and the Completed pill', async () => {
    stubIntersectionObserver()
    await renderDashboard()
    const user = userEvent.setup()

    expect(screen.getByRole('button', {name: /All\s+120/})).toBeInTheDocument()
    expect(cardCount()).toBe(48)

    await user.click(screen.getByRole('button', {name: 'Completed'}))

    expect(await screen.findByText('120 results')).toBeInTheDocument()
    expect(cardCount()).toBe(48)
  })

  test('opens the card menu on a card that was added by growth', async () => {
    const io = stubIntersectionObserver()
    await renderDashboard()
    expect(screen.queryByRole('heading', {name: 'Batch Game 060'})).not.toBeInTheDocument()
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))

    await fireEvent.contextMenu(cardNamed('Batch Game 060'))

    expect(await screen.findByRole('button', {name: 'Disable game'})).toBeInTheDocument()
  })
})

describe('App incremental card grid without IntersectionObserver', () => {
  test('renders the first 48 cards and a button that grows the list', async () => {
    expect(typeof IntersectionObserver).toBe('undefined')
    await renderDashboard()
    const user = userEvent.setup()

    expect(cardCount()).toBe(48)
    await user.click(screen.getByRole('button', {name: 'Show more (48 of 120)'}))
    await waitFor(() => expect(cardCount()).toBe(96))

    await user.click(screen.getByRole('button', {name: 'Show more (96 of 120)'}))

    await waitFor(() => expect(cardCount()).toBe(120))
    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
  })

  test('moves focus to the first new card when a manual activation finishes the list', async () => {
    await renderDashboard()
    const user = userEvent.setup()
    const first = screen.getByRole('button', {name: 'Show more (48 of 120)'})
    first.focus()

    await user.click(first)
    await waitFor(() => expect(cardCount()).toBe(96))
    // Not the last step yet: the button stays and keeps focus.
    const second = screen.getByRole('button', {name: 'Show more (96 of 120)'})
    expect(document.activeElement).toBe(second)

    await user.click(second)
    await waitFor(() => expect(cardCount()).toBe(120))
    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
    expect(document.activeElement).toBe(cardNamed('Batch Game 097'))
    expect(cardNamed('Batch Game 097')).toHaveAttribute('tabindex', '-1')
  })

  test('does not move focus when the button was clicked without holding focus', async () => {
    await renderDashboard()
    const search = screen.getByPlaceholderText('Search games in this view')
    const button = screen.getByRole('button', {name: 'Show more (48 of 120)'})
    search.focus()

    // element.click() does not move focus, so the button never holds focus here.
    button.click()
    await waitFor(() => expect(cardCount()).toBe(96))
    screen.getByRole('button', {name: 'Show more (96 of 120)'}).click()
    await waitFor(() => expect(cardCount()).toBe(120))

    expect(document.activeElement).toBe(search)
  })
})
