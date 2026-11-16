import {cleanup, fireEvent, render, screen} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import ClearDataConfirmModal from './ClearDataConfirmModal.svelte'

const CONFIRM_TEXT = '76561198000000000'

const OUTCOME = {
  none: 'Nothing is deleted. The profile is disconnected; connecting the same Steam profile again restores everything.',
  data: "The profile and its data are deleted. This can't be undone. The API key is kept.",
  key: "The saved key is deleted; profile data is kept. You'll need to enter a key to sync again.",
  both: "The profile, its data and the saved key are deleted. This can't be undone."
}

const BUTTON = {
  none: 'Remove profile',
  data: 'Remove profile and data',
  key: 'Remove profile and key',
  both: 'Remove profile, data and key',
  busy: 'Removing…'
}

afterEach(() => {
  cleanup()
})

function profileDataBox() {
  return screen.getByRole('switch', {name: 'Delete profile data'})
}

function apiKeyBox() {
  return screen.getByRole('switch', {name: 'Delete API key'})
}

function confirmButton() {
  return screen.getByRole('button', {name: /^(Remove profile|Remove profile and data|Remove profile and key|Remove profile, data and key|Removing…)$/})
}

function typeConfirm(value) {
  return userEvent.type(screen.getByLabelText(/Type this exact value/), value)
}

function renderModal(props = {}) {
  return render(ClearDataConfirmModal, {props: {confirmText: CONFIRM_TEXT, ...props}})
}

describe('ClearDataConfirmModal toggles', () => {
  test('both toggles start off, the button removes the profile, and the outcome says nothing is deleted', () => {
    renderModal()

    expect(profileDataBox()).not.toBeChecked()
    expect(apiKeyBox()).not.toBeChecked()
    expect(screen.getByRole('button', {name: BUTTON.none})).toBeInTheDocument()
    expect(screen.getByText(OUTCOME.none)).toBeInTheDocument()
    expect(screen.getByText('This removes the active profile from the app. Nothing else is deleted unless you turn it on.')).toBeInTheDocument()
  })

  test('the dialog is titled for removing the active profile', () => {
    renderModal()

    expect(screen.getByRole('dialog', {name: 'Remove active profile?'})).toBeInTheDocument()
  })

  test('the toggles are real labelled switches with hints linked through aria-describedby', () => {
    renderModal()

    expect(profileDataBox()).toHaveAttribute('type', 'checkbox')
    expect(profileDataBox()).toHaveAccessibleDescription('Tracked games, achievements, sync history, settings and cached images.')
    expect(apiKeyBox()).toHaveAccessibleDescription("Removes the saved key from the system keyring (Secret Service). The app can't sync until you enter a key again.")
  })

  test.each([
    {label: 'profile data only', toggles: ['profile'], button: BUTTON.data, outcome: OUTCOME.data},
    {label: 'API key only', toggles: ['key'], button: BUTTON.key, outcome: OUTCOME.key},
    {label: 'both toggles', toggles: ['profile', 'key'], button: BUTTON.both, outcome: OUTCOME.both}
  ])('switching $label on changes the button and the outcome', async ({toggles, button, outcome}) => {
    renderModal()

    for (const toggle of toggles) {
      await fireEvent.click(toggle === 'profile' ? profileDataBox() : apiKeyBox())
    }

    expect(screen.getByRole('button', {name: button})).toBeInTheDocument()
    expect(screen.getByText(outcome)).toBeInTheDocument()
  })

  test('the key-only button never reads Clear data', async () => {
    renderModal()

    await fireEvent.click(apiKeyBox())

    expect(screen.queryByRole('button', {name: /Clear data/})).not.toBeInTheDocument()
  })

  test('turning both toggles back off returns to Remove profile', async () => {
    renderModal()

    await fireEvent.click(profileDataBox())
    expect(screen.getByRole('button', {name: BUTTON.data})).toBeInTheDocument()
    await fireEvent.click(profileDataBox())

    expect(screen.getByRole('button', {name: BUTTON.none})).toBeInTheDocument()
    expect(screen.getByText(OUTCOME.none)).toBeInTheDocument()
  })

  test('the outcome is styled as a destructive callout only while a switch is on', async () => {
    renderModal()
    const outcome = screen.getByText(OUTCOME.none)
    expect(outcome).not.toHaveClass('destructive')

    await fireEvent.click(apiKeyBox())

    expect(screen.getByText(OUTCOME.key)).toHaveClass('destructive')
  })

  test('the toggles are disabled while clearing, and the button reads Removing…', () => {
    renderModal({clearing: true})

    expect(profileDataBox()).toBeDisabled()
    expect(apiKeyBox()).toBeDisabled()
    expect(screen.getByRole('button', {name: BUTTON.busy})).toBeDisabled()
  })

  test('the toggles are enabled when not clearing', () => {
    renderModal()

    expect(profileDataBox()).toBeEnabled()
    expect(apiKeyBox()).toBeEnabled()
  })
})

describe('ClearDataConfirmModal onConfirm payload', () => {
  test.each([
    {label: 'both off', toggles: [], payload: {deleteProfileData: false, deleteAPIKey: false}},
    {label: 'profile data only', toggles: ['profile'], payload: {deleteProfileData: true, deleteAPIKey: false}},
    {label: 'API key only', toggles: ['key'], payload: {deleteProfileData: false, deleteAPIKey: true}},
    {label: 'both on', toggles: ['profile', 'key'], payload: {deleteProfileData: true, deleteAPIKey: true}}
  ])('passes $label to onConfirm from the button', async ({toggles, payload}) => {
    const onConfirm = vi.fn()
    renderModal({onConfirm})

    for (const toggle of toggles) {
      await fireEvent.click(toggle === 'profile' ? profileDataBox() : apiKeyBox())
    }
    await typeConfirm(CONFIRM_TEXT)
    await userEvent.click(confirmButton())

    expect(onConfirm).toHaveBeenCalledOnce()
    expect(onConfirm).toHaveBeenCalledWith(payload)
  })

  test.each([
    {label: 'both off', toggles: [], payload: {deleteProfileData: false, deleteAPIKey: false}},
    {label: 'both on', toggles: ['profile', 'key'], payload: {deleteProfileData: true, deleteAPIKey: true}}
  ])('passes $label to onConfirm from Enter', async ({toggles, payload}) => {
    const onConfirm = vi.fn()
    renderModal({onConfirm})

    for (const toggle of toggles) {
      await fireEvent.click(toggle === 'profile' ? profileDataBox() : apiKeyBox())
    }
    await typeConfirm(`${CONFIRM_TEXT}{Enter}`)

    expect(onConfirm).toHaveBeenCalledOnce()
    expect(onConfirm).toHaveBeenCalledWith(payload)
  })
})

describe('ClearDataConfirmModal typed confirmation', () => {
  test.each([
    {label: 'both off', toggles: []},
    {label: 'profile data only', toggles: ['profile']},
    {label: 'API key only', toggles: ['key']},
    {label: 'both on', toggles: ['profile', 'key']}
  ])('still requires the exact value for $label', async ({toggles}) => {
    const onConfirm = vi.fn()
    renderModal({onConfirm})

    for (const toggle of toggles) {
      await fireEvent.click(toggle === 'profile' ? profileDataBox() : apiKeyBox())
    }
    await typeConfirm('7656119800000000')

    expect(confirmButton()).toBeDisabled()
    await userEvent.keyboard('{Enter}')
    expect(onConfirm).not.toHaveBeenCalled()
  })

  test('toggles do not satisfy the confirmation on their own', async () => {
    const onConfirm = vi.fn()
    renderModal({onConfirm})

    await fireEvent.click(profileDataBox())
    await fireEvent.click(apiKeyBox())

    expect(confirmButton()).toBeDisabled()
    expect(onConfirm).not.toHaveBeenCalled()
  })

  test('the typed input is described by the confirmation value it must match', () => {
    renderModal()

    expect(screen.getByLabelText(/Type this exact value/)).toHaveAccessibleDescription(CONFIRM_TEXT)
  })
})

describe('ClearDataConfirmModal focus', () => {
  test('moves focus into the dialog on open', () => {
    renderModal()

    expect(screen.getByRole('dialog')).toHaveFocus()
  })

  test('returns focus to the element that opened it on close', () => {
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const {unmount} = renderModal()
    expect(opener).not.toHaveFocus()

    unmount()

    expect(opener).toHaveFocus()
    opener.remove()
  })

  test('does not move focus to an opener that has left the page', () => {
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const {unmount} = renderModal()
    opener.remove()

    unmount()

    expect(document.activeElement).toBe(document.body)
  })
})

describe('ClearDataConfirmModal state reset', () => {
  test('a reopened modal starts with both toggles off', async () => {
    const {unmount} = renderModal()
    await fireEvent.click(profileDataBox())
    await fireEvent.click(apiKeyBox())
    expect(profileDataBox()).toBeChecked()
    expect(apiKeyBox()).toBeChecked()
    unmount()

    renderModal()

    expect(profileDataBox()).not.toBeChecked()
    expect(apiKeyBox()).not.toBeChecked()
    expect(screen.getByRole('button', {name: BUTTON.none})).toBeInTheDocument()
  })
})

describe('ClearDataConfirmModal cancel', () => {
  test('Cancel calls onCancel and never onConfirm', async () => {
    const onCancel = vi.fn()
    const onConfirm = vi.fn()
    renderModal({onCancel, onConfirm})

    await fireEvent.click(profileDataBox())
    await fireEvent.click(screen.getByRole('button', {name: 'Cancel'}))

    expect(onCancel).toHaveBeenCalledOnce()
    expect(onConfirm).not.toHaveBeenCalled()
  })
})
