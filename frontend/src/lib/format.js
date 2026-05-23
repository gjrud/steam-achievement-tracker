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
