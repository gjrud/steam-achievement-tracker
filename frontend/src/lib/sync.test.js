import {describe, expect, test} from 'vitest'
import {formatDateTime, integer} from './format.js'
import {syncBand} from './sync.js'

const startedAt = '2026-05-22T10:00:00Z'
const finishedAt = '2026-05-22T10:05:00Z'
const MINUTE = 60 * 1000
const HOUR = 60 * MINUTE
// Two hours after the run finished, so its time reads "2 hours ago".
const NOW = Date.parse(finishedAt) + 2 * HOUR
const run = {status: 'success', startedAt, finishedAt, gamesSynced: 1243, gamesTotal: 1243, gamesFailed: 0}

describe('syncBand', () => {
  test.each([
    ['running', {run: {...run, status: 'running', finishedAt: ''}, running: true}, 'run'],
    ['starting', {run: null, running: true}, 'run'],
    ['starting over a finished run', {run, running: true}, 'run'],
    ['success', {run}, 'ok'],
    ['partial', {run: {...run, status: 'partial', gamesSynced: 1240, gamesFailed: 3}}, 'warn'],
    ['failed', {run: {...run, status: 'failed', gamesSynced: 0, gamesFailed: 1243}}, 'fail'],
    ['canceled', {run: {...run, status: 'canceled', gamesSynced: 600, gamesFailed: 0}}, 'none'],
    ['no sync yet', {run: null}, 'none']
  ])('gives a %s sync the %s tone', (_name, {run: latest, running = false}, tone) => {
    expect(syncBand(latest, running, NOW).tone).toBe(tone)
  })

  test.each([
    ['running', {run: {...run, status: 'running', finishedAt: ''}, running: true}, 'spin'],
    ['starting', {run: null, running: true}, 'spin'],
    ['starting over a finished run', {run, running: true}, 'spin'],
    ['success', {run}, 'check'],
    ['partial', {run: {...run, status: 'partial', gamesSynced: 1240, gamesFailed: 3}}, 'warn'],
    ['failed', {run: {...run, status: 'failed', gamesSynced: 0, gamesFailed: 1243}}, 'warn'],
    ['canceled', {run: {...run, status: 'canceled', gamesSynced: 600, gamesFailed: 0}}, 'stop'],
    ['unknown status', {run: {...run, status: 'weird'}}, 'stop'],
    ['empty status', {run: {...run, status: ''}}, 'stop'],
    ['no sync yet', {run: null}, 'clock']
  ])('gives a %s sync the %s icon', (_name, {run: latest, running = false}, icon) => {
    expect(syncBand(latest, running, NOW).icon).toBe(icon)
  })

  test('reads a successful run as all games synced, dated by its finish time', () => {
    expect(syncBand(run, false, NOW)).toEqual({
      tone: 'ok',
      icon: 'check',
      headline: 'All games synced',
      detail: `${integer(1243)} / ${integer(1243)} games`,
      time: {iso: finishedAt, title: formatDateTime(finishedAt), text: '2 hours ago'},
      announce: 'Last sync: All games synced'
    })
  })

  test('reads a partial run with its failure count', () => {
    expect(syncBand({...run, status: 'partial', gamesSynced: 1240, gamesFailed: 3}, false, NOW)).toMatchObject({
      tone: 'warn',
      icon: 'warn',
      headline: 'Partial sync · 3 failed',
      detail: `${integer(1240)} / ${integer(1243)} games synced`,
      announce: 'Last sync: Partial sync, 3 failed'
    })
  })

  test('uses the singular for one failed game in a partial run', () => {
    expect(syncBand({...run, status: 'partial', gamesSynced: 1242, gamesFailed: 1}, false, NOW)).toMatchObject({
      headline: 'Partial sync · 1 failed',
      announce: 'Last sync: Partial sync, 1 failed'
    })
  })

  test('reads a failed run with its error count, and without one when nothing failed', () => {
    expect(syncBand({...run, status: 'failed', gamesSynced: 0, gamesFailed: 1243}, false, NOW)).toMatchObject({
      tone: 'fail',
      icon: 'warn',
      headline: `Sync failed · ${integer(1243)} errors`,
      detail: `${integer(0)} / ${integer(1243)} games synced`,
      announce: `Last sync: Sync failed, ${integer(1243)} errors`
    })
    expect(syncBand({...run, status: 'failed', gamesSynced: 0, gamesFailed: 0}, false, NOW).headline).toBe('Sync failed')
  })

  test('uses the singular for one error in a failed run', () => {
    expect(syncBand({...run, status: 'failed', gamesSynced: 0, gamesFailed: 1}, false, NOW)).toMatchObject({
      headline: 'Sync failed · 1 error',
      announce: 'Last sync: Sync failed, 1 error'
    })
  })

  test('reads a canceled run as neutral, not as success', () => {
    expect(syncBand({...run, status: 'canceled', gamesSynced: 600, gamesFailed: 0}, false, NOW)).toMatchObject({
      tone: 'none',
      icon: 'stop',
      headline: 'Sync canceled',
      detail: `${integer(600)} / ${integer(1243)} games synced`,
      announce: 'Last sync: Sync canceled'
    })
  })

  test('drops the counts from a finished run with no games, keeping the headline and time', () => {
    expect(syncBand({...run, status: 'failed', gamesSynced: 0, gamesTotal: 0, gamesFailed: 0}, false, NOW)).toEqual({
      tone: 'fail',
      icon: 'warn',
      headline: 'Sync failed',
      detail: '',
      time: {iso: finishedAt, title: formatDateTime(finishedAt), text: '2 hours ago'},
      announce: 'Last sync: Sync failed'
    })
    expect(syncBand({...run, gamesSynced: 0, gamesTotal: 0}, false, NOW)).toMatchObject({
      headline: 'All games synced',
      detail: '',
      time: {iso: finishedAt}
    })
    expect(syncBand({...run, status: 'canceled', gamesSynced: 0, gamesTotal: 0}, false, NOW)).toMatchObject({detail: ''})
  })

  test('shows running progress in the headline and the start time underneath', () => {
    const now = Date.parse(startedAt) + 2 * MINUTE
    const running = {...run, status: 'running', finishedAt: '', gamesSynced: 612}

    expect(syncBand(running, true, now)).toEqual({
      tone: 'run',
      icon: 'spin',
      headline: `Syncing ${integer(612)} / ${integer(1243)}`,
      detail: '',
      time: {iso: startedAt, title: formatDateTime(startedAt), text: 'Started 2 min ago'},
      announce: 'Current sync: Sync running'
    })
  })

  test('keeps the running announcement the same while counts change', () => {
    const first = syncBand({...run, status: 'running', finishedAt: '', gamesSynced: 2}, true, NOW)
    const later = syncBand({...run, status: 'running', finishedAt: '', gamesSynced: 900}, true, NOW)

    expect(later.headline).not.toBe(first.headline)
    expect(later.announce).toBe(first.announce)
  })

  test('says a running sync is running with no counts until its total is known', () => {
    const now = Date.parse(startedAt) + 2 * MINUTE
    const running = {...run, status: 'running', finishedAt: '', gamesSynced: 0, gamesTotal: 0}

    expect(syncBand(running, true, now)).toEqual({
      tone: 'run',
      icon: 'spin',
      headline: 'Sync running',
      detail: '',
      time: {iso: startedAt, title: formatDateTime(startedAt), text: 'Started 2 min ago'},
      announce: 'Current sync: Sync running'
    })
  })

  test('says a sync is starting before its run record arrives, with no time', () => {
    expect(syncBand(null, true, NOW)).toEqual({
      tone: 'run',
      icon: 'spin',
      headline: 'Sync starting',
      detail: 'Starting sync…',
      time: null,
      announce: 'Current sync: Sync running'
    })
  })

  test('says a sync is starting, not the previous finished run, when Sync now is pressed', () => {
    expect(syncBand(run, true, NOW)).toEqual({
      tone: 'run',
      icon: 'spin',
      headline: 'Sync starting',
      detail: 'Starting sync…',
      time: null,
      announce: 'Current sync: Sync running'
    })
  })

  test('announces starting and running syncs with the same text, so the handoff between them is silent', () => {
    const starting = syncBand(null, true, NOW)
    const running = syncBand({...run, status: 'running', finishedAt: '', gamesSynced: 0}, true, NOW)

    expect(starting.headline).not.toBe(running.headline)
    expect(starting.announce).toBe(running.announce)
    expect(running.announce).toBe('Current sync: Sync running')
  })

  test('says no sync has run yet without a run record, with no time', () => {
    expect(syncBand(null, false, NOW)).toEqual({
      tone: 'none',
      icon: 'clock',
      headline: 'No sync yet',
      detail: 'Press Sync now to fetch your library',
      time: null,
      announce: 'No sync yet'
    })
  })

  test('dates a finished run by its start time when it has no finish time', () => {
    expect(syncBand({...run, finishedAt: ''}, false, NOW).time).toEqual({
      iso: startedAt,
      title: formatDateTime(startedAt),
      text: '2 hours ago'
    })
  })

  test('falls back to the start time when the finish time cannot be parsed', () => {
    expect(syncBand({...run, finishedAt: 'not a date'}, false, NOW).time).toEqual({
      iso: startedAt,
      title: formatDateTime(startedAt),
      text: '2 hours ago'
    })
  })

  test('gives no time when neither run date can be parsed, so the band never writes a bad datetime', () => {
    expect(syncBand({...run, finishedAt: 'not a date', startedAt: 'not a date'}, false, NOW).time).toBeNull()
    expect(syncBand({...run, finishedAt: '', startedAt: 'not a date'}, false, NOW).time).toBeNull()
    expect(syncBand({...run, status: 'running', finishedAt: '', startedAt: 'nope'}, true, NOW).time).toBeNull()
  })

  test('shows an unknown status as its raw text, and an empty status as Unknown', () => {
    expect(syncBand({...run, status: 'weird'}, false, NOW)).toMatchObject({
      tone: 'none',
      icon: 'stop',
      headline: 'weird',
      announce: 'Last sync: weird'
    })
    expect(syncBand({...run, status: ''}, false, NOW)).toMatchObject({tone: 'none', icon: 'stop', headline: 'Unknown'})
  })
})
