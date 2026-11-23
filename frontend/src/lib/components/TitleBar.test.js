import {cleanup, render, screen} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import {Window} from '@wailsio/runtime'
import {__emitWailsEvent, __listenerCount, __setMaximised, __windowCalls} from '../../test/wails-runtime-mock.js'
import TitleBar from './TitleBar.svelte'

const bar = () => document.querySelector('.title-bar')

function renderBar(props = {}) {
  const onActiveViewChange = vi.fn()
  const onBarPress = vi.fn()
  const result = render(TitleBar, {props: {dashboard: true, activeView: 'suggestions', onActiveViewChange, onBarPress, ...props}})
  return {...result, onActiveViewChange, onBarPress}
}

afterEach(() => {
  cleanup()
  // Restores the IsMaximised spies, so a later test does not count an earlier test's reads.
  vi.restoreAllMocks()
})

describe('TitleBar DOM and ARIA', () => {
  test('the dashboard shows four tabs in a nav labelled Dashboard views, and a Settings gear', () => {
    renderBar()

    const nav = screen.getByRole('navigation', {name: 'Dashboard views'})
    const tabs = [...nav.querySelectorAll('button')].map((button) => button.textContent)
    expect(tabs).toEqual(['Not completed', 'Completed', 'Sync errors', 'Disabled'])
    expect(screen.getByRole('button', {name: 'Settings'})).toBeInTheDocument()
    // Folder tabs are not an ARIA tablist (D12).
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument()
  })

  test('the active tab and the gear while Settings is open carry aria-current="page"; the others do not', () => {
    const {rerender} = renderBar({activeView: 'suggestions'})

    expect(screen.getByRole('button', {name: 'Not completed'})).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('button', {name: 'Completed'})).not.toHaveAttribute('aria-current')
    expect(screen.getByRole('button', {name: 'Settings'})).not.toHaveAttribute('aria-current')

    rerender({dashboard: true, activeView: 'settings', onActiveViewChange: vi.fn(), onBarPress: vi.fn()})

    expect(screen.getByRole('button', {name: 'Not completed'})).not.toHaveAttribute('aria-current')
    expect(screen.getByRole('button', {name: 'Settings'})).toHaveAttribute('aria-current', 'page')
  })

  test('the window controls have accessible names and tabindex -1 (keyboard users use OS shortcuts, D13)', () => {
    renderBar()

    for (const name of ['Minimize', 'Maximize', 'Close']) {
      expect(screen.getByRole('button', {name})).toHaveAttribute('tabindex', '-1')
      expect(screen.getByRole('button', {name})).toHaveAttribute('type', 'button')
    }
  })

  test('the icon is the app icon and decorative: hidden from assistive tech, empty alt, not HTML-draggable', () => {
    renderBar()

    const icon = document.querySelector('.title-icon')
    expect(icon).toHaveAttribute('aria-hidden', 'true')
    const img = icon.querySelector('img')
    expect(img).toHaveAttribute('alt', '')
    expect(img).toHaveAttribute('draggable', 'false')
    expect(img.getAttribute('src')).toMatch(/appicon\.svg$/)
  })

  test('the disabled-while-modal state disables the tabs and the gear but not the window controls (D14)', () => {
    renderBar({tabsDisabled: true})

    expect(screen.getByRole('button', {name: 'Not completed'})).toBeDisabled()
    expect(screen.getByRole('button', {name: 'Completed'})).toBeDisabled()
    expect(screen.getByRole('button', {name: 'Settings'})).toBeDisabled()
    expect(screen.getByRole('button', {name: 'Minimize'})).toBeEnabled()
    expect(screen.getByRole('button', {name: 'Close'})).toBeEnabled()
  })

  test('off the dashboard the bar shows the icon and the window controls only', () => {
    renderBar({dashboard: false})

    expect(screen.queryByRole('navigation', {name: 'Dashboard views'})).not.toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Settings'})).not.toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Minimize'})).toBeInTheDocument()
    expect(document.querySelector('.title-icon')).toBeInTheDocument()
  })
})

describe('TitleBar keyboard', () => {
  test('Tab reaches the tabs and the gear in order, and skips the window controls', async () => {
    renderBar()
    const user = userEvent.setup()

    const order = []
    for (let i = 0; i < 5; i++) {
      await user.tab()
      order.push(document.activeElement.textContent || document.activeElement.getAttribute('aria-label'))
    }

    expect(order).toEqual(['Not completed', 'Completed', 'Sync errors', 'Disabled', 'Settings'])
    await user.tab()
    expect(document.activeElement).not.toBe(screen.getByRole('button', {name: 'Minimize'}))
  })

  test('Enter on a focused tab or the gear changes the view', async () => {
    const {onActiveViewChange} = renderBar()
    const user = userEvent.setup()

    screen.getByRole('button', {name: 'Sync errors'}).focus()
    await user.keyboard('{Enter}')
    expect(onActiveViewChange).toHaveBeenLastCalledWith('warnings')

    screen.getByRole('button', {name: 'Settings'}).focus()
    await user.keyboard('{Enter}')
    expect(onActiveViewChange).toHaveBeenLastCalledWith('settings')
  })
})

describe('TitleBar clicks and bar press', () => {
  test('a tab click reports its view, and the mousedown that comes first does not cancel the click', async () => {
    const {onActiveViewChange, onBarPress} = renderBar()

    await userEvent.setup().click(screen.getByRole('button', {name: 'Disabled'}))

    expect(onBarPress).toHaveBeenCalledTimes(1)
    expect(onActiveViewChange).toHaveBeenCalledWith('disabled')
  })

  test('the gear reports the settings view', async () => {
    const {onActiveViewChange} = renderBar()

    await userEvent.setup().click(screen.getByRole('button', {name: 'Settings'}))

    expect(onActiveViewChange).toHaveBeenCalledWith('settings')
  })

  test('a mousedown on the bar cancels its default (focus stays in the search box or modal input, E14) and reports the press', () => {
    const {onBarPress} = renderBar()
    const event = new MouseEvent('mousedown', {bubbles: true, cancelable: true})

    bar().dispatchEvent(event)

    expect(event.defaultPrevented).toBe(true)
    expect(onBarPress).toHaveBeenCalledTimes(1)
  })

})

describe('TitleBar window controls', () => {
  test('Minimize and Close call the window; Maximize toggles it', async () => {
    renderBar()
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', {name: 'Minimize'}))
    await user.click(screen.getByRole('button', {name: 'Close'}))
    await user.click(screen.getByRole('button', {name: 'Maximize'}))

    expect(__windowCalls()).toEqual(['Minimise', 'Close', 'ToggleMaximise'])
  })

  test('the maximise button reads Restore once the window is maximised, from the state read at mount', async () => {
    __setMaximised(true)
    renderBar()

    expect(await screen.findByRole('button', {name: 'Restore'})).toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Maximize'})).not.toBeInTheDocument()
  })

  test('the maximise button title is Maximize, then Restore once the window is maximised', async () => {
    renderBar()
    const maximise = await screen.findByRole('button', {name: 'Maximize'})
    expect(maximise).toHaveAttribute('title', 'Maximize')

    __setMaximised(true)
    __emitWailsEvent('common:WindowMaximise')

    const restore = await screen.findByRole('button', {name: 'Restore'})
    expect(restore).toHaveAttribute('title', 'Restore')
  })

  test('a maximise event re-reads the window after the debounce, so the label follows the state', async () => {
    renderBar()
    expect(await screen.findByRole('button', {name: 'Maximize'})).toBeInTheDocument()

    __setMaximised(true)
    __emitWailsEvent('common:WindowMaximise')
    // The read waits for the quiet period, so the label is not yet updated synchronously.
    expect(screen.getByRole('button', {name: 'Maximize'})).toBeInTheDocument()

    expect(await screen.findByRole('button', {name: 'Restore'})).toBeInTheDocument()
  })

  test('WindowUnMaximise and WindowRestore also re-read the state (Windows emits WindowRestore during resize and snap)', async () => {
    __setMaximised(true)
    renderBar()
    await screen.findByRole('button', {name: 'Restore'})

    __setMaximised(false)
    __emitWailsEvent('common:WindowUnMaximise')
    expect(await screen.findByRole('button', {name: 'Maximize'})).toBeInTheDocument()

    __setMaximised(true)
    __emitWailsEvent('common:WindowRestore')
    expect(await screen.findByRole('button', {name: 'Restore'})).toBeInTheDocument()
  })

  test('a burst of maximise events costs one read, made 120 ms after the last event', async () => {
    vi.useFakeTimers()
    try {
      renderBar()
      await vi.advanceTimersByTimeAsync(0)
      const isMaximised = vi.spyOn(Window, 'IsMaximised')

      __emitWailsEvent('common:WindowMaximise')
      __emitWailsEvent('common:WindowRestore')
      __emitWailsEvent('common:WindowMaximise')

      await vi.advanceTimersByTimeAsync(119)
      expect(isMaximised).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1)
      expect(isMaximised).toHaveBeenCalledTimes(1)

      // Nothing else is pending: the burst produced exactly one read.
      await vi.advanceTimersByTimeAsync(500)
      expect(isMaximised).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  test('a later event restarts the quiet period', async () => {
    vi.useFakeTimers()
    try {
      renderBar()
      await vi.advanceTimersByTimeAsync(0)
      const isMaximised = vi.spyOn(Window, 'IsMaximised')

      __emitWailsEvent('common:WindowMaximise')
      await vi.advanceTimersByTimeAsync(100)
      __emitWailsEvent('common:WindowUnMaximise')
      await vi.advanceTimersByTimeAsync(119)
      expect(isMaximised).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1)
      expect(isMaximised).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  test('unmounting cancels a pending read and drops the listeners', async () => {
    vi.useFakeTimers()
    try {
      const {unmount} = renderBar()
      await vi.advanceTimersByTimeAsync(0)
      const isMaximised = vi.spyOn(Window, 'IsMaximised')

      __emitWailsEvent('common:WindowMaximise')
      unmount()
      await vi.advanceTimersByTimeAsync(1000)

      expect(isMaximised).not.toHaveBeenCalled()
      expect(__listenerCount('common:WindowMaximise')).toBe(0)
      expect(__listenerCount('common:WindowUnMaximise')).toBe(0)
      expect(__listenerCount('common:WindowRestore')).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })

  test('unmounting removes the three maximise listeners', async () => {
    const {unmount} = renderBar()
    await screen.findByRole('button', {name: 'Maximize'})
    expect(__listenerCount('common:WindowMaximise')).toBe(1)
    expect(__listenerCount('common:WindowUnMaximise')).toBe(1)
    expect(__listenerCount('common:WindowRestore')).toBe(1)

    unmount()

    expect(__listenerCount('common:WindowMaximise')).toBe(0)
    expect(__listenerCount('common:WindowUnMaximise')).toBe(0)
    expect(__listenerCount('common:WindowRestore')).toBe(0)
  })
})
