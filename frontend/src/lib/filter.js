import {gameTitle} from './game.js'
import {gameTags} from './tag.js'

export function matchesSearchQuery(game, query) {
  if (!query) return true
  return gameTitle(game).toLowerCase().includes(query)
}

export function matchesTagFilter(game, tag) {
  return !tag || gameTags(game).includes(tag)
}

export function matchesViewFilters(game, query, tag) {
  return matchesSearchQuery(game, query) && matchesTagFilter(game, tag)
}

// titleCollator is localeCompare's comparer (default locale and options), built once instead of per comparison.
const titleCollator = new Intl.Collator()

export function sortByTitle(games) {
  return [...(games || [])].sort((a, b) => titleCollator.compare(gameTitle(a), gameTitle(b)))
}
