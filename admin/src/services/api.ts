import axios from 'axios'
import type {
  DashboardSummary,
  TimeSeriesResponse,
  DistributionResponse,
  CrashListResponse,
  CrashDetail,
  ExceptionListResponse,
  FilterParams,
  PaginationParams,
} from '../types/api'

const api = axios.create({
  baseURL: '/api',
  timeout: 30000,
})

// Dashboard APIs
export async function getSummary(params: FilterParams): Promise<DashboardSummary> {
  const { data } = await api.get<DashboardSummary>('/summary', { params })
  if (!data) {
    return {
      time_range: { start: '', end: '' },
      total_sessions: 0,
      total_events: 0,
      crash_count: 0,
      crash_rate: 0,
      exception_count: 0,
      jank_count: 0,
      avg_fps: 0,
      avg_startup_ms: 0,
      top_versions: [],
      top_platforms: [],
    }
  }
  return {
    ...data,
    top_versions: data.top_versions || [],
    top_platforms: data.top_platforms || [],
  }
}

export async function getTimeSeries(
  metric: string,
  params: FilterParams
): Promise<TimeSeriesResponse> {
  const { data } = await api.get<TimeSeriesResponse>('/timeseries', {
    params: { metric, ...params },
  })
  if (!data) {
    return { metric, data: [] }
  }
  return { ...data, data: data.data || [] }
}

export async function getDistribution(
  metric: string,
  params: FilterParams & { scene?: string }
): Promise<DistributionResponse> {
  const { data } = await api.get<DistributionResponse>('/distribution', {
    params: { metric, ...params },
  })
  if (!data) {
    return { metric, buckets: [], p50: 0, p90: 0, p95: 0, p99: 0 }
  }
  return { ...data, buckets: data.buckets || [] }
}

export async function getAppVersions(): Promise<string[]> {
  const { data } = await api.get<{ versions: string[] }>('/versions')
  return data?.versions || []
}

export async function getScenes(appVersion?: string): Promise<string[]> {
  const { data } = await api.get<{ scenes: string[] }>('/scenes', {
    params: { app_version: appVersion },
  })
  return data?.scenes || []
}

// Crash APIs
export async function getCrashes(
  params: FilterParams & PaginationParams
): Promise<CrashListResponse> {
  const { data } = await api.get<CrashListResponse>('/crashes', { params })
  if (!data) {
    return { crashes: [], total_count: 0, page: 1, page_size: 20 }
  }
  return { ...data, crashes: data.crashes || [] }
}

export async function getCrashDetail(
  fingerprint: string,
  params: FilterParams
): Promise<CrashDetail> {
  const { data } = await api.get<CrashDetail>('/crashes/detail', {
    params: { fingerprint, ...params },
  })
  if (!data) {
    throw new Error('Crash detail not found')
  }
  return {
    ...data,
    occurrences: data.occurrences || [],
    version_distribution: data.version_distribution || [],
    device_distribution: data.device_distribution || [],
    os_distribution: data.os_distribution || [],
  }
}

// Exception APIs
export async function getExceptions(
  params: FilterParams & PaginationParams
): Promise<ExceptionListResponse> {
  const { data } = await api.get<ExceptionListResponse>('/exceptions', { params })
  if (!data) {
    return { exceptions: [], total_count: 0, page: 1, page_size: 20 }
  }
  return { ...data, exceptions: data.exceptions || [] }
}
