import '@testing-library/jest-dom/vitest'
import {afterEach} from 'vitest'
import {__resetWailsEvents} from './wails-runtime-mock.js'

// Listeners registered by a component that outlives its test must not leak into the next one.
afterEach(() => {
  __resetWailsEvents()
})
