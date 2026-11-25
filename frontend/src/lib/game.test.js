import {describe, expect, test} from 'vitest'
import {cssUrl} from './game.js'

describe('cssUrl', () => {
  test('wraps a plain URL in a quoted url() value', () => {
    expect(cssUrl('https://cdn.example.test/header.jpg')).toBe('url("https://cdn.example.test/header.jpg")')
  })

  test('leaves parentheses, commas and spaces alone inside the quotes', () => {
    expect(cssUrl('https://cdn.example.test/a(1), b.jpg')).toBe('url("https://cdn.example.test/a(1), b.jpg")')
  })

  test('escapes a double quote so it cannot end the string', () => {
    expect(cssUrl('x"); background: red; --y: "')).toBe('url("x\\22 ); background: red; --y: \\22 ")')
  })

  test('escapes backslashes', () => {
    expect(cssUrl('a\\b')).toBe('url("a\\5c b")')
  })

  test('escapes line breaks and other control characters', () => {
    expect(cssUrl('a\nb\rc\u0000d\u007f')).toBe('url("a\\a b\\d c\\0 d\\7f ")')
  })

  test('treats a missing value as an empty string', () => {
    expect(cssUrl(undefined)).toBe('url("")')
  })
})
