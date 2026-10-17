import { createContext, createElement, useContext, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { format, subDays, subHours } from 'date-fns'
import type { BuildTypeFilter, ContextFilter } from '../types/api'

export type TimePreset = '1h' | '6h' | '24h' | '7d' | '30d' | 'custom'

interface FilterState {
  timePreset: TimePreset
  startTime: Date
  endTime: Date
  appVersion: string
  platform: string
  // APM-5: release by default (the server then also includes events from
  // SDKs without a build type).
  buildType: BuildTypeFilter
  contextFilters: ContextFilter[]
}

export interface QueryParams {
  start_time: string
  end_time: string
  app_version?: string
  platform?: string
  build_type: BuildTypeFilter
  ctx?: string[]
}

function useFilterState() {
  const [filters, setFilters] = useState<FilterState>(() => {
    const endTime = new Date()
    const startTime = subHours(endTime, 24)
    return {
      timePreset: '24h',
      startTime,
      endTime,
      appVersion: '',
      platform: '',
      buildType: 'release',
      contextFilters: [],
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

  const setBuildType = (buildType: BuildTypeFilter) => {
    setFilters((prev) => ({ ...prev, buildType }))
  }

  // Adding the same key:value twice is a no-op.
  const addContextFilter = (filter: ContextFilter) => {
    setFilters((prev) =>
      prev.contextFilters.some((f) => f.key === filter.key && f.value === filter.value)
        ? prev
        : { ...prev, contextFilters: [...prev.contextFilters, filter] }
    )
  }

  const removeContextFilter = (index: number) => {
    setFilters((prev) => ({
      ...prev,
      contextFilters: prev.contextFilters.filter((_, i) => i !== index),
    }))
  }

  const clearContextFilters = () => {
    setFilters((prev) => ({ ...prev, contextFilters: [] }))
  }

  const queryParams = useMemo(() => {
    const params: QueryParams = {
      start_time: filters.startTime.toISOString(),
      end_time: filters.endTime.toISOString(),
      build_type: filters.buildType,
    }
    if (filters.appVersion) {
      params.app_version = filters.appVersion
    }
    if (filters.platform) {
      params.platform = filters.platform
    }
    if (filters.contextFilters.length > 0) {
      params.ctx = filters.contextFilters.map((f) => `${f.key}:${f.value}`)
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
    setBuildType,
    addContextFilter,
    removeContextFilter,
    clearContextFilters,
  }
}

export type Filters = ReturnType<typeof useFilterState>

const FiltersContext = createContext<Filters | null>(null)

// FiltersProvider shares one filter state between all pages, so the build
// type and context filters stay set when switching pages.
export function FiltersProvider({ children }: { children: ReactNode }) {
  const value = useFilterState()
  return createElement(FiltersContext.Provider, { value }, children)
}

// useFilters returns the shared filters inside a FiltersProvider, and a
// component-local state outside of one.
export function useFilters(): Filters {
  const shared = useContext(FiltersContext)
  const local = useFilterState()
  return shared ?? local
}
