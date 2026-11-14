import {describe, expect, test} from 'vitest'
import {formatDateTime, formatMessage, formatPlaytime, integer, pct} from './format.js'

describe('formatMessage', () => {
  test('uses the Error message', () => {
    expect(formatMessage(new Error('boom'))).toBe('boom')
  })

  test('uses the string form of plain rejections', () => {
    expect(formatMessage('plain text')).toBe('plain text')
  })

  test('falls back to the error name when the message is empty', () => {
    expect(formatMessage(new Error(''))).toBe('Error')
  })

  test('reports an unknown error when there is nothing to show', () => {
    expect(formatMessage(null)).toBe('Unknown error')
    expect(formatMessage(undefined)).toBe('Unknown error')
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
