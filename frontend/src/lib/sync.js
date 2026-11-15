import {formatDateTime, formatRelativeTime, integer} from './format.js'

// timeOf returns the datetime, absolute title and relative text for the profile card's time element, or null when
// the time is missing or unparseable, so the band never writes an invalid datetime attribute.
function timeOf(iso, now, prefix = '') {
  if (!iso || Number.isNaN(Date.parse(iso))) return null
  return {iso, title: formatDateTime(iso), text: `${prefix}${formatRelativeTime(iso, now)}`}
}

// errorCount words a failure count for the failed headline: "1 error", "6 errors".
function errorCount(count) {
  return count === 1 ? '1 error' : `${integer(count)} errors`
}

// countsOf words a finished run's game counts. A run with no games (empty library or failed owned-games fetch)
// has no meaningful counts, so it returns '' and the band shows only the headline and time.
function countsOf(run, suffix) {
  const total = Number(run.gamesTotal) || 0
  if (total === 0) return ''
  return `${integer(run.gamesSynced)} / ${integer(total)} ${suffix}`
}

// syncBand describes the profile card's sync band for the latest run.
//   tone: ok | run | warn | fail | none (drives colour)
//   icon: check | spin | clock | warn | stop (drives the glyph; canceled and unknown statuses get stop, not clock)
//   headline: the status line; detail: the second line's text before the time (may be empty)
//   time: null or {iso, title, text}; a finished run is dated by its finish time, or its start time when the finish
//   time is missing or unparseable; a running one by its start time
//   announce: short screen-reader summary. It changes only on a state transition, never with counts or time.
//   "Sync starting" and "Sync running" share one announce, so starting a sync is announced once.
export function syncBand(run, running = false, now = Date.now()) {
  if (run?.status === 'running') {
    return {
      tone: 'run',
      icon: 'spin',
      headline: run.gamesTotal ? `Syncing ${integer(run.gamesSynced)} / ${integer(run.gamesTotal)}` : 'Sync running',
      detail: '',
      time: timeOf(run.startedAt, now, 'Started '),
      announce: 'Current sync: Sync running'
    }
  }
  if (running) {
    return {tone: 'run', icon: 'spin', headline: 'Sync starting', detail: 'Starting sync…', time: null, announce: 'Current sync: Sync running'}
  }
  if (!run) {
    return {
      tone: 'none',
      icon: 'clock',
      headline: 'No sync yet',
      detail: 'Press Sync now to fetch your library',
      time: null,
      announce: 'No sync yet'
    }
  }

  const failed = Number(run.gamesFailed) || 0
  const time = timeOf(run.finishedAt, now) ?? timeOf(run.startedAt, now)
  switch (run.status) {
    case 'success':
      return {tone: 'ok', icon: 'check', headline: 'All games synced', detail: countsOf(run, 'games'), time, announce: 'Last sync: All games synced'}
    case 'partial': {
      const headline = failed > 0 ? `Partial sync · ${integer(failed)} failed` : 'Partial sync'
      const announce = failed > 0 ? `Last sync: Partial sync, ${integer(failed)} failed` : 'Last sync: Partial sync'
      return {tone: 'warn', icon: 'warn', headline, detail: countsOf(run, 'games synced'), time, announce}
    }
    case 'failed': {
      const headline = failed > 0 ? `Sync failed · ${errorCount(failed)}` : 'Sync failed'
      const announce = failed > 0 ? `Last sync: Sync failed, ${errorCount(failed)}` : 'Last sync: Sync failed'
      return {tone: 'fail', icon: 'warn', headline, detail: countsOf(run, 'games synced'), time, announce}
    }
    case 'canceled':
      return {tone: 'none', icon: 'stop', headline: 'Sync canceled', detail: countsOf(run, 'games synced'), time, announce: 'Last sync: Sync canceled'}
    default: {
      const label = run.status || 'Unknown'
      return {tone: 'none', icon: 'stop', headline: label, detail: countsOf(run, 'games synced'), time, announce: `Last sync: ${label}`}
    }
  }
}
