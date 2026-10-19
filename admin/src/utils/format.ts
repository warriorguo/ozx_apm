import { format, parseISO } from 'date-fns'

export function formatNumber(value: number, decimals = 0): string {
  if (value >= 1_000_000) {
    return (value / 1_000_000).toFixed(1) + 'M'
  }
  if (value >= 1_000) {
    return (value / 1_000).toFixed(1) + 'K'
  }
  return value.toFixed(decimals)
}

export function formatPercent(value: number, decimals = 2): string {
  return (value * 100).toFixed(decimals) + '%'
}

export function formatMs(value: number, decimals = 0): string {
  if (value >= 1000) {
    return (value / 1000).toFixed(1) + 's'
  }
  return value.toFixed(decimals) + 'ms'
}

export function formatDateTime(isoString: string): string {
  try {
    return format(parseISO(isoString), 'MMM d, yyyy HH:mm')
  } catch {
    return isoString
  }
}

export function formatDate(isoString: string): string {
  try {
    return format(parseISO(isoString), 'MMM d, yyyy')
  } catch {
    return isoString
  }
}

// The reason a request failed, for the analysis pages (APM-9): the server's
// own message for a 400 (e.g. "time range longer than 31 days") or 503
// ("the query took too long"), cut to 200 characters; rendered as text.
export function requestError(err: unknown, fallback: string): string {
  const r = (err as { response?: { status?: number; data?: unknown } } | null)?.response
  if (r && (r.status === 400 || r.status === 503) && typeof r.data === 'string' && r.data.trim() !== '') {
    const msg = r.data.trim()
    return `${fallback} ${msg.length > 200 ? msg.slice(0, 200) + '…' : msg}`
  }
  return fallback
}

export function formatTime(isoString: string): string {
  try {
    return format(parseISO(isoString), 'HH:mm')
  } catch {
    return isoString
  }
}
