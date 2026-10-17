import type { BreadcrumbKind, ExceptionOccurrence } from '../types/api'

// Breadcrumb lines as the SDK sends them (APM-4/APM-6): "[HH:mm:ss.fff] Kind: text",
// UTC; a log line the SDK counted ends in " (x37 until 12:00:05.120)".
// The text comes from game logs: it is only ever rendered as React text.
export interface ParsedBreadcrumb {
  clock: string // HH:mm:ss.fff (UTC), '' when the line has none
  kind: BreadcrumbKind
  text: string
  repeats: number // 0 unless the SDK counted the line
  until: string // clock of the last repeat
}

const prefixes: [string, BreadcrumbKind][] = [
  ['Event: ', 'event'],
  ['Scene: ', 'scene'],
  ['Warning: ', 'warning'],
  ['Error: ', 'error'],
  ['Assert: ', 'assert'],
  ['Exception: ', 'exception'],
]

const clockRe = /^\[(\d{2}:\d{2}:\d{2}\.\d{3})\] ?/
const repeatRe = / \(x(\d+) until (\d{2}:\d{2}:\d{2}\.\d{3})\)$/

export function parseBreadcrumb(line: string): ParsedBreadcrumb {
  let text = line ?? ''
  let clock = ''
  const m = clockRe.exec(text)
  if (m) {
    clock = m[1]
    text = text.slice(m[0].length)
  }
  let kind: BreadcrumbKind = 'text'
  for (const [p, k] of prefixes) {
    if (text.startsWith(p)) {
      kind = k
      text = text.slice(p.length)
      break
    }
  }
  let repeats = 0
  let until = ''
  if (kind === 'warning' || kind === 'error' || kind === 'assert') {
    const r = repeatRe.exec(text)
    if (r && Number(r[1]) >= 2) {
      repeats = Number(r[1])
      until = r[2]
      text = text.slice(0, r.index)
    }
  }
  return { clock, kind, text, repeats, until }
}

// Tailwind classes per kind (static strings: never built from data).
export const kindStyles: Record<BreadcrumbKind, { label: string; className: string }> = {
  event: { label: 'Event', className: 'bg-blue-100 text-blue-800' },
  scene: { label: 'Scene', className: 'bg-purple-100 text-purple-800' },
  warning: { label: 'Warning', className: 'bg-yellow-100 text-yellow-800' },
  error: { label: 'Error', className: 'bg-red-100 text-red-800' },
  assert: { label: 'Assert', className: 'bg-red-100 text-red-800' },
  exception: { label: 'Exception', className: 'bg-orange-100 text-orange-800' },
  text: { label: 'Note', className: 'bg-gray-100 text-gray-700' },
}

function iso(ms: number): string {
  return new Date(ms).toISOString()
}

// Link to an exception group's detail page. Fingerprints are hex today, but
// they are data: always encoded.
export function exceptionDetailLink(fingerprint: string): string {
  return '/exceptions/detail?' + new URLSearchParams({ fingerprint }).toString()
}

// Link to the session time line anchored at an occurrence (APM-15): the range
// starts a minute before the session started (time - session_ms) and ends a
// day after the occurrence; without session_ms, a day before.
export function sessionLink(o: Pick<ExceptionOccurrence, 'session_id' | 'timestamp' | 'session_ms'>): string {
  const params = new URLSearchParams({ session_id: o.session_id })
  const at = Date.parse(o.timestamp)
  if (!Number.isNaN(at)) {
    const day = 24 * 3600 * 1000
    const start = o.session_ms > 0 ? at - o.session_ms - 60 * 1000 : at - day
    params.set('at', iso(at))
    params.set('start_time', iso(start))
    params.set('end_time', iso(at + day))
  }
  return '/sessions?' + params.toString()
}

// A device's sessions around a moment (an occurrence, a session): two weeks
// on each side (the server allows 31 days); without a time, the last 7 days.
export function deviceSessionsLink(deviceId: string, around?: string): string {
  const params = new URLSearchParams({ device_id: deviceId })
  const t = around ? Date.parse(around) : NaN
  if (!Number.isNaN(t)) {
    const span = 14 * 24 * 3600 * 1000
    params.set('start_time', iso(t - span))
    params.set('end_time', iso(t + span))
  }
  return '/sessions?' + params.toString()
}

// fillTrend adds the empty buckets between the points (the server leaves
// them out), so a chart does not draw a line across periods without
// occurrences.
export function fillTrend<T extends { timestamp: string; count: number }>(
  points: T[],
  intervalSeconds: number
): { timestamp: string; count: number }[] {
  if (points.length < 2 || intervalSeconds <= 0) return points
  const step = intervalSeconds * 1000
  const out: { timestamp: string; count: number }[] = []
  let prev = Date.parse(points[0].timestamp)
  out.push(points[0])
  for (let i = 1; i < points.length; i++) {
    const t = Date.parse(points[i].timestamp)
    // At most 1000 filled buckets in all (the server returns at most ~200).
    for (let g = prev + step; g < t && out.length < 1000; g += step) {
      out.push({ timestamp: iso(g), count: 0 })
    }
    out.push(points[i])
    prev = t
  }
  return out
}

// "1m 23.4s" for milliseconds since the session start.
export function formatSessionMs(ms: number): string {
  if (!ms || ms < 0) return ''
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${(s - m * 60).toFixed(0)}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m - h * 60}m`
}

// HH:mm:ss.SSS in UTC (the breadcrumbs' clock).
export function utcClock(isoString: string): string {
  const t = Date.parse(isoString)
  if (Number.isNaN(t)) return ''
  return new Date(t).toISOString().slice(11, 23)
}
