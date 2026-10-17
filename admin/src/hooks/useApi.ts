import { useQuery } from '@tanstack/react-query'
import * as api from '../services/api'
import type { FilterParams, PaginationParams } from '../types/api'

export function useSummary(params: FilterParams) {
  return useQuery({
    queryKey: ['summary', params],
    queryFn: () => api.getSummary(params),
  })
}

export function useTimeSeries(metric: string, params: FilterParams) {
  return useQuery({
    queryKey: ['timeseries', metric, params],
    queryFn: () => api.getTimeSeries(metric, params),
    enabled: !!metric,
  })
}

export function useDistribution(metric: string, params: FilterParams & { scene?: string }) {
  return useQuery({
    queryKey: ['distribution', metric, params],
    queryFn: () => api.getDistribution(metric, params),
    enabled: !!metric,
  })
}

export function useAppVersions() {
  return useQuery({
    queryKey: ['versions'],
    queryFn: api.getAppVersions,
    staleTime: 5 * 60 * 1000,
  })
}

export function useScenes(appVersion?: string) {
  return useQuery({
    queryKey: ['scenes', appVersion],
    queryFn: () => api.getScenes(appVersion),
    staleTime: 5 * 60 * 1000,
  })
}

export function useCrashes(params: FilterParams & PaginationParams) {
  return useQuery({
    queryKey: ['crashes', params],
    queryFn: () => api.getCrashes(params),
  })
}

export function useCrashDetail(fingerprint: string, params: FilterParams) {
  return useQuery({
    queryKey: ['crash-detail', fingerprint, params],
    queryFn: () => api.getCrashDetail(fingerprint, params),
    enabled: !!fingerprint,
  })
}

export function useExceptions(params: FilterParams & PaginationParams) {
  return useQuery({
    queryKey: ['exceptions', params],
    queryFn: () => api.getExceptions(params),
  })
}

// Game context (APM-5)
export function useContextKeys(params: FilterParams & { source?: string }, enabled = true) {
  return useQuery({
    queryKey: ['context-keys', params],
    queryFn: () => api.getContextKeys(params),
    staleTime: 60 * 1000,
    enabled,
  })
}

export function useContextValues(key: string, params: FilterParams & { source?: string }) {
  return useQuery({
    queryKey: ['context-values', key, params],
    queryFn: () => api.getContextValues(key, params),
    staleTime: 60 * 1000,
    enabled: isValidContextKey(key),
  })
}

export function useContextDistribution(
  key: string,
  metric: string,
  params: FilterParams & { fingerprint?: string }
) {
  return useQuery({
    queryKey: ['context-distribution', key, metric, params],
    queryFn: () => api.getContextDistribution(key, metric, params),
    enabled: isValidContextKey(key) && !!metric,
  })
}

// Same rule as the SDK and the server: 1-64 characters from [A-Za-z0-9_.-].
export function isValidContextKey(key: string): boolean {
  return /^[A-Za-z0-9_.-]{1,64}$/.test(key)
}
