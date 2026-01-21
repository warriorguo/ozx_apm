import { useState, useMemo } from 'react'
import { format, subDays, subHours } from 'date-fns'

export type TimePreset = '1h' | '6h' | '24h' | '7d' | '30d' | 'custom'

interface FilterState {
  timePreset: TimePreset
  startTime: Date
  endTime: Date
  appVersion: string
  platform: string
}

export function useFilters() {
  const [filters, setFilters] = useState<FilterState>(() => {
    const endTime = new Date()
    const startTime = subHours(endTime, 24)
    return {
      timePreset: '24h',
      startTime,
      endTime,
      appVersion: '',
      platform: '',
    }
  })

  const setTimePreset = (preset: TimePreset) => {
    const endTime = new Date()
    let startTime: Date

    switch (preset) {
      case '1h':
        startTime = subHours(endTime, 1)
        break
      case '6h':
        startTime = subHours(endTime, 6)
        break
      case '24h':
        startTime = subHours(endTime, 24)
        break
      case '7d':
        startTime = subDays(endTime, 7)
        break
      case '30d':
        startTime = subDays(endTime, 30)
        break
      default:
        return
    }

    setFilters((prev) => ({
      ...prev,
      timePreset: preset,
      startTime,
      endTime,
    }))
  }

  const setCustomTime = (startTime: Date, endTime: Date) => {
    setFilters((prev) => ({
      ...prev,
      timePreset: 'custom',
      startTime,
      endTime,
    }))
  }

  const setAppVersion = (version: string) => {
    setFilters((prev) => ({ ...prev, appVersion: version }))
  }

  const setPlatform = (platform: string) => {
    setFilters((prev) => ({ ...prev, platform }))
  }

  const queryParams = useMemo(() => {
    const params: Record<string, string> = {
      start_time: filters.startTime.toISOString(),
      end_time: filters.endTime.toISOString(),
    }
    if (filters.appVersion) {
      params.app_version = filters.appVersion
    }
    if (filters.platform) {
      params.platform = filters.platform
    }
    return params
  }, [filters])

  const formattedRange = useMemo(() => {
    return `${format(filters.startTime, 'MMM d, HH:mm')} - ${format(filters.endTime, 'MMM d, HH:mm')}`
  }, [filters.startTime, filters.endTime])

  return {
    filters,
    queryParams,
    formattedRange,
    setTimePreset,
    setCustomTime,
    setAppVersion,
    setPlatform,
  }
}
