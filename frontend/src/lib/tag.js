export const suggestionTagOptions = ['new_achievements_added', 'almost_there', 'in_progress', 'untouched', 'no_achievements', 'missing_achievements_in_dlc', 'missing_cover_art', 'sync_warning']

export function gameTags(game) {
  return Array.isArray(game?.tags) ? game.tags : []
}

export function tagLabel(tag) {
  switch (tag) {
    case 'new_achievements_added': return 'New achievements added'
    case 'almost_there': return 'Almost there'
    case 'in_progress': return 'In progress'
    case 'untouched': return 'Untouched'
    case 'no_achievements': return 'No achievements'
    case 'completed': return 'Completed'
    case 'missing_achievements_in_dlc': return 'Achievements in DLC'
    case 'missing_cover_art': return 'Missing cover art'
    case 'sync_warning': return 'Sync error'
    default: return tag
  }
}

export function tagTone(tag) {
  switch (tag) {
    case 'completed': return 'success'
    case 'in_progress': return 'info'
    case 'almost_there': return 'accent'
    case 'new_achievements_added': return 'warn'
    case 'missing_achievements_in_dlc':
    case 'missing_cover_art':
    case 'sync_warning': return 'danger'
    case 'untouched':
    case 'no_achievements': return 'neutral'
    default: return 'neutral'
  }
}

export function toneClass(tone) {
  return tone ? `tone-${tone}` : ''
}

export function tagCountFromGames(games, tag) {
  return (games || []).filter((game) => gameTags(game).includes(tag)).length
}

export function suggestionTagCount(games, tag) {
  return tagCountFromGames(games, tag)
}
