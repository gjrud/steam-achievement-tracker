const numberFormatter = new Intl.NumberFormat()
const dateTimeFormatter = new Intl.DateTimeFormat(undefined, {dateStyle: 'medium', timeStyle: 'short', hour12: false})

export function formatMessage(err) {
  if (!err) return 'Unknown error'
  return err.message || err.toString?.() || String(err)
}

export function integer(value) {
  const n = Number(value)
  return numberFormatter.format(Number.isFinite(n) ? n : 0)
}

export function pct(value) {
  const n = Number(value)
  if (!Number.isFinite(n)) return '0%'
  return `${n.toFixed(n % 1 === 0 ? 0 : 1)}%`
}

export function formatPlaytime(minutes) {
  const total = Math.max(0, Math.floor(Number(minutes) || 0))
  if (total < 60) return `${integer(total)}m`
  const hours = Math.floor(total / 60)
  const mins = total % 60
  return `${integer(hours)}h ${integer(mins)}m`
}

export function formatDateTime(value) {
  if (!value) return 'Unknown'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : dateTimeFormatter.format(parsed)
}

const MINUTE_MS = 60 * 1000
const HOUR_MS = 60 * MINUTE_MS
const DAY_MS = 24 * HOUR_MS

// formatRelativeTime words a timestamp against now ("5 min ago", "2 hours ago"). After a week it falls back to the
// absolute date, and input that cannot be parsed comes back unchanged.
export function formatRelativeTime(value, now = Date.now()) {
  if (!value) return ''
  const then = new Date(value).getTime()
  if (Number.isNaN(then)) return value
  const elapsed = now - then
  if (elapsed >= 7 * DAY_MS) return formatDateTime(value)
  if (elapsed < MINUTE_MS) return 'just now'
  if (elapsed < HOUR_MS) return `${Math.floor(elapsed / MINUTE_MS)} min ago`
  if (elapsed < DAY_MS) {
    const hours = Math.floor(elapsed / HOUR_MS)
    return hours === 1 ? '1 hour ago' : `${hours} hours ago`
  }
  const days = Math.floor(elapsed / DAY_MS)
  return days === 1 ? '1 day ago' : `${days} days ago`
}
