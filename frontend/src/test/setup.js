import '@testing-library/jest-dom/vitest'
import {afterEach} from 'vitest'
import {__resetBrowser, __resetWailsEvents, __resetWindow} from './wails-runtime-mock.js'

// Listeners registered by a component that outlives its test must not leak into the next one.
// Browser.OpenURL calls and forced failures must not leak either. Window state (maximised, recorded calls) resets too.
afterEach(() => {
  __resetWailsEvents()
  __resetBrowser()
  __resetWindow()
})
