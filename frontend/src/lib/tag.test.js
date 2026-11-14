import {describe, expect, test} from 'vitest'
import {matchesTagFilter} from './filter.js'
import {gameTags, suggestionTagOptions, tagCountFromGames, tagTone} from './tag.js'

// Backend incomplete tag names. MUST stay in sync with internal/repository/tags.go: the profileGameTags sort list and
// the stat/issue tag candidates (statTagCandidates, issueTagCandidates). Checked against that file when this list was
// last edited. 'completed' is excluded because it is not offered as a filter chip.
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

  test('derives a dlc tag for DLC-bound sets and de-duplicates it', () => {
    expect(gameTags({dlcAppId: 4173830, tags: ['almost_there']})).toEqual(['almost_there', 'dlc'])
    expect(gameTags({dlcAppId: 4173830, tags: ['dlc', 'almost_there']})).toEqual(['almost_there', 'dlc'])
    expect(gameTags({dlcAppId: 0, tags: ['almost_there']})).toEqual(['almost_there'])
    expect(gameTags({tags: ['in_progress']})).toEqual(['in_progress'])
  })

  test('keeps the dlc tag on completed DLC sets, after completed', () => {
    expect(gameTags({dlcAppId: 4173830, isCompleted: true, tags: ['completed', 'missing_achievements_in_dlc']})).toEqual(['completed', 'dlc'])
  })

  test('derives a set tag for every non-base set and sorts it by the filter chip order', () => {
    expect(gameTags({groupId: 3, tags: ['almost_there']})).toEqual(['almost_there', 'set'])
    expect(gameTags({groupId: 3, tags: ['set', 'almost_there']})).toEqual(['almost_there', 'set'])
    expect(gameTags({groupId: 1, isCompleted: true, tags: ['completed', 'missing_achievements_in_dlc']})).toEqual(['completed', 'set'])
  })

  test('sorts a completed card as completed, set, dlc, missing_cover_art', () => {
    const tags = gameTags({groupId: 1, dlcAppId: 4173830, isCompleted: true, tags: ['missing_cover_art', 'completed', 'new_achievements_added']})

    expect(tags).toEqual(['completed', 'set', 'dlc', 'missing_cover_art'])
  })

  test('sorts a mixed incomplete card by the filter chip order', () => {
    const tags = gameTags({groupId: 2, dlcAppId: 4173830, tags: ['sync_warning', 'missing_cover_art', 'dlc_not_owned', 'in_progress', 'new_achievements_added']})

    expect(tags).toEqual(['new_achievements_added', 'in_progress', 'dlc_not_owned', 'set', 'dlc', 'missing_cover_art', 'sync_warning'])
  })

  test('sorts unknown tags last and keeps their original relative order', () => {
    const tags = gameTags({tags: ['zeta_tag', 'untouched', 'alpha_tag', 'almost_there']})

    expect(tags).toEqual(['almost_there', 'untouched', 'zeta_tag', 'alpha_tag'])
  })

  test('does not mutate the stored tag array', () => {
    const stored = ['sync_warning', 'almost_there']
    gameTags({tags: stored})

    expect(stored).toEqual(['sync_warning', 'almost_there'])
  })

  test('does not derive a set tag for the base set', () => {
    expect(gameTags({groupId: 0, tags: ['almost_there']})).toEqual(['almost_there'])
    expect(gameTags({tags: ['almost_there']})).toEqual(['almost_there'])
    expect(gameTags({groupId: null, tags: ['almost_there']})).toEqual(['almost_there'])
  })

  test('orders set before dlc on DLC sets, and both after dlc_not_owned', () => {
    expect(gameTags({groupId: 1, dlcAppId: 4173830, tags: ['dlc_not_owned']})).toEqual(['dlc_not_owned', 'set', 'dlc'])
    expect(gameTags({groupId: 0, dlcAppId: 4173830, tags: []})).toEqual(['dlc'])
  })
})

describe('suggestion tag options', () => {
  // The exact chip order is pinned by App.test.js (chipLabels on the Not completed and Completed panels) and by the
  // card-order tests below; this block only checks membership.
  test('offers every backend incomplete tag as a chip and never offers completed', () => {
    for (const tag of backendIncompleteTags) {
      expect(suggestionTagOptions).toContain(tag)
    }
    expect(suggestionTagOptions).not.toContain('completed')
    expect(new Set(suggestionTagOptions).size).toBe(suggestionTagOptions.length)
  })

  test('card tags use the filter chip order for every chip tag', () => {
    const game = {groupId: 1, dlcAppId: 4173830, tags: [...suggestionTagOptions].reverse()}

    expect(gameTags(game)).toEqual(suggestionTagOptions)
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

  test('counts derived set tags and filters on them', () => {
    const games = [
      {groupId: 1, tags: []},
      {groupId: 0, tags: []},
      {tags: []}
    ]

    expect(tagCountFromGames(games, 'set')).toBe(1)
    expect(matchesTagFilter(games[0], 'set')).toBe(true)
    expect(matchesTagFilter(games[1], 'set')).toBe(false)
  })
})

// Card labels and hover text for these tags are asserted through a rendered DLC set card in App.test.js ('gives the
// derived Set and DLC card tags their hover text'), so only the tone classification (which group a tag belongs to)
// is checked here.
describe('derived set and dlc tags', () => {
  test('a DLC set carries set and dlc, both in the neutral tone group', () => {
    const tags = gameTags({groupId: 1, dlcAppId: 4173830, tags: []})

    expect(tags).toEqual(['set', 'dlc'])
    for (const tag of tags) {
      expect(tagTone(tag)).toBe('neutral')
    }
  })
})

describe('dlc_not_owned tag', () => {
  test('uses the danger tone group shared with Achievements in DLC', () => {
    expect(tagTone('dlc_not_owned')).toBe('danger')
    expect(tagTone('dlc_not_owned')).toBe(tagTone('missing_achievements_in_dlc'))
  })
})
