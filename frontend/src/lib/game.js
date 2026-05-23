import {formatDateTime, integer, pct} from './format.js'

export function gameTitle(game) {
  return game?.name || 'Untitled game'
}

export function gameWarning(game) {
  if (!game?.syncWarning) return ''
  const base = 'Could not refresh this game during last sync.'
  const suffix = game.lastSyncedAt ? ` Showing data from ${formatDateTime(game.lastSyncedAt)}.` : ' No previous data available.'
  return `${base}${suffix}${game.lastError ? ` ${game.lastError}` : ''}`
}

export function syncErrorLabel(game) {
  return shouldShowSyncErrorMessage(game) ? 'Sync error' : ''
}

export function hasAchievements(game) {
  return Number(game?.totalAchievements) > 0
}

export function shouldShowSyncErrorMessage(game) {
  return Boolean(game?.syncWarning && !game?.lastSyncedAt)
}

export function cardProgress(game) {
  const total = Number(game?.totalAchievements) || 0
  if (!total) return 0
  return Math.max(0, Math.min(100, Number(game?.completionPercent) || 0))
}

export function completionLabel(game) {
  const syncError = syncErrorLabel(game)
  if (syncError) return syncError
  const total = Number(game?.totalAchievements) || 0
  const unlocked = Number(game?.unlockedAchievements) || 0
  if (!total) return 'No achievements'
  return `${integer(unlocked)} / ${integer(total)}`
}

export function missingAvgUnlockLabel(game) {
  if (game?.missingAvgUnlock == null) return ''
  return `Missing avg unlock: ${pct(game.missingAvgUnlock)}`
}
