import {describe, expect, test} from 'vitest'
import {formatDateTime} from './format.js'
import {statusLabel, syncDetails, syncTime, syncTitle} from './sync.js'

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

  test('shows only the status label on the first line', () => {
    expect(syncTitle(run)).toBe('Success')
    expect(syncTitle({...run, status: 'partial'})).toBe('Partial sync')
  })

  test('labels a run with an empty status as Unknown', () => {
    expect(syncTitle({...run, status: ''})).toBe('Unknown')
  })

  test('shows Sync running for a running run while a sync is in progress', () => {
    expect(syncTitle({...run, status: 'running', finishedAt: ''}, true)).toBe('Sync running')
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

describe('syncTime', () => {
  const run = {status: 'success', startedAt, finishedAt, gamesSynced: 6, gamesTotal: 6, gamesFailed: 0}

  test('dates a finished run by its finish time', () => {
    expect(syncTime(run)).toBe(formatDateTime(finishedAt))
  })

  test('falls back to the start time for a run without a finish time', () => {
    expect(syncTime({...run, finishedAt: ''})).toBe(formatDateTime(startedAt))
  })

  test.each(['partial', 'failed', 'canceled'])('dates a %s run by its finish time, not as started', (status) => {
    const ended = {...run, status}
    expect(syncTime(ended)).toBe(formatDateTime(finishedAt))
    expect(syncTime(ended)).not.toMatch(/^Started /)
  })

  test.each(['partial', 'failed', 'canceled'])('falls back to the start time for a %s run without a finish time', (status) => {
    expect(syncTime({...run, status, finishedAt: ''})).toBe(formatDateTime(startedAt))
  })

  test('dates a running run by its start time, labelled as started', () => {
    expect(syncTime({...run, status: 'running', finishedAt: ''}, true)).toBe(`Started ${formatDateTime(startedAt)}`)
  })

  test('has no date while a sync is starting or when no run exists', () => {
    expect(syncTime(null, true)).toBe('')
    expect(syncTime(run, true)).toBe('')
    expect(syncTime(null)).toBe('')
  })
})

describe('syncDetails', () => {
  const run = {status: 'partial', startedAt, finishedAt, gamesSynced: 5, gamesTotal: 6, gamesFailed: 1}

  test('counts synced and failed games without the date, which syncTime shows', () => {
    expect(syncDetails(run)).toBe('5/6 games synced, 1 failed')
  })

  test('counts a still-running record without a date', () => {
    expect(syncDetails({...run, status: 'running', finishedAt: ''}, true)).toBe('5/6 games synced, 1 failed')
  })

  test('explains a sync that is starting and a missing run', () => {
    expect(syncDetails(null, true)).toBe('Starting sync…')
    expect(syncDetails(null)).toBe('Manual sync or startup sync will populate the dashboard.')
  })
})
