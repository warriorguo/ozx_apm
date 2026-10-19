import { describe, it, expect } from 'vitest'
import {
  formatNumber,
  formatPercent,
  formatMs,
  formatDateTime,
  formatDate,
  formatTime,
  requestError,
} from './format'

describe('requestError (APM-9)', () => {
  it("adds the server's message for a 400 or 503, bounded", () => {
    const e400 = { response: { status: 400, data: 'time range longer than 31 days: narrow it\n' } }
    expect(requestError(e400, 'Could not load.')).toBe('Could not load. time range longer than 31 days: narrow it')
    const long = { response: { status: 503, data: 'x'.repeat(500) } }
    expect(requestError(long, 'F.').length).toBe('F. '.length + 201)
  })
  it('keeps the fallback otherwise', () => {
    expect(requestError({ response: { status: 500, data: 'internal error' } }, 'F.')).toBe('F.')
    expect(requestError(new Error('network'), 'F.')).toBe('F.')
    expect(requestError(null, 'F.')).toBe('F.')
    expect(requestError({ response: { status: 400, data: { a: 1 } } }, 'F.')).toBe('F.')
  })
})

describe('formatNumber', () => {
  it('formats small numbers', () => {
    expect(formatNumber(0)).toBe('0')
    expect(formatNumber(100)).toBe('100')
    expect(formatNumber(999)).toBe('999')
  })

  it('formats thousands with K suffix', () => {
    expect(formatNumber(1000)).toBe('1.0K')
    expect(formatNumber(1500)).toBe('1.5K')
    expect(formatNumber(999999)).toBe('1000.0K')
  })

  it('formats millions with M suffix', () => {
    expect(formatNumber(1000000)).toBe('1.0M')
    expect(formatNumber(2500000)).toBe('2.5M')
  })

  it('respects decimal parameter', () => {
    expect(formatNumber(123.456, 2)).toBe('123.46')
    expect(formatNumber(123.456, 0)).toBe('123')
  })
})

describe('formatPercent', () => {
  it('formats decimal as percentage', () => {
    expect(formatPercent(0.5)).toBe('50.00%')
    expect(formatPercent(0.123)).toBe('12.30%')
    expect(formatPercent(1)).toBe('100.00%')
  })

  it('respects decimal parameter', () => {
    expect(formatPercent(0.12345, 1)).toBe('12.3%')
    expect(formatPercent(0.12345, 0)).toBe('12%')
  })
})

describe('formatMs', () => {
  it('formats milliseconds', () => {
    expect(formatMs(0)).toBe('0ms')
    expect(formatMs(100)).toBe('100ms')
    expect(formatMs(999)).toBe('999ms')
  })

  it('converts to seconds for large values', () => {
    expect(formatMs(1000)).toBe('1.0s')
    expect(formatMs(2500)).toBe('2.5s')
  })

  it('respects decimal parameter', () => {
    expect(formatMs(123.456, 2)).toBe('123.46ms')
  })
})

describe('formatDateTime', () => {
  it('formats ISO string to readable date time', () => {
    const result = formatDateTime('2024-01-15T10:30:00Z')
    expect(result).toContain('2024')
    expect(result).toContain('15')
  })

  it('returns original string on invalid input', () => {
    expect(formatDateTime('invalid')).toBe('invalid')
  })
})

describe('formatDate', () => {
  it('formats ISO string to readable date', () => {
    const result = formatDate('2024-01-15T10:30:00Z')
    expect(result).toContain('2024')
    expect(result).toContain('15')
  })

  it('returns original string on invalid input', () => {
    expect(formatDate('invalid')).toBe('invalid')
  })
})

describe('formatTime', () => {
  it('formats ISO string to time only', () => {
    const result = formatTime('2024-01-15T10:30:00Z')
    // Result depends on timezone, just verify it's not the original
    expect(result).not.toBe('2024-01-15T10:30:00Z')
  })

  it('returns original string on invalid input', () => {
    expect(formatTime('invalid')).toBe('invalid')
  })
})
