import {describe, expect, test} from 'vitest'
import {formatDateTime} from './format.js'
import {statusLabel, syncDetails, syncTitle} from './sync.js'

const startedAt = '2026-05-22T10:00:00Z'
const finishedAt = '2026-05-22T10:05:00Z'

describe('statusLabel', () => {
  test.each([
    ['running', 'Sync running'],
    ['success', 'Success'],
    ['partial', 'Partial sync'],
    ['failed', 'Failed'],
    ['canceled', 'Canceled']
  ])('labels %s runs as %s', (status, label) => {
    expect(statusLabel(status)).toBe(label)
  })

  test('passes unknown statuses through and blanks missing ones', () => {
    expect(statusLabel('weird')).toBe('weird')
    expect(statusLabel('')).toBe('')
    expect(statusLabel(undefined)).toBe('')
  })
})

describe('syncTitle', () => {
  const run = {status: 'success', startedAt, finishedAt, gamesSynced: 6, gamesTotal: 6, gamesFailed: 0}

  test('shows the status with the finish time', () => {
    expect(syncTitle(run)).toBe(`Success · ${formatDateTime(finishedAt)}`)
  })

  test('falls back to the start time for a run without a finish time', () => {
    expect(syncTitle({...run, finishedAt: ''})).toBe(`Success · ${formatDateTime(startedAt)}`)
  })

  test('labels a run with an empty status as Unknown', () => {
    expect(syncTitle({...run, status: ''})).toBe(`Unknown · ${formatDateTime(finishedAt)}`)
  })

  test('shows the running run with its start time while a sync is in progress', () => {
    expect(syncTitle({...run, status: 'running', finishedAt: ''}, true)).toBe(`Sync running · ${formatDateTime(startedAt)}`)
  })

  test('says a sync is starting until a running record arrives', () => {
    expect(syncTitle(null, true)).toBe('Sync starting')
    expect(syncTitle(run, true)).toBe('Sync starting')
  })

  test('says no sync has run yet without a run record', () => {
    expect(syncTitle(null)).toBe('No sync yet')
    expect(syncTitle(undefined, false)).toBe('No sync yet')
  })
})

describe('syncDetails', () => {
  const run = {status: 'partial', startedAt, finishedAt, gamesSynced: 5, gamesTotal: 6, gamesFailed: 1}

  test('counts synced and failed games and dates the run by its finish time', () => {
    expect(syncDetails(run)).toBe(`5/6 games synced, 1 failed · ${formatDateTime(finishedAt)}`)
  })

  test('dates a still-running record by its start time', () => {
    expect(syncDetails({...run, status: 'running', finishedAt: ''}, true)).toBe(`5/6 games synced, 1 failed · ${formatDateTime(startedAt)}`)
  })

  test('explains a sync that is starting and a missing run', () => {
    expect(syncDetails(null, true)).toBe('Starting sync…')
    expect(syncDetails(null)).toBe('Manual sync or startup sync will populate the dashboard.')
  })
})
