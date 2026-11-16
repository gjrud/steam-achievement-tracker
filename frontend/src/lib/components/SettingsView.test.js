import {cleanup, fireEvent, render, screen, waitFor} from '@testing-library/svelte'
import {afterEach, describe, expect, test, vi} from 'vitest'
import SettingsView from './SettingsView.svelte'
import {dashboardState} from '../../test/app-fixtures.js'

afterEach(() => {
  cleanup()
})

// SettingsView only renders with an active profile, so dashboardState is the fixture for every case here.
function stateWith(settings) {
  const state = dashboardState()
  if (settings === undefined) delete state.settings
  else state.settings = settings
  return state
}

function unvettedBox() {
  return screen.getByRole('switch', {name: 'Include unvetted apps'})
}

function playedFreeBox() {
  return screen.getByRole('switch', {name: 'Include played free games'})
}

describe('SettingsView library sync', () => {
  test('reflects state.settings in the switches', () => {
    render(SettingsView, {props: {state: stateWith({includeUnvettedApps: false, includePlayedFreeGames: true})}})

    expect(unvettedBox()).not.toBeChecked()
    expect(playedFreeBox()).toBeChecked()
  })

  test('checks both boxes when settings are absent', () => {
    render(SettingsView, {props: {state: stateWith(undefined)}})

    expect(unvettedBox()).toBeChecked()
    expect(playedFreeBox()).toBeChecked()
  })

  test('shows the hints and the next-sync note', () => {
    render(SettingsView, {props: {state: stateWith(undefined)}})

    expect(screen.getByText('Library sync')).toBeInTheDocument()
    expect(screen.getByText('Steam hides new, not-yet-vetted apps by default.')).toBeInTheDocument()
    expect(screen.getByText('When off, only purchased games are tracked and the Purchased counter equals All games.')).toBeInTheDocument()
    expect(screen.getByText('Changes apply on next sync. Games no longer returned are marked as not owned.')).toBeInTheDocument()
  })

  test('toggling a box calls the handler with both values', async () => {
    const onSetLibrarySettings = vi.fn(async () => {})
    render(SettingsView, {props: {state: stateWith(undefined), onSetLibrarySettings}})

    await fireEvent.click(unvettedBox())

    expect(onSetLibrarySettings).toHaveBeenCalledWith(false, true)
  })

  test('toggling the played-free box sends the unchanged unvetted value too', async () => {
    const onSetLibrarySettings = vi.fn(async () => {})
    render(SettingsView, {props: {state: stateWith(undefined), onSetLibrarySettings}})

    await fireEvent.click(playedFreeBox())

    expect(onSetLibrarySettings).toHaveBeenCalledWith(true, false)
  })

  // Not `disabled`: a disabled switch dims and loses focus mid-click.
  test('marks both switches busy while saving, stays enabled, and ignores clicks', async () => {
    const onSetLibrarySettings = vi.fn(async () => {})
    render(SettingsView, {props: {state: stateWith(undefined), savingSettings: true, onSetLibrarySettings}})

    expect(unvettedBox()).toHaveAttribute('aria-busy', 'true')
    expect(playedFreeBox()).toHaveAttribute('aria-busy', 'true')
    expect(unvettedBox()).toBeEnabled()

    await fireEvent.click(unvettedBox())

    expect(onSetLibrarySettings).not.toHaveBeenCalled()
    expect(unvettedBox()).toBeChecked()
  })

  // The parent reports failures through state (App catches them), so the stored state never changes here.
  test('snaps a box back to the stored value when the parent keeps the old state', async () => {
    const onSetLibrarySettings = vi.fn(async () => {})
    render(SettingsView, {props: {state: stateWith(undefined), onSetLibrarySettings}})

    await fireEvent.click(unvettedBox())

    expect(onSetLibrarySettings).toHaveBeenCalledWith(false, true)
    await waitFor(() => expect(unvettedBox()).toBeChecked())
  })
})

describe('SettingsView danger zone', () => {
  test('describes removing the profile and the two choices', () => {
    render(SettingsView, {props: {state: stateWith(undefined)}})

    expect(screen.getByRole('heading', {name: 'Remove active profile'})).toBeInTheDocument()
    expect(screen.getByText('Remove the active profile. You choose whether to also delete its data and the API key.')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Remove profile'})).toBeEnabled()
  })

  test('opens the confirmation through onClearData', async () => {
    const onClearData = vi.fn()
    render(SettingsView, {props: {state: stateWith(undefined), onClearData}})

    await fireEvent.click(screen.getByRole('button', {name: 'Remove profile'}))

    expect(onClearData).toHaveBeenCalledOnce()
  })

  test('shows Removing… and disables the button while clearing', () => {
    render(SettingsView, {props: {state: stateWith(undefined), clearingData: true}})

    expect(screen.getByRole('button', {name: 'Removing…'})).toBeDisabled()
  })
})
