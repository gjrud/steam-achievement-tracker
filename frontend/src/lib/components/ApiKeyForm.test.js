import {cleanup, fireEvent, render, screen, waitFor} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import ApiKeyForm from './ApiKeyForm.svelte'
import {__failBrowserOpen, __openedUrls, RuntimeError} from '../../test/wails-runtime-mock.js'

const KEY_PAGE_URL = 'https://steamcommunity.com/dev/apikey'

function keyLink() {
  return screen.getByRole('link', {name: 'Get a Steam Web API key'})
}

afterEach(() => {
  cleanup()
})

function keyField() {
  return screen.getByLabelText('Steam Web API key')
}

function saveButton() {
  return screen.getByRole('button', {name: /^(Save key|Saving…)$/})
}

describe('ApiKeyForm', () => {
  test('renders a password field with autofill, capitalization and spellcheck off', () => {
    render(ApiKeyForm)

    const input = keyField()
    expect(input).toHaveAttribute('type', 'password')
    expect(input).toHaveAttribute('autocomplete', 'off')
    expect(input).toHaveAttribute('autocapitalize', 'off')
    expect(input).toHaveAttribute('autocorrect', 'off')
    expect(input).toHaveAttribute('spellcheck', 'false')
    expect(input).toHaveAttribute('placeholder', 'Paste key')
  })

  test('keeps the label screen-reader only unless showLabel is set', () => {
    const {unmount} = render(ApiKeyForm)
    expect(screen.getByText('Steam Web API key')).toHaveClass('sr-only')
    unmount()

    render(ApiKeyForm, {props: {showLabel: true}})
    expect(screen.getByText('Steam Web API key')).not.toHaveClass('sr-only')
  })

  test('does not take focus on mount unless autofocus is set', () => {
    render(ApiKeyForm)

    expect(keyField()).not.toHaveFocus()
  })

  test('autofocus moves focus into the field on mount', async () => {
    render(ApiKeyForm, {props: {autofocus: true}})

    await waitFor(() => expect(keyField()).toHaveFocus())
  })

  test('refocuses the field after a failed save, once the field is enabled again', async () => {
    const onSave = vi.fn(async () => {
      // Stand-in for WebKit dropping focus from a field that was disabled during the save.
      keyField().blur()
      throw new RuntimeError('Secret Service is locked')
    })
    render(ApiKeyForm, {props: {onSave}})

    await userEvent.setup().type(keyField(), 'abc123')
    await fireEvent.submit(keyField().closest('form'))

    expect(await screen.findByText('Secret Service is locked')).toBeInTheDocument()
    expect(keyField()).toHaveFocus()
  })

  test('shows a failed save error as an alert', async () => {
    const user = userEvent.setup()
    render(ApiKeyForm, {props: {onSave: vi.fn(async () => {
      throw new RuntimeError('Secret Service is locked')
    })}})

    await user.type(keyField(), 'abc123')
    await user.click(saveButton())

    expect(await screen.findByRole('alert')).toHaveTextContent('Secret Service is locked')
  })

  test('Save key stays disabled until the field holds a non-blank key', async () => {
    const user = userEvent.setup()
    render(ApiKeyForm)

    expect(saveButton()).toBeDisabled()
    await user.type(keyField(), '   ')
    expect(saveButton()).toBeDisabled()
    await user.type(keyField(), 'k')
    expect(saveButton()).toBeEnabled()
  })

  test('submits the trimmed key when Save key is clicked', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn(async () => {})
    render(ApiKeyForm, {props: {onSave}})

    await user.type(keyField(), '  abc123  ')
    await user.click(saveButton())

    expect(onSave).toHaveBeenCalledOnce()
    expect(onSave).toHaveBeenCalledWith('abc123')
  })

  test('Enter in the field submits the trimmed key', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn(async () => {})
    render(ApiKeyForm, {props: {onSave}})

    await user.type(keyField(), ' abc123 {Enter}')

    expect(onSave).toHaveBeenCalledOnce()
    expect(onSave).toHaveBeenCalledWith('abc123')
  })

  test('clears the field after a successful save and shows no error', async () => {
    const user = userEvent.setup()
    render(ApiKeyForm, {props: {onSave: vi.fn(async () => {})}})

    await user.type(keyField(), 'abc123')
    await user.click(saveButton())

    expect(keyField()).toHaveValue('')
    expect(document.querySelector('.error-text')).toBeNull()
    expect(document.body.textContent).not.toContain('abc123')
  })

  test('shows the error from a failed save, clears the field, and never renders the key', async () => {
    const user = userEvent.setup()
    // The rejection is caught by the form, so the test completes without an unhandled rejection.
    render(ApiKeyForm, {props: {onSave: vi.fn(async () => {
      throw new RuntimeError('Secret Service is locked')
    })}})

    await user.type(keyField(), 'abc123')
    await user.click(saveButton())

    expect(await screen.findByText('Secret Service is locked')).toBeInTheDocument()
    expect(keyField()).toHaveValue('')
    expect(document.body.textContent).not.toContain('abc123')
  })

  test('a later successful save clears the earlier error', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
      .mockRejectedValueOnce(new RuntimeError('Secret Service is locked'))
      .mockResolvedValueOnce(undefined)
    render(ApiKeyForm, {props: {onSave}})

    await user.type(keyField(), 'first')
    await user.click(saveButton())
    expect(await screen.findByText('Secret Service is locked')).toBeInTheDocument()

    await user.type(keyField(), 'second')
    await user.click(saveButton())

    expect(onSave).toHaveBeenLastCalledWith('second')
    expect(screen.queryByText('Secret Service is locked')).not.toBeInTheDocument()
  })

  test('while saving, shows Saving…, disables the field and button, and ignores submit', async () => {
    const onSave = vi.fn(async () => {})
    render(ApiKeyForm, {props: {onSave, saving: true}})

    expect(saveButton()).toHaveTextContent('Saving…')
    expect(saveButton()).toBeDisabled()
    expect(keyField()).toBeDisabled()

    await fireEvent.submit(keyField().closest('form'))
    expect(onSave).not.toHaveBeenCalled()
  })

  test('when disabled by the parent, blocks the field, the button and submit', async () => {
    const onSave = vi.fn(async () => {})
    render(ApiKeyForm, {props: {onSave, disabled: true}})

    expect(saveButton()).toHaveTextContent('Save key')
    expect(saveButton()).toBeDisabled()
    expect(keyField()).toBeDisabled()

    await fireEvent.submit(keyField().closest('form'))
    expect(onSave).not.toHaveBeenCalled()
  })

  test('renders a link to the Steam key page with a real href', () => {
    render(ApiKeyForm)

    expect(keyLink()).toHaveAttribute('href', KEY_PAGE_URL)
    expect(keyLink()).toHaveAttribute('rel', 'noopener noreferrer')
  })

  test('shows the localhost domain note next to the key-page link, described by the link', () => {
    render(ApiKeyForm)

    const note = screen.getByText('When Steam asks for a domain name, enter localhost.')
    expect(note).toBeInTheDocument()
    expect(note).toHaveClass('muted')
    expect(keyLink()).toHaveAccessibleDescription('When Steam asks for a domain name, enter localhost.')
  })

  test('shows the localhost note on every context where the link shows', () => {
    const {unmount} = render(ApiKeyForm, {props: {saving: true}})
    expect(screen.getByText('enter localhost.', {exact: false})).toBeInTheDocument()
    unmount()

    render(ApiKeyForm, {props: {disabled: true, autofocus: true, showLabel: true}})
    expect(screen.getByText('enter localhost.', {exact: false})).toBeInTheDocument()
  })

  test('two forms on screen each describe their own link with their own note', () => {
    render(ApiKeyForm)
    render(ApiKeyForm)

    const links = screen.getAllByRole('link', {name: 'Get a Steam Web API key'})
    expect(links).toHaveLength(2)
    expect(links[0].getAttribute('aria-describedby')).not.toBe(links[1].getAttribute('aria-describedby'))
    links.forEach((link) => expect(link).toHaveAccessibleDescription('When Steam asks for a domain name, enter localhost.'))
  })

  test('clicking the link opens the key page once through the runtime and prevents webview navigation', async () => {
    render(ApiKeyForm)

    // dispatchEvent (not user.click) so the returned event reports whether default navigation was prevented.
    const event = new MouseEvent('click', {bubbles: true, cancelable: true})
    keyLink().dispatchEvent(event)

    expect(event.defaultPrevented).toBe(true)
    await waitFor(() => expect(__openedUrls()).toEqual([KEY_PAGE_URL]))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('shows the fallback message when the browser cannot be opened, without an unhandled rejection', async () => {
    __failBrowserOpen(new Error('no browser'))
    const user = userEvent.setup()
    render(ApiKeyForm)

    await user.click(keyLink())

    expect(await screen.findByRole('alert')).toHaveTextContent(`Could not open the browser. Visit ${KEY_PAGE_URL}`)
    expect(__openedUrls()).toEqual([KEY_PAGE_URL])
  })

  test('the link stays clickable while saving or disabled', async () => {
    const user = userEvent.setup()
    const {unmount} = render(ApiKeyForm, {props: {saving: true}})

    await user.click(keyLink())
    expect(__openedUrls()).toEqual([KEY_PAGE_URL])
    unmount()

    render(ApiKeyForm, {props: {disabled: true}})
    await user.click(keyLink())
    expect(__openedUrls()).toEqual([KEY_PAGE_URL, KEY_PAGE_URL])
  })
})
