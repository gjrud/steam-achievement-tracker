import {formatDateTime} from './format.js'

export function statusLabel(status) {
  switch (status) {
    case 'running': return 'Sync running'
    case 'success': return 'Success'
    case 'partial': return 'Partial sync'
    case 'failed': return 'Failed'
    case 'canceled': return 'Canceled'
    default: return status || ''
  }
}

export function syncTitle(run, running = false) {
  if (running && (!run || run.status !== 'running')) return 'Sync starting'
  if (!run) return 'No sync yet'
  return `${statusLabel(run.status) || 'Unknown'} · ${formatDateTime(run.finishedAt || run.startedAt)}`
}

export function syncDetails(run, running = false) {
  if (running && (!run || run.status !== 'running')) return 'Starting sync…'
  if (!run) return 'Manual sync or startup sync will populate the dashboard.'
  const time = run.finishedAt ? formatDateTime(run.finishedAt) : formatDateTime(run.startedAt)
  return `${run.gamesSynced}/${run.gamesTotal} games synced, ${run.gamesFailed} failed · ${time}`
}
