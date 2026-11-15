import {cleanup, fireEvent, render, screen, waitFor} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {tick} from 'svelte'
import {afterEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {INITIAL, SPACER_BIAS, STEP} from './lib/components/IncrementalGrid.svelte'
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

// ResizeObserver stub: records observe/disconnect and lets a test fire the callback like a wrapper resize.
function stubResizeObserver() {
  const instances = []

  class StubResizeObserver {
    constructor(callback) {
      this.callback = callback
      this.observed = []
      this.disconnected = false
      instances.push(this)
    }

    observe(target) {
      this.observed.push(target)
    }

    disconnect() {
      this.disconnected = true
    }
  }

  vi.stubGlobal('ResizeObserver', StubResizeObserver)

  return {
    instances,
    get latest() {
      return instances.at(-1)
    },
    fire() {
      const observer = instances.at(-1)
      observer.callback([], observer)
    }
  }
}

// jsdom has no layout. Stubs the card grid (.game-grid) and its button so the spacer estimate uses real numbers:
// `cols` columns, rows `pitch` px apart (row height + `gap`), a button `buttonHeight` px tall.
// gridBottom is the grid's bottom edge in viewport coordinates. wrapperHeight overrides the grid height (0 = no layout).
// moreMarginTop / moreMarginBottom are the margins of the .grid-more box that holds the button (0 by default).
// moreBottom is the .grid-more box's bottom edge in viewport coordinates (far below the viewport by default).
let restoreLayout = () => {}

function stubGridLayout(overrides = {}) {
  const layout = {
    cols: 4, colWidth: 200, pitch: 300, gap: 16, buttonHeight: 40, gridBottom: 0, wrapperHeight: undefined,
    moreMarginTop: 0, moreMarginBottom: 0, moreBottom: 100000,
    ...overrides
  }
  const originalGetComputedStyle = window.getComputedStyle.bind(window)
  const originalRect = Element.prototype.getBoundingClientRect
  const originalOffsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight')

  vi.spyOn(window, 'getComputedStyle').mockImplementation((element, pseudo) => {
    if (element.classList?.contains('game-grid')) {
      return {gridTemplateColumns: Array(layout.cols).fill(`${layout.colWidth}px`).join(' '), rowGap: `${layout.gap}px`}
    }
    if (element.classList?.contains('grid-more')) {
      return {marginTop: `${layout.moreMarginTop}px`, marginBottom: `${layout.moreMarginBottom}px`}
    }
    return originalGetComputedStyle(element, pseudo)
  })

  Element.prototype.getBoundingClientRect = function getBoundingClientRect() {
    if (this.classList?.contains('game-grid')) {
      return {x: 0, y: layout.gridBottom, top: layout.gridBottom, bottom: layout.gridBottom, left: 0, right: 0, width: 0, height: 0}
    }
    if (this.classList?.contains('grid-more')) {
      return {x: 0, y: layout.moreBottom, top: layout.moreBottom, bottom: layout.moreBottom, left: 0, right: 0, width: 0, height: 0}
    }
    return originalRect.call(this)
  }

  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
    configurable: true,
    get() {
      if (this.classList.contains('game-grid')) {
        if (layout.wrapperHeight !== undefined) return layout.wrapperHeight
        const rows = Math.ceil(this.children.length / layout.cols)
        return rows ? rows * layout.pitch - layout.gap : 0
      }
      return this.tagName === 'BUTTON' ? layout.buttonHeight : 0
    }
  })

  restoreLayout = () => {
    Element.prototype.getBoundingClientRect = originalRect
    if (originalOffsetHeight) Object.defineProperty(HTMLElement.prototype, 'offsetHeight', originalOffsetHeight)
    else delete HTMLElement.prototype.offsetHeight
  }

  return layout
}

// Records every HTMLElement.focus call as {el, args} and still runs the real focus, so activeElement stays correct.
function recordFocusCalls() {
  const original = HTMLElement.prototype.focus
  const calls = []
  vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function focus(...args) {
    calls.push({el: this, args})
    return original.apply(this, args)
  })
  return calls
}

// jsdom does not scroll. Spies on the APIs that would move the page, so a test can assert the component used none.
let restoreScroll = () => {}

function spyOnScrolling() {
  const scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  const hadScrollIntoView = 'scrollIntoView' in Element.prototype
  const originalScrollIntoView = Element.prototype.scrollIntoView
  const scrollIntoView = vi.fn()
  Element.prototype.scrollIntoView = scrollIntoView
  restoreScroll = () => {
    if (hadScrollIntoView) Element.prototype.scrollIntoView = originalScrollIntoView
    else delete Element.prototype.scrollIntoView
  }
  return {scrollTo, scrollIntoView}
}

const cardCount = () => document.querySelectorAll('.game-card').length
const moreBox = () => document.querySelector('.grid-more')
const spacer = () => document.querySelector('.grid-spacer')
const showMoreButton = (name) => screen.queryByRole('button', {name})
const cardNamed = (name) => screen.getByRole('heading', {name}).closest('article')
const alternatingTags = (index) => ({tags: index % 2 ? ['almost_there'] : ['new_achievements_added']})
// Spacer px for unmounted rows * pitch (unmountedPx), scaled by SPACER_BIAS, minus the button block (buttonBlockPx, 40px by default).
// The bias applies to the row estimate only, like the component.
const spacerPx = (unmountedPx, buttonBlockPx = 40) => `${Math.max(0, Math.round(unmountedPx * SPACER_BIAS - buttonBlockPx))}px`

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
  restoreLayout()
  restoreLayout = () => {}
  restoreScroll()
  restoreScroll = () => {}
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
    // The observed element is the box holding the button and the spacer, not the button itself.
    expect(io.latest.calls).toContainEqual(['observe', moreBox()])
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
    const box = moreBox()

    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))
    await tick()

    expect(io.latest.calls.slice(-2)).toEqual([['unobserve', box], ['observe', box]])

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
    const oldBox = moreBox()

    // First refresh: 30 items, so the button is removed and every card is shown.
    await fireEvent.contextMenu(cardNamed('Batch Game 001'))
    await user.click(await screen.findByRole('button', {name: 'Disable game'}))
    await waitFor(() => expect(cardCount()).toBe(30))
    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
    expect(io.latest.calls).toContainEqual(['unobserve', oldBox])

    // Second refresh: 120 items, so a new button is created with the kept limit of 96.
    await fireEvent.contextMenu(cardNamed('Batch Game 001'))
    await user.click(await screen.findByRole('button', {name: 'Disable game'}))
    await screen.findByRole('button', {name: 'Show more (96 of 120)'})
    const newBox = moreBox()

    expect(newBox).not.toBe(oldBox)
    expect(cardCount()).toBe(96)
    expect(io.latest.calls).toContainEqual(['observe', newBox])
    expect([...io.latest.targets]).toEqual([newBox])
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

describe('App incremental card grid spacer and viewport-sized growth', () => {
  test('reserves the height of the unmounted rows under the button', async () => {
    stubIntersectionObserver()
    stubGridLayout()
    await renderDashboard()

    // 120 cards at 4 columns = 30 rows. 48 mounted = 12 rows, leaving 18 rows of 300px, times SPACER_BIAS, minus the 40px button.
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(18 * 300)))
    const button = screen.getByRole('button', {name: 'Show more (48 of 120)'})
    expect(button.nextElementSibling).toBe(spacer())
  })

  test('the spacer shrinks as cards load and is removed when the list is complete', async () => {
    const io = stubIntersectionObserver()
    const layout = stubGridLayout({gridBottom: 1000})
    await renderDashboard()
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(18 * 300)))

    // 1268px needed past the grid = 5 rows; STEP (48) is larger, so 96 mounted = 24 rows, 6 left: 6 * 300, times SPACER_BIAS, minus 40.
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(6 * 300)))

    layout.gridBottom = -3000
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(120))
    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
    expect(spacer()).toBeNull()
  })

  test('grows to the whole remaining list in one step when the grid is far above the viewport', async () => {
    const io = stubIntersectionObserver()
    vi.stubGlobal('innerHeight', 800)
    stubGridLayout({gridBottom: -3000})
    await renderDashboard()
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(18 * 300)))

    // Needed: 800 + 1500 + 3000 = 5300px = 18 rows of 300px = 72 cards. One callback covers all 72 unmounted cards.
    io.fire(true)

    await waitFor(() => expect(cardCount()).toBe(120))
    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
    expect(spacer()).toBeNull()
  })

  test('loads the whole remainder in one step when the end of the list is within reach, even if the row estimate is short', async () => {
    const io = stubIntersectionObserver()
    vi.stubGlobal('innerHeight', 800)
    // Needed: 800 + 1500 + 1000 = 3300px = 11 rows = 44 cards, under the STEP floor, so the computed batch is 48 (96 cards).
    // The box bottom at 2000px is inside the 2300px reach, so the remaining 72 cards load in one step.
    stubGridLayout({gridBottom: -1000, moreBottom: 2000})
    await renderDashboard()

    io.fire(true)

    await waitFor(() => expect(cardCount()).toBe(120))
    expect(showMoreButton(/Show more/)).not.toBeInTheDocument()
  })

  test('loads only the computed batch when the end of the list is beyond reach', async () => {
    const io = stubIntersectionObserver()
    vi.stubGlobal('innerHeight', 800)
    // Needed: 800 + 1500 + 2000 = 4300px = 15 rows = 60 cards, above the STEP floor and below the 72 remaining.
    // The box bottom at 5000px is beyond the 2300px reach, so the batch stays at 60 (108 cards).
    stubGridLayout({gridBottom: -2000, moreBottom: 5000})
    await renderDashboard()

    io.fire(true)

    await waitFor(() => expect(cardCount()).toBe(108))
    expect(screen.getByRole('button', {name: 'Show more (108 of 120)'})).toBeInTheDocument()
  })

  test('without a usable layout the end-in-reach check does not apply: one STEP even when the box is in reach', async () => {
    const io = stubIntersectionObserver()
    vi.stubGlobal('innerHeight', 800)
    // wrapperHeight 0 means no pitch. The box sits at the viewport edge, which is inside the reach, but only STEP may load.
    stubGridLayout({wrapperHeight: 0, gridBottom: -3000, moreBottom: 0})
    await renderDashboard()

    io.fire(true)

    await waitFor(() => expect(cardCount()).toBe(96))
    expect(screen.getByRole('button', {name: 'Show more (96 of 120)'})).toBeInTheDocument()
  })

  test('a small gap below the grid still grows by one STEP', async () => {
    const io = stubIntersectionObserver()
    vi.stubGlobal('innerHeight', 800)
    stubGridLayout({gridBottom: 1000})
    await renderDashboard()

    // Needed: 800 + 1500 - 1000 = 1300px = 5 rows = 20 cards, below the STEP floor of 48.
    io.fire(true)

    await waitFor(() => expect(cardCount()).toBe(96))
    expect(screen.getByRole('button', {name: 'Show more (96 of 120)'})).toBeInTheDocument()
  })

  test('without a usable layout the spacer is 0px and each observer hit adds one STEP', async () => {
    const io = stubIntersectionObserver()
    vi.stubGlobal('innerHeight', 800)
    stubGridLayout({wrapperHeight: 0, gridBottom: -3000})
    await renderDashboard()
    expect(spacer().style.height).toBe('0px')

    // The viewport would need 72 cards, but the zero-height grid gives no pitch, so only STEP applies.
    io.fire(true)

    await waitFor(() => expect(cardCount()).toBe(96))
    expect(spacer().style.height).toBe('0px')
    expect(screen.getByRole('button', {name: 'Show more (96 of 120)'})).toBeInTheDocument()
  })

  test('re-measures the spacer when the grid resizes and disconnects on destroy', async () => {
    stubIntersectionObserver()
    const resizeObserver = stubResizeObserver()
    const layout = stubGridLayout()
    await renderDashboard()
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(18 * 300)))
    expect(resizeObserver.latest.observed).toEqual([document.querySelector('.game-grid')])

    // Narrow to 2 columns: 24 rows mounted, 60 rows total, 36 rows of 300px, times SPACER_BIAS, minus the 40px button.
    layout.cols = 2
    resizeObserver.fire()
    await waitFor(() => expect(spacer().style.height).toBe(spacerPx(36 * 300)))

    cleanup()
    expect(resizeObserver.latest.disconnected).toBe(true)
  })

  test('the spacer is aria-hidden, empty, and not focusable', async () => {
    stubIntersectionObserver()
    stubGridLayout()
    await renderDashboard()
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(18 * 300)))

    expect(spacer()).toHaveAttribute('aria-hidden', 'true')
    expect(spacer()).not.toHaveAttribute('tabindex')
    expect(spacer()).toBeEmptyDOMElement()
  })

  test('a reset to the first 48 recomputes the spacer for the reset list', async () => {
    const io = stubIntersectionObserver()
    stubGridLayout()
    await renderDashboard()
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(18 * 300)))
    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(96))
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(6 * 300)))

    // Typing changes resetKey: the limit drops to 48, so the spacer must again cover the 72 unmounted cards (18 rows).
    await userEvent.setup().type(screen.getByPlaceholderText('Search games in this view'), 'a')

    await waitFor(() => expect(cardCount()).toBe(48))
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(18 * 300)))
  })

  test('the spacer subtracts the top and bottom margins of the button box', async () => {
    stubIntersectionObserver()
    stubGridLayout({moreMarginTop: 12, moreMarginBottom: 8})
    await renderDashboard()

    // 18 unmounted rows * 300px, times SPACER_BIAS, minus (40px button + 12px top + 8px bottom margin).
    // Without margins the value is 18 * 300 * SPACER_BIAS - 40, so the margins must change the result.
    await waitFor(() => expect(spacer()?.style.height).toBe(spacerPx(18 * 300, 40 + 12 + 8)))
    expect(spacerPx(18 * 300, 40 + 12 + 8)).not.toBe(spacerPx(18 * 300))
  })

  test('the estimated total never exceeds the true full height when the pitch is uniform', async () => {
    stubIntersectionObserver()
    stubGridLayout()
    await renderDashboard()

    // Estimate at the first render: loaded grid + spacer + button block. Uniform 300px rows make the raw estimate exact,
    // so the biased spacer must keep the total at or below the true height.
    const grid = document.querySelector('.game-grid')
    const button = screen.getByRole('button', {name: 'Show more (48 of 120)'})
    const estimatedTotal = grid.offsetHeight + parseInt(spacer().style.height, 10) + button.offsetHeight
    // True full height with all 120 cards at 4 columns: 30 rows * 300px - 16px row gap, no button.
    const trueFullHeight = (120 / 4) * 300 - 16

    expect(estimatedTotal).toBeLessThan(trueFullHeight)
  })
})

describe('App incremental card grid focus handoff never scrolls', () => {
  test('after a click finishes the list the focus handoff uses preventScroll and the page does not scroll', async () => {
    const {scrollTo, scrollIntoView} = spyOnScrolling()
    await renderDashboard()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', {name: 'Show more (48 of 120)'}))
    await waitFor(() => expect(cardCount()).toBe(96))
    const focusCalls = recordFocusCalls()
    const scrollY = window.scrollY

    await user.click(screen.getByRole('button', {name: 'Show more (96 of 120)'}))
    await waitFor(() => expect(cardCount()).toBe(120))
    await waitFor(() => expect(focusCalls.length).toBeGreaterThan(0))

    // First card revealed by the last step is index 96 = Batch Game 097.
    const card = cardNamed('Batch Game 097')
    expect(focusCalls.at(-1)).toEqual({el: card, args: [{preventScroll: true}]})
    expect(document.activeElement).toBe(card)
    expect(scrollTo).not.toHaveBeenCalled()
    expect(scrollIntoView).not.toHaveBeenCalled()
    expect(window.scrollY).toBe(scrollY)
  })

  test('after an observer hit finishes the list with the button focused the page does not scroll', async () => {
    const io = stubIntersectionObserver()
    const {scrollTo, scrollIntoView} = spyOnScrolling()
    // Grid far above the viewport: one observer hit adds all 72 remaining cards, and the button still holds focus.
    vi.stubGlobal('innerHeight', 800)
    stubGridLayout({gridBottom: -3000})
    await renderDashboard()
    screen.getByRole('button', {name: 'Show more (48 of 120)'}).focus()
    const focusCalls = recordFocusCalls()
    const scrollY = window.scrollY

    io.fire(true)
    await waitFor(() => expect(cardCount()).toBe(120))
    // The handoff runs after the DOM update, so wait for it before asserting.
    await waitFor(() => expect(focusCalls.length).toBeGreaterThan(0))

    // First card revealed by this step is index 48 = Batch Game 049.
    const card = cardNamed('Batch Game 049')
    expect(focusCalls.at(-1)).toEqual({el: card, args: [{preventScroll: true}]})
    expect(document.activeElement).toBe(card)
    expect(scrollTo).not.toHaveBeenCalled()
    expect(scrollIntoView).not.toHaveBeenCalled()
    expect(window.scrollY).toBe(scrollY)
  })
})
