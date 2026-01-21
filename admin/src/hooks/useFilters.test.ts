import { describe, it, expect } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useFilters } from './useFilters'

describe('useFilters', () => {
  it('initializes with default values', () => {
    const { result } = renderHook(() => useFilters())

    expect(result.current.filters.timePreset).toBe('24h')
    expect(result.current.filters.appVersion).toBe('')
    expect(result.current.filters.platform).toBe('')
  })

  it('setTimePreset updates time range', () => {
    const { result } = renderHook(() => useFilters())

    act(() => {
      result.current.setTimePreset('7d')
    })

    expect(result.current.filters.timePreset).toBe('7d')

    // Check that time range is ~7 days
    const duration = result.current.filters.endTime.getTime() - result.current.filters.startTime.getTime()
    const days = duration / (24 * 60 * 60 * 1000)
    expect(days).toBeCloseTo(7, 0)
  })

  it('setTimePreset works for all presets', () => {
    const { result } = renderHook(() => useFilters())

    const presets: Array<'1h' | '6h' | '24h' | '7d' | '30d'> = ['1h', '6h', '24h', '7d', '30d']

    for (const preset of presets) {
      act(() => {
        result.current.setTimePreset(preset)
      })
      expect(result.current.filters.timePreset).toBe(preset)
    }
  })

  it('setCustomTime sets custom time range', () => {
    const { result } = renderHook(() => useFilters())

    const start = new Date('2024-01-01T00:00:00Z')
    const end = new Date('2024-01-15T00:00:00Z')

    act(() => {
      result.current.setCustomTime(start, end)
    })

    expect(result.current.filters.timePreset).toBe('custom')
    expect(result.current.filters.startTime).toEqual(start)
    expect(result.current.filters.endTime).toEqual(end)
  })

  it('setAppVersion updates app version filter', () => {
    const { result } = renderHook(() => useFilters())

    act(() => {
      result.current.setAppVersion('1.0.0')
    })

    expect(result.current.filters.appVersion).toBe('1.0.0')
  })

  it('setPlatform updates platform filter', () => {
    const { result } = renderHook(() => useFilters())

    act(() => {
      result.current.setPlatform('Android')
    })

    expect(result.current.filters.platform).toBe('Android')
  })

  it('queryParams includes time range', () => {
    const { result } = renderHook(() => useFilters())

    expect(result.current.queryParams).toHaveProperty('start_time')
    expect(result.current.queryParams).toHaveProperty('end_time')
  })

  it('queryParams includes filters when set', () => {
    const { result } = renderHook(() => useFilters())

    act(() => {
      result.current.setAppVersion('1.0.0')
      result.current.setPlatform('iOS')
    })

    expect(result.current.queryParams.app_version).toBe('1.0.0')
    expect(result.current.queryParams.platform).toBe('iOS')
  })

  it('queryParams excludes empty filters', () => {
    const { result } = renderHook(() => useFilters())

    expect(result.current.queryParams).not.toHaveProperty('app_version')
    expect(result.current.queryParams).not.toHaveProperty('platform')
  })

  it('formattedRange returns readable string', () => {
    const { result } = renderHook(() => useFilters())

    expect(result.current.formattedRange).toMatch(/\w+ \d+/)
  })
})
