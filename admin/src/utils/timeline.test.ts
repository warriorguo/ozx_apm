import { describe, it, expect } from 'vitest'
import { parseBreadcrumb, sessionLink, exceptionDetailLink, deviceSessionsLink, fillTrend, formatSessionMs, utcClock } from './timeline'

describe('parseBreadcrumb', () => {
  it('reads the time, kind and text of the SDK format', () => {
    expect(parseBreadcrumb('[10:00:01.512] Event: room_enter roomId=12')).toEqual({
      clock: '10:00:01.512',
      kind: 'event',
      text: 'room_enter roomId=12',
      repeats: 0,
      until: '',
    })
    expect(parseBreadcrumb('[10:00:00.000] Scene: Main').kind).toBe('scene')
    expect(parseBreadcrumb('[10:00:00.000] Error: x').kind).toBe('error')
    expect(parseBreadcrumb('[10:00:00.000] Assert: x').kind).toBe('assert')
    expect(parseBreadcrumb('[10:00:00.000] Exception: NullReferenceException: y').text).toBe('NullReferenceException: y')
    expect(parseBreadcrumb('[10:00:00.000] boss phase 2')).toMatchObject({ kind: 'text', text: 'boss phase 2' })
  })

  it('reads the count of a repeated log line, only for log lines', () => {
    expect(parseBreadcrumb('[10:00:01.000] Warning: Pool not found for enemy 12 (x300 until 10:00:20.120)')).toEqual({
      clock: '10:00:01.000',
      kind: 'warning',
      text: 'Pool not found for enemy 12',
      repeats: 300,
      until: '10:00:20.120',
    })
    expect(parseBreadcrumb('[10:00:01.000] Event: e (x3 until 10:00:02.000)').repeats).toBe(0)
    expect(parseBreadcrumb('[10:00:01.000] Warning: w (x1 until 10:00:02.000)').repeats).toBe(0)
  })

  it('keeps lines without a time or with odd content', () => {
    expect(parseBreadcrumb('plain')).toEqual({ clock: '', kind: 'text', text: 'plain', repeats: 0, until: '' })
    expect(parseBreadcrumb('')).toMatchObject({ clock: '', text: '' })
    expect(parseBreadcrumb('[10:00] Event: x')).toMatchObject({ clock: '', kind: 'text' })
    expect(parseBreadcrumb('[10:00:00.000] Warning: <b>x</b>').text).toBe('<b>x</b>')
  })
})

describe('links', () => {
  it('anchors the session link at the occurrence, from a minute before the session start', () => {
    const href = sessionLink({ session_id: 'a&b=c', timestamp: '2026-10-18T10:00:10.123Z', session_ms: 10123 })
    const u = new URL(href, 'http://x')
    expect(u.pathname).toBe('/sessions')
    expect(u.searchParams.get('session_id')).toBe('a&b=c')
    expect(u.searchParams.get('at')).toBe('2026-10-18T10:00:10.123Z')
    expect(u.searchParams.get('start_time')).toBe('2026-10-18T09:59:00.000Z')
    expect(u.searchParams.get('end_time')).toBe('2026-10-19T10:00:10.123Z')
  })

  it('searches a day back without session_ms, and only by id without a time', () => {
    const u = new URL(sessionLink({ session_id: 's', timestamp: '2026-10-18T10:00:00Z', session_ms: 0 }), 'http://x')
    expect(u.searchParams.get('start_time')).toBe('2026-10-17T10:00:00.000Z')
    expect(sessionLink({ session_id: 's', timestamp: '', session_ms: 0 })).toBe('/sessions?session_id=s')
  })

  it('looks for the sessions of a device two weeks around a moment', () => {
    const u = new URL(deviceSessionsLink('d&1', '2026-10-18T10:00:00Z'), 'http://x')
    expect(u.searchParams.get('device_id')).toBe('d&1')
    expect(u.searchParams.get('start_time')).toBe('2026-10-04T10:00:00.000Z')
    expect(u.searchParams.get('end_time')).toBe('2026-11-01T10:00:00.000Z')
    expect(deviceSessionsLink('d', 'garbage')).toBe('/sessions?device_id=d')
  })

  it('encodes ids', () => {
    expect(exceptionDetailLink('a/b?c#d')).toBe('/exceptions/detail?fingerprint=a%2Fb%3Fc%23d')
    expect(deviceSessionsLink('javascript:alert(1)')).toBe('/sessions?device_id=javascript%3Aalert%281%29')
  })
})

describe('fillTrend', () => {
  it('adds the empty buckets between points', () => {
    const pts = [
      { timestamp: '2026-10-18T10:00:00.000Z', count: 5, sessions: 1 },
      { timestamp: '2026-10-18T10:03:00.000Z', count: 2, sessions: 1 },
    ]
    expect(fillTrend(pts, 60).map((p) => [p.timestamp.slice(11, 16), p.count])).toEqual([
      ['10:00', 5],
      ['10:01', 0],
      ['10:02', 0],
      ['10:03', 2],
    ])
    expect(fillTrend(pts, 0)).toBe(pts)
    expect(fillTrend([], 60)).toEqual([])
  })
})

describe('formatting', () => {
  it('formats the time since the session start', () => {
    expect(formatSessionMs(0)).toBe('')
    expect(formatSessionMs(850)).toBe('850ms')
    expect(formatSessionMs(10123)).toBe('10.1s')
    expect(formatSessionMs(125000)).toBe('2m 5s')
    expect(formatSessionMs(2 * 3600 * 1000 + 5 * 60000)).toBe('2h 5m')
  })

  it('shows UTC clock times', () => {
    expect(utcClock('2026-10-18T10:00:01.5Z')).toBe('10:00:01.500')
    expect(utcClock('nonsense')).toBe('')
  })
})
