import {describe, expect, test} from 'vitest'
import {RuntimeError} from '../test/wails-runtime-mock.js'
import {AppMessage, formatDateTime, formatMessage, formatPlaytime, formatRelativeTime, genericMessage, integer, pct} from './format.js'

describe('formatMessage', () => {
  test('shows the message of a RuntimeError, the backend sentence of a bound method', () => {
    expect(formatMessage(new RuntimeError('No Steam profile matches that custom name.'))).toBe('No Steam profile matches that custom name.')
  })

  test('shows the message of an AppMessage the frontend wrote for the user', () => {
    expect(formatMessage(new AppMessage('A save is already in progress'))).toBe('A save is already in progress')
  })

  test.each([
    ['a TypeError from a bad argument', new TypeError('Cannot read properties of undefined (reading "appid")')],
    ['a ReferenceError', new ReferenceError('steamKey is not defined')],
    ['a plain Error', new Error('boom /home/user/.local/share/app/app.db')],
    ['a string rejection', 'plain text']
  ])('shows the generic sentence for %s, never its text', (_, err) => {
    expect(formatMessage(err)).toBe(genericMessage)
  })

  test('shows the generic sentence for a RuntimeError or AppMessage with no message', () => {
    expect(formatMessage(new RuntimeError(''))).toBe(genericMessage)
    expect(formatMessage(new AppMessage(''))).toBe(genericMessage)
  })

  test('shows the generic sentence when there is nothing to show', () => {
    expect(formatMessage(null)).toBe(genericMessage)
    expect(formatMessage(undefined)).toBe(genericMessage)
  })
})

describe('integer', () => {
  test('keeps the digits of whole numbers', () => {
    expect(integer(0)).toBe('0')
    expect(integer(999)).toBe('999')
    expect(integer(12345).replace(/\D/g, '')).toBe('12345')
    expect(integer('42')).toBe('42')
  })

  test('treats missing or non-finite values as zero', () => {
    expect(integer(undefined)).toBe('0')
    expect(integer(null)).toBe('0')
    expect(integer('abc')).toBe('0')
    expect(integer(NaN)).toBe('0')
    expect(integer(Infinity)).toBe('0')
  })
})

describe('pct', () => {
  test('drops the decimal for whole percentages', () => {
    expect(pct(0)).toBe('0%')
    expect(pct(50)).toBe('50%')
    expect(pct(100)).toBe('100%')
    expect(pct('86')).toBe('86%')
  })

  test('keeps one decimal for fractional percentages', () => {
    expect(pct(33.333)).toBe('33.3%')
    expect(pct(0.5)).toBe('0.5%')
  })

  test('shows 0% for values that are not numbers', () => {
    expect(pct(NaN)).toBe('0%')
    expect(pct('x')).toBe('0%')
    expect(pct(undefined)).toBe('0%')
  })
})

describe('formatPlaytime', () => {
  test.each([
    [0, '0m'],
    [59, '59m'],
    [60, '1h 0m'],
    [75, '1h 15m'],
    [620, '10h 20m'],
    [1500, '25h 0m']
  ])('formats %i minutes as %s', (minutes, label) => {
    expect(formatPlaytime(minutes)).toBe(label)
  })

  test('drops fractional minutes', () => {
    expect(formatPlaytime(75.9)).toBe('1h 15m')
  })

  test('shows 0m for negative or missing playtime', () => {
    expect(formatPlaytime(-5)).toBe('0m')
    expect(formatPlaytime(undefined)).toBe('0m')
    expect(formatPlaytime('x')).toBe('0m')
  })
})

describe('formatDateTime', () => {
  test('shows Unknown when there is no date', () => {
    expect(formatDateTime('')).toBe('Unknown')
    expect(formatDateTime(null)).toBe('Unknown')
    expect(formatDateTime(undefined)).toBe('Unknown')
  })

  test('formats a valid timestamp into a readable date and time', () => {
    const text = formatDateTime('2026-05-22T10:00:00Z')

    expect(text).toContain('2026')
    expect(text).not.toBe('2026-05-22T10:00:00Z')
  })

  test('returns input it cannot parse unchanged', () => {
    expect(formatDateTime('not a date')).toBe('not a date')
  })
})

describe('formatRelativeTime', () => {
  const now = Date.parse('2026-05-22T10:00:00Z')

  test('shows nothing when there is no date', () => {
    expect(formatRelativeTime('', now)).toBe('')
    expect(formatRelativeTime(null, now)).toBe('')
  })

  test('words recent times in minutes and hours', () => {
    expect(formatRelativeTime('2026-05-22T10:00:00Z', now)).toBe('just now')
    expect(formatRelativeTime('2026-05-22T09:59:30Z', now)).toBe('just now')
    expect(formatRelativeTime('2026-05-22T09:58:00Z', now)).toBe('2 min ago')
    expect(formatRelativeTime('2026-05-22T09:00:00Z', now)).toBe('1 hour ago')
    expect(formatRelativeTime('2026-05-22T08:00:00Z', now)).toBe('2 hours ago')
  })

  test('words older times in days', () => {
    expect(formatRelativeTime('2026-05-21T10:00:00Z', now)).toBe('1 day ago')
    expect(formatRelativeTime('2026-05-19T10:00:00Z', now)).toBe('3 days ago')
  })

  test('falls back to the absolute date from seven days on', () => {
    expect(formatRelativeTime('2026-05-15T10:00:00Z', now)).toBe(formatDateTime('2026-05-15T10:00:00Z'))
  })

  test('treats a time ahead of the clock as just now', () => {
    expect(formatRelativeTime('2026-05-22T10:05:00Z', now)).toBe('just now')
  })

  test('returns input it cannot parse unchanged', () => {
    expect(formatRelativeTime('not a date', now)).toBe('not a date')
  })
})
