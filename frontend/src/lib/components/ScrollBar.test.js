import {cleanup, fireEvent, render} from '@testing-library/svelte'
import {tick} from 'svelte'
import {afterEach, beforeEach, describe, expect, test, vi} from 'vitest'
import ScrollBar from './ScrollBar.svelte'

// jsdom does no layout, so every metric the component reads is defined here: the document's scrollHeight and clientHeight,
// window.scrollY, and the track's clientHeight and top. Layout used throughout unless a test says otherwise:
// 600 px viewport, 3000 px page, 537 px track (an example height; the real one is 600 minus the bar and the bottom inset). Thumb = 537 * 600 / 3000 = 107.4 px, travel = 429.6 px,
// range = 2400 px.
const VIEWPORT = 600
const TOTAL = 3000
const TRACK = 537

const track = () => document.querySelector('.app-scrollbar')
const thumb = () => document.querySelector('.app-scrollbar-thumb')

let frames = []
let scrollY = 0
const defined = []

function define(target, name, value) {
  const had = Object.prototype.hasOwnProperty.call(target, name)
  const original = Object.getOwnPropertyDescriptor(target, name)
  Object.defineProperty(target, name, {configurable: true, get: () => (typeof value === 'function' ? value() : value)})
  defined.push(() => {
    if (had) Object.defineProperty(target, name, original)
    else delete target[name]
  })
}

// layout sets the page metrics. Call it before render for the first measurement, or after render followed by resize().
function layout({viewport = VIEWPORT, total = TOTAL, y = 0} = {}) {
  scrollY = y
  const root = document.documentElement
  define(root, 'clientHeight', viewport)
  define(root, 'scrollHeight', total)
  define(window, 'scrollY', () => scrollY)
}

// The track only exists after render: its metrics are defined on the element, then a resize event re-measures.
function layoutTrack({height = TRACK, top = 42} = {}) {
  const el = track()
  define(el, 'clientHeight', height)
  el.getBoundingClientRect = () => ({top, bottom: top + height, left: 0, right: 14, width: 14, height, x: 0, y: top})
}

function flushFrames() {
  const pending = frames
  frames = []
  pending.forEach((callback) => callback(0))
}

async function measure() {
  window.dispatchEvent(new Event('resize'))
  flushFrames()
  await tick()
}

async function mount(options = {}) {
  layout(options)
  const result = render(ScrollBar)
  layoutTrack(options)
  await measure()
  return result
}

// The last scrollTo call, with the top compared to a few decimals (the component and the test round differently).
function expectLastScroll(top) {
  const call = scrollTo.mock.lastCall[0]
  expect(call.behavior).toBe('instant')
  expect(call.top).toBeCloseTo(top, 6)
}

const thumbHeight = () => parseFloat(thumb().style.height)
const thumbOffset = () => parseFloat(thumb().style.transform.match(/translateY\((.*)px\)/)[1])

let scrollTo

beforeEach(() => {
  frames = []
  vi.stubGlobal('requestAnimationFrame', (callback) => frames.push(callback))
  vi.stubGlobal('cancelAnimationFrame', () => {
    frames = []
  })
  scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  defined.splice(0).reverse().forEach((undo) => undo())
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('ScrollBar visibility', () => {
  test('is hidden while the page does not overflow', async () => {
    await mount({total: VIEWPORT})

    expect(track()).not.toHaveClass('scrollable')
  })

  test('is shown once the page is taller than the window', async () => {
    await mount()

    expect(track()).toHaveClass('scrollable')
  })

  test('hides again when the page shrinks to fit', async () => {
    await mount()
    expect(track()).toHaveClass('scrollable')

    layout({total: VIEWPORT})
    await measure()

    expect(track()).not.toHaveClass('scrollable')
  })

  test('is a decorative mirror: aria-hidden and not focusable', async () => {
    await mount()

    expect(track()).toHaveAttribute('aria-hidden', 'true')
    expect(track()).not.toHaveAttribute('tabindex')
    expect(thumb()).not.toHaveAttribute('tabindex')
  })
})

describe('ScrollBar thumb size and position', () => {
  test('thumb height is track * viewport / page', async () => {
    await mount()

    expect(thumbHeight()).toBeCloseTo(107.4, 5)
  })

  test('thumb sits at the top at scrollY 0', async () => {
    await mount()

    expect(thumbOffset()).toBe(0)
  })

  test('thumb position follows the scroll ratio over the thumb travel', async () => {
    await mount({y: 1200})

    // 1200 / 2400 of 429.6.
    expect(thumbOffset()).toBeCloseTo(214.8, 5)
  })

  test('thumb ends flush with the track at the bottom of the page', async () => {
    await mount({y: 2400})

    expect(thumbOffset() + thumbHeight()).toBeCloseTo(TRACK, 5)
  })

  test('thumb never gets shorter than 48 px', async () => {
    await mount({total: 100000})

    expect(thumbHeight()).toBe(48)
  })

  test('a scroll event moves the thumb, once per frame', async () => {
    await mount()
    expect(thumbOffset()).toBe(0)

    scrollY = 600
    window.dispatchEvent(new Event('scroll'))
    window.dispatchEvent(new Event('scroll'))
    window.dispatchEvent(new Event('scroll'))
    expect(frames).toHaveLength(1)
    flushFrames()
    await tick()

    expect(thumbOffset()).toBeCloseTo(107.4, 5)
  })

  test('a window resize re-measures the thumb', async () => {
    await mount()

    layout({viewport: 900, total: TOTAL})
    layoutTrack({height: 837})
    await measure()

    expect(thumbHeight()).toBeCloseTo((837 * 900) / TOTAL, 5)
  })

  test('removes its listeners when destroyed', async () => {
    const {unmount} = await mount()

    unmount()
    window.dispatchEvent(new Event('scroll'))
    window.dispatchEvent(new Event('resize'))

    expect(frames).toHaveLength(0)
  })
})

describe('ScrollBar content growth', () => {
  function stubResizeObserver() {
    const instances = []
    class StubResizeObserver {
      constructor(callback) {
        this.callback = callback
        this.observed = []
        this.disconnected = false
        instances.push(this)
      }

      observe(el) {
        this.observed.push(el)
      }

      disconnect() {
        this.disconnected = true
      }
    }
    vi.stubGlobal('ResizeObserver', StubResizeObserver)
    return instances
  }

  test('watches the document element and body, and re-measures when the page grows', async () => {
    const instances = stubResizeObserver()
    await mount()
    expect(instances).toHaveLength(1)
    expect(instances[0].observed).toEqual([document.documentElement, document.body])
    const before = thumbHeight()

    layout({total: TOTAL * 2})
    instances[0].callback([])
    flushFrames()
    await tick()

    expect(thumbHeight()).toBeLessThan(before)
    expect(thumbHeight()).toBeCloseTo((TRACK * VIEWPORT) / (TOTAL * 2), 5)
  })

  test('disconnects the observer when destroyed', async () => {
    const instances = stubResizeObserver()
    const {unmount} = await mount()

    unmount()

    expect(instances[0].disconnected).toBe(true)
  })

  test('works where ResizeObserver does not exist', async () => {
    vi.stubGlobal('ResizeObserver', undefined)

    await mount()

    expect(track()).toHaveClass('scrollable')
  })
})

describe('ScrollBar thumb drag', () => {
  const RANGE = TOTAL - VIEWPORT
  const TRAVEL = TRACK - (TRACK * VIEWPORT) / TOTAL

  test('dragging maps the pointer delta to delta * range / travel, and scrolls instantly', async () => {
    await mount()

    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1})
    await fireEvent.pointerMove(thumb(), {clientY: 200, buttons: 1, pointerId: 1})

    expectLastScroll((100 * RANGE) / TRAVEL)
  })

  test('the drag starts from the thumb position, not from the top', async () => {
    await mount({y: 1200})

    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 300, pointerId: 1})
    await fireEvent.pointerMove(thumb(), {clientY: 250, buttons: 1, pointerId: 1})

    // The thumb began 214.8 px down and moved up 50 px.
    expectLastScroll(((214.8 - 50) * RANGE) / TRAVEL)
  })

  test('the drag clamps to the top and the bottom of the page', async () => {
    await mount({y: 1200})
    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 300, pointerId: 1})

    await fireEvent.pointerMove(thumb(), {clientY: 100000, buttons: 1, pointerId: 1})
    expectLastScroll(RANGE)

    await fireEvent.pointerMove(thumb(), {clientY: -100000, buttons: 1, pointerId: 1})
    expectLastScroll(0)
  })

  test('a pointer move with no button held ends the drag (the release was missed) and does not scroll', async () => {
    await mount()
    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1})
    expect(track()).toHaveClass('dragging')

    await fireEvent.pointerMove(thumb(), {clientY: 200, buttons: 0, pointerId: 1})

    expect(track()).not.toHaveClass('dragging')
    expect(scrollTo).not.toHaveBeenCalled()
  })

  test('only lostpointercapture ends the drag', async () => {
    await mount()
    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1})
    expect(track()).toHaveClass('dragging')

    await fireEvent(thumb(), new Event('lostpointercapture'))

    expect(track()).not.toHaveClass('dragging')
    await fireEvent.pointerMove(thumb(), {clientY: 300, buttons: 1, pointerId: 1})
    expect(scrollTo).not.toHaveBeenCalled()
  })

  test('zero travel (the track is no longer than the thumb floor): the thumb fills the track and a drag does not scroll or produce NaN', async () => {
    await mount({height: 40})
    expect(thumbHeight()).toBe(40)
    expect(thumbOffset()).toBe(0)

    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1})
    await fireEvent.pointerMove(thumb(), {clientY: 300, buttons: 1, pointerId: 1})

    expect(scrollTo).not.toHaveBeenCalled()
    expect(Number.isNaN(thumbOffset())).toBe(false)
    expect(thumbOffset()).toBe(0)
  })

  test('the thumb takes the dragging class during the drag and loses it on release', async () => {
    await mount()

    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1})
    expect(track()).toHaveClass('dragging')

    await fireEvent.pointerUp(thumb(), {pointerId: 1})
    expect(track()).not.toHaveClass('dragging')
  })

  test('moves after release do not scroll', async () => {
    await mount()
    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1})
    await fireEvent.pointerUp(thumb(), {pointerId: 1})

    await fireEvent.pointerMove(thumb(), {clientY: 300, pointerId: 1})

    expect(scrollTo).not.toHaveBeenCalled()
  })

  test('a pointer cancel ends the drag', async () => {
    await mount()
    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1})

    await fireEvent.pointerCancel(thumb(), {pointerId: 1})

    expect(track()).not.toHaveClass('dragging')
  })

  test('a press on the thumb is cancelled (no text selection, no mouse events) and does not page the track', async () => {
    await mount()

    const notCancelled = fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1, cancelable: true})

    expect(await notCancelled).toBe(false)
    expect(scrollTo).not.toHaveBeenCalled()
  })

  test('a secondary button does not start a drag', async () => {
    await mount()

    await fireEvent.pointerDown(thumb(), {button: 2, clientY: 100, pointerId: 1})
    await fireEvent.pointerMove(thumb(), {clientY: 200, pointerId: 1})

    expect(track()).not.toHaveClass('dragging')
    expect(scrollTo).not.toHaveBeenCalled()
  })

  test('the drag re-reads the metrics, so a page that grew mid-drag keeps the thumb under the pointer', async () => {
    await mount()
    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 100, pointerId: 1})

    layout({total: TOTAL * 2})
    await fireEvent.pointerMove(thumb(), {clientY: 150, buttons: 1, pointerId: 1})

    const range = TOTAL * 2 - VIEWPORT
    const travel = TRACK - (TRACK * VIEWPORT) / (TOTAL * 2)
    expectLastScroll((50 * range) / travel)
  })
})

describe('ScrollBar track click', () => {
  test('a press below the thumb pages one track height down (not the window height, which would skip the bar strip)', async () => {
    await mount()

    await fireEvent.pointerDown(track(), {button: 0, clientY: 400, pointerId: 1})

    expect(scrollTo).toHaveBeenCalledTimes(1)
    expect(scrollTo).toHaveBeenCalledWith({top: TRACK, behavior: 'instant'})
  })

  test('a press above the thumb pages one track height up', async () => {
    await mount({y: 1800})

    // The thumb is at 322.2 in the track, so 42 + 322.2 on screen; 100 is above it.
    await fireEvent.pointerDown(track(), {button: 0, clientY: 100, pointerId: 1})

    expect(scrollTo).toHaveBeenCalledWith({top: 1800 - TRACK, behavior: 'instant'})
  })

  test('paging clamps to the top and the bottom of the page', async () => {
    await mount({y: 2300})
    await fireEvent.pointerDown(track(), {button: 0, clientY: 640, pointerId: 1})
    expectLastScroll(TOTAL - VIEWPORT)

    layout({y: 100})
    await measure()
    await fireEvent.pointerDown(track(), {button: 0, clientY: 49, pointerId: 1})
    expectLastScroll(0)
  })

  test('a press on the thumb itself does not page', async () => {
    await mount()

    await fireEvent.pointerDown(thumb(), {button: 0, clientY: 60, pointerId: 1})

    expect(scrollTo).not.toHaveBeenCalled()
  })

  test('a secondary button on the track does not page', async () => {
    await mount()

    await fireEvent.pointerDown(track(), {button: 2, clientY: 400, pointerId: 1})

    expect(scrollTo).not.toHaveBeenCalled()
  })
})
