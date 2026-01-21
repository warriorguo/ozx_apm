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
  return data
}

export async function getTimeSeries(
  metric: string,
  params: FilterParams
): Promise<TimeSeriesResponse> {
  const { data } = await api.get<TimeSeriesResponse>('/timeseries', {
    params: { metric, ...params },
  })
  return data
}

export async function getDistribution(
  metric: string,
  params: FilterParams & { scene?: string }
): Promise<DistributionResponse> {
  const { data } = await api.get<DistributionResponse>('/distribution', {
    params: { metric, ...params },
  })
  return data
}

export async function getAppVersions(): Promise<string[]> {
  const { data } = await api.get<{ versions: string[] }>('/versions')
  return data.versions
}

export async function getScenes(appVersion?: string): Promise<string[]> {
  const { data } = await api.get<{ scenes: string[] }>('/scenes', {
    params: { app_version: appVersion },
  })
  return data.scenes
}

// Crash APIs
export async function getCrashes(
  params: FilterParams & PaginationParams
): Promise<CrashListResponse> {
  const { data } = await api.get<CrashListResponse>('/crashes', { params })
  return data
}

export async function getCrashDetail(
  fingerprint: string,
  params: FilterParams
): Promise<CrashDetail> {
  const { data } = await api.get<CrashDetail>('/crashes/detail', {
    params: { fingerprint, ...params },
  })
  return data
}

// Exception APIs
export async function getExceptions(
  params: FilterParams & PaginationParams
): Promise<ExceptionListResponse> {
  const { data } = await api.get<ExceptionListResponse>('/exceptions', { params })
  return data
}
