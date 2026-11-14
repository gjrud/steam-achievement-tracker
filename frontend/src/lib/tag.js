// Filter chip order, shared by the Not completed and Completed views and by the tags on every card. Follows the
// Not completed groups (candidates, not owned, flagged DLC, no achievements), then the derived set/dlc tags and
// issue tags.
export const suggestionTagOptions = ['new_achievements_added', 'almost_there', 'in_progress', 'untouched', 'dlc_not_owned', 'missing_achievements_in_dlc', 'no_achievements', 'set', 'dlc', 'missing_cover_art', 'sync_warning']

// Issue tags that describe an incomplete entry. Completed cards never show them, even if a stored tag is present.
const incompleteOnlyTags = ['new_achievements_added', 'missing_achievements_in_dlc']

// tagRank is the sort key for card tags: completed first, then the filter chip order, then unknown tags.
// Unknown tags all share the last rank, so a stable sort keeps their original relative order.
function tagRank(tag) {
  if (tag === 'completed') return -1
  const index = suggestionTagOptions.indexOf(tag)
  return index === -1 ? suggestionTagOptions.length : index
}

// gameTags lists the tags shown on a card and used for filtering. 'set' is derived for every non-base achievement
// set (groupId != 0) and 'dlc' from dlcAppId, so both are filterable like any other tag. The result is de-duplicated
// and sorted by tagRank, so card tags appear in the same order as the filter chips.
export function gameTags(game) {
  const stored = Array.isArray(game?.tags) ? game.tags : []
  const derived = []
  if (typeof game?.groupId === 'number' && game.groupId !== 0) derived.push('set')
  if (game?.dlcAppId) derived.push('dlc')
  const tags = [...new Set([...derived, ...stored])]
  const visible = game?.isCompleted ? tags.filter((tag) => !incompleteOnlyTags.includes(tag)) : tags
  return visible.sort((a, b) => tagRank(a) - tagRank(b))
}

export function tagLabel(tag) {
  switch (tag) {
    case 'new_achievements_added': return 'New achievements added'
    case 'almost_there': return 'Almost there'
    case 'in_progress': return 'In progress'
    case 'untouched': return 'Untouched'
    case 'no_achievements': return 'No achievements'
    case 'completed': return 'Completed'
    case 'set': return 'Set'
    case 'dlc': return 'DLC'
    case 'dlc_not_owned': return 'Not owned'
    case 'missing_achievements_in_dlc': return 'Achievements in DLC'
    case 'missing_cover_art': return 'Missing cover art'
    case 'sync_warning': return 'Sync error'
    default: return tag
  }
}

export function tagDescription(tag) {
  switch (tag) {
    case 'set': return 'Additional achievement set of this game'
    case 'dlc': return 'Achievement set from DLC'
    case 'dlc_not_owned': return 'You do not own the DLC for this achievement set.'
    case 'missing_achievements_in_dlc': return 'Remaining achievements belong to DLC or content you do not plan to count. Set manually from the card menu.'
    default: return ''
  }
}

export function tagTone(tag) {
  switch (tag) {
    case 'completed': return 'success'
    case 'in_progress': return 'info'
    case 'almost_there': return 'accent'
    case 'new_achievements_added': return 'warn'
    case 'dlc_not_owned':
    case 'missing_achievements_in_dlc':
    case 'missing_cover_art':
    case 'sync_warning': return 'danger'
    case 'untouched':
    case 'no_achievements':
    case 'set':
    case 'dlc': return 'neutral'
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
