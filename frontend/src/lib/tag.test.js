import {describe, expect, test} from 'vitest'
import {matchesTagFilter} from './filter.js'
import {gameTags, suggestionTagOptions, tagCountFromGames, tagDescription, tagLabel, tagTone} from './tag.js'

// Backend tag names from internal/repository/tags.go, excluding 'completed' (not offered in the Not completed view).
const backendIncompleteTags = ['new_achievements_added', 'almost_there', 'in_progress', 'untouched', 'no_achievements', 'missing_achievements_in_dlc', 'missing_cover_art', 'sync_warning', 'dlc_not_owned']

describe('gameTags', () => {
  test('hides new-achievement and DLC-missing tags on completed cards', () => {
    const tags = gameTags({isCompleted: true, tags: ['completed', 'new_achievements_added', 'missing_achievements_in_dlc', 'missing_cover_art']})

    expect(tags).toEqual(['completed', 'missing_cover_art'])
  })

  test('keeps issue tags on incomplete cards', () => {
    const tags = gameTags({isCompleted: false, tags: ['new_achievements_added', 'missing_achievements_in_dlc']})

    expect(tags).toEqual(['new_achievements_added', 'missing_achievements_in_dlc'])
  })

  test('returns an empty list without tags', () => {
    expect(gameTags({})).toEqual([])
    expect(gameTags(undefined)).toEqual([])
  })

  test('derives a dlc tag first for DLC-bound sets and de-duplicates it', () => {
    expect(gameTags({dlcAppId: 4173830, tags: ['almost_there']})).toEqual(['dlc', 'almost_there'])
    expect(gameTags({dlcAppId: 4173830, tags: ['dlc', 'almost_there']})).toEqual(['dlc', 'almost_there'])
    expect(gameTags({dlcAppId: 0, tags: ['almost_there']})).toEqual(['almost_there'])
    expect(gameTags({tags: ['in_progress']})).toEqual(['in_progress'])
  })

  test('keeps the dlc tag on completed DLC sets', () => {
    expect(gameTags({dlcAppId: 4173830, isCompleted: true, tags: ['completed', 'missing_achievements_in_dlc']})).toEqual(['dlc', 'completed'])
  })
})

describe('suggestion tag options', () => {
  test('offers dlc before dlc_not_owned and matches every backend incomplete tag', () => {
    expect(suggestionTagOptions.indexOf('dlc')).toBeGreaterThanOrEqual(0)
    expect(suggestionTagOptions.indexOf('dlc')).toBeLessThan(suggestionTagOptions.indexOf('dlc_not_owned'))
    for (const tag of backendIncompleteTags) {
      expect(suggestionTagOptions).toContain(tag)
    }
    expect(suggestionTagOptions).not.toContain('completed')
    expect(new Set(suggestionTagOptions).size).toBe(suggestionTagOptions.length)
  })

  test('counts derived dlc tags and filters on them', () => {
    const games = [
      {dlcAppId: 1, tags: ['dlc_not_owned'], dlcNotOwned: true},
      {dlcAppId: 2, tags: ['missing_achievements_in_dlc']},
      {dlcAppId: 0, tags: ['almost_there']}
    ]

    expect(tagCountFromGames(games, 'dlc')).toBe(2)
    expect(tagCountFromGames(games, 'dlc_not_owned')).toBe(1)
    expect(matchesTagFilter(games[0], 'dlc')).toBe(true)
    expect(matchesTagFilter(games[2], 'dlc')).toBe(false)
    expect(matchesTagFilter(games[2], '')).toBe(true)
  })
})

describe('dlc tag', () => {
  test('is labelled DLC, accent, and explained on hover', () => {
    expect(tagLabel('dlc')).toBe('DLC')
    expect(tagTone('dlc')).toBe('accent')
    expect(tagDescription('dlc')).toBe('Achievement set from DLC')
  })
})

describe('dlc_not_owned tag', () => {
  test('is labelled Not owned, uses the same danger tone as Achievements in DLC, and is offered as a filter chip', () => {
    expect(tagLabel('dlc_not_owned')).toBe('Not owned')
    expect(tagTone('dlc_not_owned')).toBe('danger')
    expect(tagTone('dlc_not_owned')).toBe(tagTone('missing_achievements_in_dlc'))
    expect(tagDescription('dlc_not_owned')).toBe('You do not own the DLC for this achievement set.')
    expect(suggestionTagOptions).toContain('dlc_not_owned')
  })
})
