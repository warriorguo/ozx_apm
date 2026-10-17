import { useQuery } from '@tanstack/react-query'
import * as api from '../services/api'
import type { CustomGroup, ExceptionGroupBy, ExceptionSort, FilterParams, PaginationParams, SessionTimeline } from '../types/api'

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

export function useExceptions(
  params: FilterParams & PaginationParams & { sort?: ExceptionSort; first_error?: string }
) {
  return useQuery({
    queryKey: ['exceptions', params],
    queryFn: () => api.getExceptions(params),
  })
}

// APM-11: the exception detail page
export function useExceptionDetail(fingerprint: string, params: FilterParams) {
  return useQuery({
    queryKey: ['exception-detail', fingerprint, params],
    queryFn: () => api.getExceptionDetail(fingerprint, params),
    enabled: !!fingerprint,
    retry: false,
  })
}

export function useExceptionOccurrences(fingerprint: string, params: FilterParams & PaginationParams) {
  return useQuery({
    queryKey: ['exception-occurrences', fingerprint, params],
    queryFn: () => api.getExceptionOccurrences(fingerprint, params),
    enabled: !!fingerprint,
  })
}

export function useExceptionDistribution(fingerprint: string, group: ExceptionGroupBy, params: FilterParams) {
  return useQuery({
    queryKey: ['exception-distribution', fingerprint, group, params],
    queryFn: () => api.getExceptionDistribution(fingerprint, group, params),
    enabled: !!fingerprint && (group.kind === 'by' || isValidContextKey(group.key)),
  })
}

// APM-15: the session time line
export function useSessionTimeline(params: api.SessionTimelineParams) {
  return useQuery({
    queryKey: ['session-timeline', params],
    queryFn: () => api.getSessionTimeline(params),
    enabled: !!params.session_id,
    // Keep showing the session while a filter (e.g. the perf track) reloads it.
    placeholderData: (prev: SessionTimeline | undefined) => (prev?.session_id === params.session_id ? prev : undefined),
    retry: false,
  })
}

export function useDeviceSessions(deviceId: string, params: { start_time?: string; end_time?: string }) {
  return useQuery({
    queryKey: ['device-sessions', deviceId, params],
    queryFn: () => api.getDeviceSessions(deviceId, params),
    enabled: !!deviceId,
    retry: false,
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

// Custom events and metrics (APM-6)
export function useCustomEvents(params: FilterParams) {
  return useQuery({
    queryKey: ['custom-events', params],
    queryFn: () => api.getCustomEvents(params),
  })
}

export function useCustomMetrics(params: FilterParams) {
  return useQuery({
    queryKey: ['custom-metrics', params],
    queryFn: () => api.getCustomMetrics(params),
  })
}

export function useCustomDistribution(
  kind: 'events' | 'metrics',
  name: string,
  group: CustomGroup,
  params: FilterParams,
  enabled = true
) {
  return useQuery({
    queryKey: ['custom-distribution', kind, name, group, params],
    queryFn: () => api.getCustomDistribution(kind, name, group, params),
    enabled: enabled && isValidContextKey(name) && (group.kind === 'by' || isValidContextKey(group.key)),
  })
}
