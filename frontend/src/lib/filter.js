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

export function sortByTitle(games) {
  return [...(games || [])].sort((a, b) => gameTitle(a).localeCompare(gameTitle(b)))
}
