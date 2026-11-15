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

// syncTitle is the status label alone, shown on the first line of the profile sync block.
export function syncTitle(run, running = false) {
  if (running && (!run || run.status !== 'running')) return 'Sync starting'
  if (!run) return 'No sync yet'
  return statusLabel(run.status) || 'Unknown'
}

// syncTime is the date and time on its own line under the status: the finish time, or the start time while a run is
// still running ("Started …"). Empty when there is no run yet.
export function syncTime(run, running = false) {
  if (running && (!run || run.status !== 'running')) return ''
  if (!run) return ''
  if (run.status === 'running') return `Started ${formatDateTime(run.startedAt)}`
  return formatDateTime(run.finishedAt || run.startedAt)
}

// syncDetails is the game counts only; the date is shown by syncTime, so it appears once.
export function syncDetails(run, running = false) {
  if (running && (!run || run.status !== 'running')) return 'Starting sync…'
  if (!run) return 'Manual sync or startup sync will populate the dashboard.'
  return `${run.gamesSynced}/${run.gamesTotal} games synced, ${run.gamesFailed} failed`
}
