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
