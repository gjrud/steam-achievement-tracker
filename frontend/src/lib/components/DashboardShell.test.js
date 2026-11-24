import {cleanup, render, screen} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import DashboardShell from './DashboardShell.svelte'

afterEach(() => {
  cleanup()
})

// Lists are empty, so none of the formatting helpers the shell passes down to the cards is called.
const noop = () => ''
function shellProps(overrides = {}) {
  return {
    state: {},
    gameTitle: noop,
    gameWarning: noop,
    formatPlaytime: noop,
    toneClass: noop,
    tagTone: noop,
    tagLabel: noop,
    integer: (n) => String(n ?? 0),
    pct: noop,
    cardProgress: noop,
    completionLabel: noop,
    missingAvgUnlockLabel: noop,
    hasAchievements: () => true,
    shouldShowSyncErrorMessage: () => false,
    showDisabledReasonLabel: noop,
    ...overrides
  }
}

const searchBox = () => screen.getByPlaceholderText('Search games in this view')
const clearButton = () => screen.queryByRole('button', {name: 'Clear search'})

describe('DashboardShell search clear button', () => {
  test('is hidden while the search is empty and appears once text is typed', async () => {
    const user = userEvent.setup()
    render(DashboardShell, {props: shellProps()})

    expect(clearButton()).not.toBeInTheDocument()
    await user.type(searchBox(), 'had')

    expect(clearButton()).toBeInTheDocument()
  })

  test('is a labelled type=button with a decorative 16px icon', async () => {
    const user = userEvent.setup()
    render(DashboardShell, {props: shellProps()})
    await user.type(searchBox(), 'x')

    const button = screen.getByRole('button', {name: 'Clear search'})
    expect(button).toHaveAttribute('type', 'button')
    const svg = button.querySelector('svg')
    expect(svg).toHaveAttribute('aria-hidden', 'true')
    expect(svg).toHaveAttribute('focusable', 'false')
    expect(svg).toHaveAttribute('viewBox', '0 0 16 16')
    expect(svg).toHaveAttribute('width', '16')
    expect(svg).toHaveAttribute('height', '16')
  })

  test('shows for a search supplied by the parent', () => {
    render(DashboardShell, {props: shellProps({viewSearch: 'hades'})})

    expect(searchBox()).toHaveValue('hades')
    expect(clearButton()).toBeInTheDocument()
  })

  test('a parent reset to an empty search empties the input and hides the button', async () => {
    const {rerender} = render(DashboardShell, {props: shellProps({viewSearch: 'hades'})})

    await rerender({viewSearch: ''})

    expect(searchBox()).toHaveValue('')
    expect(clearButton()).not.toBeInTheDocument()
  })

  test('a parent-supplied search fills the input and shows the button', async () => {
    const {rerender} = render(DashboardShell, {props: shellProps({viewSearch: ''})})
    expect(clearButton()).not.toBeInTheDocument()

    await rerender({viewSearch: 'hades'})

    expect(searchBox()).toHaveValue('hades')
    expect(clearButton()).toBeInTheDocument()
  })

  test('click clears the text, reports it to the parent, focuses the input and hides itself', async () => {
    const onViewSearch = vi.fn()
    const user = userEvent.setup()
    render(DashboardShell, {props: shellProps({onViewSearch})})
    await user.type(searchBox(), 'had')
    onViewSearch.mockClear()

    await user.click(clearButton())

    expect(onViewSearch).toHaveBeenCalledTimes(1)
    expect(onViewSearch).toHaveBeenCalledWith('')
    expect(searchBox()).toHaveValue('')
    expect(searchBox()).toHaveFocus()
    expect(clearButton()).not.toBeInTheDocument()
  })

  test('is not rendered on Settings, where there is no search input', () => {
    render(DashboardShell, {props: shellProps({activeView: 'settings', viewSearch: 'hades'})})

    expect(screen.queryByPlaceholderText('Search games in this view')).not.toBeInTheDocument()
    expect(clearButton()).not.toBeInTheDocument()
  })
})
