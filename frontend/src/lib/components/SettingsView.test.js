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

describe('SettingsView Steam Web API key', () => {
  function replaceButton() {
    return screen.getByRole('button', {name: 'Replace key'})
  }

  function keyField() {
    return screen.getByLabelText('Steam Web API key')
  }

  test('shows the stored status, and opens the key form only on Replace key with focus in the field', async () => {
    render(SettingsView, {props: {state: stateWith(undefined)}})

    expect(screen.getByText('Stored in Secret Service.')).toBeInTheDocument()
    expect(screen.queryByLabelText('Steam Web API key')).not.toBeInTheDocument()

    await fireEvent.click(replaceButton())

    expect(keyField()).toHaveAttribute('type', 'password')
    expect(replaceButton()).toHaveAttribute('aria-expanded', 'true')
    await waitFor(() => expect(keyField()).toHaveFocus())
  })

  test('saves the trimmed key through onSaveKey, closes the form, confirms the save, and returns focus to Replace key', async () => {
    const onSaveKey = vi.fn(async () => {})
    render(SettingsView, {props: {state: stateWith(undefined), onSaveKey}})

    await fireEvent.click(replaceButton())
    await fireEvent.input(keyField(), {target: {value: '  new-key  '}})
    await fireEvent.click(screen.getByRole('button', {name: 'Save key'}))

    expect(onSaveKey).toHaveBeenCalledWith('new-key')
    await waitFor(() => expect(screen.queryByLabelText('Steam Web API key')).not.toBeInTheDocument())
    expect(screen.getByRole('status')).toHaveTextContent('Key saved.')
    expect(replaceButton()).toHaveFocus()
  })

  test('clears the Key saved. status when the form is opened again', async () => {
    const onSaveKey = vi.fn(async () => {})
    render(SettingsView, {props: {state: stateWith(undefined), onSaveKey}})

    await fireEvent.click(replaceButton())
    await fireEvent.input(keyField(), {target: {value: 'new-key'}})
    await fireEvent.click(screen.getByRole('button', {name: 'Save key'}))
    expect(await screen.findByRole('status')).toHaveTextContent('Key saved.')

    await fireEvent.click(replaceButton())

    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(keyField()).toBeInTheDocument()
  })

  test('keeps the form open with the error when the save fails', async () => {
    const onSaveKey = vi.fn(async () => {
      throw new Error('Secret Service is locked')
    })
    render(SettingsView, {props: {state: stateWith(undefined), onSaveKey}})

    await fireEvent.click(replaceButton())
    await fireEvent.input(keyField(), {target: {value: 'new-key'}})
    await fireEvent.click(screen.getByRole('button', {name: 'Save key'}))

    expect(await screen.findByText('Secret Service is locked')).toBeInTheDocument()
    expect(keyField()).toBeInTheDocument()
    expect(keyField()).toHaveValue('')
    expect(document.body.textContent).not.toContain('new-key')
  })

  test('shows Saving…, refuses submit, and disables Replace key while a key save is pending', async () => {
    const onSaveKey = vi.fn(async () => {})
    const state = stateWith(undefined)
    const {rerender} = render(SettingsView, {props: {state, onSaveKey}})

    await fireEvent.click(replaceButton())
    expect(replaceButton()).toBeEnabled()

    await rerender({state, savingKey: true, onSaveKey})

    expect(replaceButton()).toBeDisabled()
    expect(screen.getByRole('button', {name: 'Saving…'})).toBeDisabled()
    expect(keyField()).toBeDisabled()
    await fireEvent.submit(keyField().closest('form'))
    expect(onSaveKey).not.toHaveBeenCalled()
  })
})
