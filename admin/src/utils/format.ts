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

export function formatTime(isoString: string): string {
  try {
    return format(parseISO(isoString), 'HH:mm')
  } catch {
    return isoString
  }
}
