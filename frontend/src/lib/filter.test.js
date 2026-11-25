import {describe, expect, test} from 'vitest'
import {sortByTitle} from './filter.js'

const titles = (games) => games.map((game) => game.name)

describe('sortByTitle', () => {
  test('sorts by title case-insensitively, and breaks a case-only tie the way localeCompare does', () => {
    // Codepoint order would put the capital first ('Cherry' < 'apple'); a case-insensitive sort does not.
    const games = [{name: 'banana'}, {name: 'Cherry'}, {name: 'apple'}]
    expect(titles(sortByTitle(games))).toEqual(['apple', 'banana', 'Cherry'])

    // Same letters, different case: the order is whatever localeCompare gives in the running locale (lowercase first in
    // most), and it is the same for either input order.
    const tieOrder = ['Apple', 'apple'].sort((a, b) => a.localeCompare(b))
    expect(titles(sortByTitle([{name: 'Apple'}, {name: 'apple'}]))).toEqual(tieOrder)
    expect(titles(sortByTitle([{name: 'apple'}, {name: 'Apple'}]))).toEqual(tieOrder)
  })

  test('returns a new array and leaves the input order alone', () => {
    const games = [{name: 'b'}, {name: 'a'}]
    const sorted = sortByTitle(games)
    expect(sorted).not.toBe(games)
    expect(titles(games)).toEqual(['b', 'a'])
  })

  test('treats a missing list as empty', () => {
    expect(sortByTitle(undefined)).toEqual([])
  })
})
